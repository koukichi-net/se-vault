//! The parts of age-plugin-winhello that need no Windows API: the identity
//! and recipient encodings and the `winhello` stanza. See
//! docs/windows.md in the se-env repository.
//!
//! The key is an RSA-2048 key in the user's Windows Hello container (CNG,
//! "Microsoft Passport Key Storage Provider"). It never leaves the TPM-backed
//! container and every decryption asks for the Hello gesture. The provider
//! only decrypts with PKCS#1 v1.5 padding, so the file key is not encrypted
//! with RSA directly: RSA carries a random seed, and the file key is sealed
//! with a key derived from it.
//!
//! ```text
//! seed    = 32 random bytes
//! stanza  -> winhello <base64(SHA-256(pub)[..4])>
//!            RSA-PKCS1(pub, seed) || ChaCha20-Poly1305(wrap key, file key)
//! wrap key = HKDF-SHA-256(salt = SHA-256(pub), "se-env/winhello/v2/wrap", seed)
//! ```
//!
//! `pub` is the key's BCRYPT_RSAPUBLIC_BLOB.

use age_core::format::{FILE_KEY_BYTES, FileKey, Stanza};
use age_core::primitives::{aead_decrypt, aead_encrypt, bech32_decode, bech32_encode, hkdf};
use age_core::secrecy::ExposeSecret;
use base64::Engine;
use base64::engine::general_purpose::STANDARD_NO_PAD as B64;
use sha2::{Digest, Sha256};
use zeroize::{Zeroize, Zeroizing};

/// The plugin's name: `age-plugin-winhello`, `AGE-PLUGIN-WINHELLO-1…`, `age1winhello1…`.
pub const PLUGIN_NAME: &str = "winhello";
pub const STANZA_TAG: &str = "winhello";
pub const IDENTITY_HRP: &str = "age-plugin-winhello-";
pub const RECIPIENT_HRP: &str = "age1winhello";

const WRAP_LABEL: &[u8] = b"se-env/winhello/v2/wrap";
/// Version 1 derived an X25519 key from a Hello signature; it was never released.
const VERSION: u8 = 2;
const TAG_BYTES: usize = 4;
pub const SEED_BYTES: usize = 32;
const SEALED_BYTES: usize = FILE_KEY_BYTES + 16;
/// A BCRYPT_RSAPUBLIC_BLOB of an RSA-2048 key is 283 bytes.
const MAX_PUBLIC: usize = 1100;
const MAX_RSA: usize = 1024;

#[derive(Debug, PartialEq, Eq)]
pub enum Error {
    Encoding,
    Version(u8),
    Mismatch,
}

impl std::fmt::Display for Error {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Error::Encoding => write!(f, "malformed encoding"),
            Error::Version(v) => write!(f, "unsupported version {v}"),
            Error::Mismatch => write!(f, "not for this key"),
        }
    }
}

impl std::error::Error for Error {}

pub fn sha256(data: &[u8]) -> [u8; 32] {
    Sha256::digest(data).into()
}

pub fn random32() -> [u8; 32] {
    let mut b = [0u8; 32];
    getrandom::fill(&mut b).expect("the operating system's random source failed");
    b
}

// ---- identity ----------------------------------------------------------------

/// What `identity.txt` holds: which Hello key, and the hash of its public
/// key. Nothing in it is secret.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Identity {
    /// Name of the key in the Hello container, e.g. "se-env-0123…".
    pub key_name: String,
    /// SHA-256 of the key's BCRYPT_RSAPUBLIC_BLOB.
    pub public_sha256: [u8; 32],
}

impl Identity {
    pub fn to_bytes(&self) -> Vec<u8> {
        let name = self.key_name.as_bytes();
        assert!(name.len() <= 255);
        let mut b = vec![VERSION, name.len() as u8];
        b.extend_from_slice(name);
        b.extend_from_slice(&self.public_sha256);
        b
    }

    pub fn from_bytes(b: &[u8]) -> Result<Self, Error> {
        let (&v, rest) = b.split_first().ok_or(Error::Encoding)?;
        if v != VERSION {
            return Err(Error::Version(v));
        }
        let (&n, rest) = rest.split_first().ok_or(Error::Encoding)?;
        if rest.len() != n as usize + 32 {
            return Err(Error::Encoding);
        }
        let (name, hash) = rest.split_at(n as usize);
        let key_name = std::str::from_utf8(name)
            .map_err(|_| Error::Encoding)?
            .to_string();
        if key_name.is_empty()
            || !key_name
                .chars()
                .all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_')
        {
            return Err(Error::Encoding);
        }
        Ok(Identity {
            key_name,
            public_sha256: hash.try_into().expect("length checked"),
        })
    }

    /// `AGE-PLUGIN-WINHELLO-1…`
    pub fn encode(&self) -> String {
        let hrp = bech32::Hrp::parse(IDENTITY_HRP).expect("valid HRP");
        bech32_encode(hrp, &self.to_bytes()).to_uppercase()
    }

    pub fn decode(s: &str) -> Result<Self, Error> {
        Identity::from_bytes(&decode_bech32(&s.to_lowercase(), IDENTITY_HRP)?)
    }

    /// Whether `public` (the blob read from the Hello key) is this identity's key.
    pub fn matches(&self, public: &[u8]) -> bool {
        sha256(public) == self.public_sha256
    }
}

fn decode_bech32(s: &str, want_hrp: &str) -> Result<Vec<u8>, Error> {
    bech32_decode(
        s,
        |_| Error::Encoding,
        |hrp| {
            if hrp.to_lowercase() == want_hrp {
                Ok(())
            } else {
                Err(Error::Encoding)
            }
        },
        |_, bytes| Ok(bytes.collect()),
    )
}

// ---- recipient ---------------------------------------------------------------

/// The data of a recipient: version, then the public key blob.
pub fn recipient_bytes(public: &[u8]) -> Vec<u8> {
    [&[VERSION][..], public].concat()
}

/// The public key blob from the data the plugin protocol carries for a recipient.
pub fn public_from_recipient(b: &[u8]) -> Result<&[u8], Error> {
    let (&v, public) = b.split_first().ok_or(Error::Encoding)?;
    if v != VERSION {
        return Err(Error::Version(v));
    }
    if public.len() < 32 || public.len() > MAX_PUBLIC {
        return Err(Error::Encoding);
    }
    Ok(public)
}

/// `age1winhello1…`
pub fn encode_recipient(public: &[u8]) -> String {
    bech32_encode(
        bech32::Hrp::parse(RECIPIENT_HRP).expect("valid HRP"),
        &recipient_bytes(public),
    )
}

pub fn decode_recipient(s: &str) -> Result<Vec<u8>, Error> {
    let b = decode_bech32(s, RECIPIENT_HRP)?;
    Ok(public_from_recipient(&b)?.to_vec())
}

// ---- stanza --------------------------------------------------------------------

fn tag_of(public_sha256: &[u8; 32]) -> String {
    B64.encode(&public_sha256[..TAG_BYTES])
}

fn wrap_key(public_sha256: &[u8; 32], seed: &[u8]) -> Zeroizing<[u8; 32]> {
    Zeroizing::new(hkdf(public_sha256, WRAP_LABEL, seed))
}

/// Wraps a file key to a public key. `rsa_encrypt` encrypts the seed to that
/// key (PKCS#1 v1.5).
pub fn wrap<E>(
    file_key: &FileKey,
    public: &[u8],
    rsa_encrypt: impl FnOnce(&[u8]) -> Result<Vec<u8>, E>,
) -> Result<Stanza, E> {
    let hash = sha256(public);
    let seed = Zeroizing::new(random32());
    let mut body = rsa_encrypt(&seed[..])?;
    body.extend_from_slice(&aead_encrypt(
        &wrap_key(&hash, &seed[..]),
        file_key.expose_secret(),
    ));
    Ok(Stanza {
        tag: STANZA_TAG.to_string(),
        args: vec![tag_of(&hash)],
        body,
    })
}

/// Whether a stanza is addressed to the key with this hash. Needs no secret,
/// so the plugin asks for Hello only when one is.
pub fn is_for(stanza: &Stanza, public_sha256: &[u8; 32]) -> bool {
    stanza.tag == STANZA_TAG && stanza.args.len() == 1 && stanza.args[0] == tag_of(public_sha256)
}

/// The RSA ciphertext of the seed in a stanza.
pub fn rsa_part(stanza: &Stanza) -> Result<&[u8], Error> {
    let n = stanza.body.len();
    if n <= SEALED_BYTES || n - SEALED_BYTES > MAX_RSA {
        return Err(Error::Encoding);
    }
    Ok(&stanza.body[..n - SEALED_BYTES])
}

/// Opens a stanza with the seed the Hello key decrypted from [`rsa_part`].
pub fn unwrap(stanza: &Stanza, public_sha256: &[u8; 32], seed: &[u8]) -> Result<FileKey, Error> {
    if !is_for(stanza, public_sha256) {
        return Err(Error::Mismatch);
    }
    if seed.len() != SEED_BYTES {
        return Err(Error::Mismatch);
    }
    let sealed = &stanza.body[rsa_part(stanza)?.len()..];
    let mut plain = aead_decrypt(&wrap_key(public_sha256, seed), FILE_KEY_BYTES, sealed)
        .map_err(|_| Error::Mismatch)?;
    let fk = FileKey::init_with_mut(|k| k.copy_from_slice(&plain));
    plain.zeroize();
    Ok(fk)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn public() -> Vec<u8> {
        let mut p = vec![0u8; 283];
        p[..32].copy_from_slice(&random32());
        p
    }

    /// Stands in for RSA: "encrypts" by XOR so the tests need no key store.
    fn fake_rsa(seed: &[u8]) -> Result<Vec<u8>, ()> {
        let mut ct = vec![0x5au8; 256];
        for (c, s) in ct.iter_mut().zip(seed) {
            *c ^= s;
        }
        Ok(ct)
    }

    fn fake_rsa_decrypt(ct: &[u8]) -> Vec<u8> {
        ct[..SEED_BYTES].iter().map(|c| c ^ 0x5a).collect()
    }

    #[test]
    fn identity_round_trip() {
        let id = Identity {
            key_name: "se-env-0123456789abcdef".into(),
            public_sha256: random32(),
        };
        let s = id.encode();
        assert!(s.starts_with("AGE-PLUGIN-WINHELLO-1"), "{s}");
        assert_eq!(Identity::decode(&s).unwrap(), id);
        assert_eq!(Identity::decode(&s.to_lowercase()).unwrap(), id);
    }

    #[test]
    fn identity_rejects_garbage() {
        let id = Identity {
            key_name: "se-env-0123".into(),
            public_sha256: random32(),
        };
        let mut b = id.to_bytes();
        b.push(0);
        assert_eq!(Identity::from_bytes(&b), Err(Error::Encoding));
        let mut b = id.to_bytes();
        b[0] = 1;
        assert_eq!(Identity::from_bytes(&b), Err(Error::Version(1)));
        assert!(Identity::from_bytes(&id.to_bytes()[..20]).is_err());
        let bad = Identity {
            key_name: "a/b".into(),
            public_sha256: random32(),
        };
        assert_eq!(Identity::from_bytes(&bad.to_bytes()), Err(Error::Encoding));
        let hrp = bech32::Hrp::parse("age-plugin-se-").unwrap();
        assert!(Identity::decode(&bech32_encode(hrp, &id.to_bytes())).is_err());
    }

    #[test]
    fn recipient_round_trip() {
        let p = public();
        let r = encode_recipient(&p);
        assert!(r.starts_with("age1winhello1"), "{r}");
        assert_eq!(decode_recipient(&r).unwrap(), p);
        assert!(public_from_recipient(&[1, 2, 3]).is_err());
    }

    #[test]
    fn wrap_unwrap() {
        let p = public();
        let hash = sha256(&p);
        let fk = FileKey::new(Box::new([9u8; 16]));
        let st = wrap(&fk, &p, fake_rsa).unwrap();
        assert!(is_for(&st, &hash));
        let seed = fake_rsa_decrypt(rsa_part(&st).unwrap());
        assert_eq!(
            unwrap(&st, &hash, &seed).unwrap().expose_secret(),
            fk.expose_secret()
        );

        let other = sha256(&public());
        assert!(!is_for(&st, &other));
        assert!(unwrap(&st, &other, &seed).is_err());
        assert!(unwrap(&st, &hash, &[0u8; 32]).is_err());
        assert!(unwrap(&st, &hash, &seed[..31]).is_err());

        let mut bad = wrap(&fk, &p, fake_rsa).unwrap();
        let n = bad.body.len();
        bad.body[n - 1] ^= 1;
        let seed = fake_rsa_decrypt(rsa_part(&bad).unwrap());
        assert!(unwrap(&bad, &hash, &seed).is_err());

        let short = Stanza {
            tag: STANZA_TAG.into(),
            args: st.args.clone(),
            body: vec![0; SEALED_BYTES],
        };
        assert!(rsa_part(&short).is_err());
    }
}
