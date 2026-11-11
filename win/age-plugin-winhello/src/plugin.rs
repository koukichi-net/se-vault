use age_core::format::{FileKey, Stanza};
use age_plugin::{Callbacks, PluginHandler, identity, recipient, run_state_machine};
use sewin::ngc::{self, Key};
use std::collections::{HashMap, HashSet};
use std::io::{self, BufRead, Write};
use std::path::Path;
use winhello_core::{self as core_, Identity};

const VERSION: &str = env!("CARGO_PKG_VERSION");

/// se-env puts what it asks Hello for in this variable while it runs the
/// plugin (cmd/se-env/reason.go). It is shown in the Hello dialog, and only
/// when the parent is the se-env.exe next to this plugin.
const REASON_ENV: &str = "SE_ENV_AUTH_REASON";
const MAX_REASON: usize = 2000;

pub fn main() -> i32 {
    // The file key passes through this process: keep other processes of the
    // same user out of it, and do nothing if that cannot be done.
    if let Err(e) = sewin::process::harden_self() {
        eprintln!(
            "age-plugin-winhello: this process could not be protected from other programs (error {e}); refusing to run"
        );
        return 1;
    }
    let args: Vec<String> = std::env::args().skip(1).collect();
    if let Some(sm) = args.iter().find_map(|a| a.strip_prefix("--age-plugin=")) {
        return match run_state_machine(sm, Handler) {
            Ok(()) => 0,
            Err(e) => {
                eprintln!("age-plugin-winhello: {e}");
                1
            }
        };
    }
    let r = match args.first().map(String::as_str) {
        Some("keygen") => keygen(&args[1..]),
        Some("recipients") => recipients(),
        Some("delete") => delete(),
        Some("--version") | Some("version") => {
            println!("age-plugin-winhello {VERSION}");
            Ok(())
        }
        _ => Err("usage: age-plugin-winhello keygen [-o FILE] | recipients | delete".into()),
    };
    match r {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("age-plugin-winhello: {e}");
            1
        }
    }
}

type Res<T> = Result<T, Box<dyn std::error::Error>>;

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

/// Turns an error code of the Hello key provider into words.
fn cng(code: u32) -> String {
    let what = match code {
        0x8009_0036 => "Windows Hello was cancelled",
        0x8009_000D | 0x8009_0016 => {
            "the Windows Hello key does not exist (removing the Windows Hello PIN deletes it)"
        }
        0x8007_139F => {
            "the Windows Hello key is no longer usable (the TPM was cleared or Windows Hello was reset)"
        }
        0x8009_0029 => {
            "Windows Hello is not set up for this user, or not available in this session"
        }
        0x8009_002D => {
            "Windows Hello cannot be shown here (no desktop, as in an SSH session), or the device is locked"
        }
        0x8009_0010 => {
            "Windows Hello refused access; it cannot be used in a Remote Desktop session (use se-env at the PC itself)"
        }
        _ => "the Windows Hello key provider failed",
    };
    format!("{what} (0x{code:08X})")
}

// ---- keygen ----------------------------------------------------------------------

fn keygen(args: &[String]) -> Res<()> {
    let out = match args {
        [] => None,
        [o, path] if o == "-o" => Some(path.clone()),
        _ => return Err("usage: age-plugin-winhello keygen [-o FILE]".into()),
    };
    if let Some(p) = &out
        && Path::new(p).exists()
    {
        return Err(format!("{p} already exists").into());
    }
    if !ngc::tpm20_present() {
        return Err(
            "this PC has no TPM 2.0; refusing to create a key that would not be protected by one"
                .into(),
        );
    }
    let name = format!("se-env-{}", hex(&core_::random32()[..8]));
    eprintln!(
        "age-plugin-winhello: creating the Windows Hello key {name} (Windows Hello asks twice)"
    );
    let key = Key::create(
        &name,
        "se-env: create the key that protects your secrets (1/2)",
    )
    .map_err(cng)?;
    let made = (|| -> Res<(Identity, String)> {
        if key.in_hardware() == Some(false) {
            return Err(
                "Windows Hello made a software key (it does not use the TPM on this PC); refusing to use it"
                    .into(),
            );
        }
        if !key.is_protected().map_err(cng)? {
            return Err(
                "the new key is exportable or usable without Windows Hello; refusing to use it"
                    .into(),
            );
        }
        let public = key.public_blob().map_err(cng)?;
        // Prove now that the key decrypts, not when the first secret is needed.
        let probe = core_::random32();
        let ct = ngc::encrypt(&public, &probe).map_err(cng)?;
        let pt = key
            .decrypt(&ct, "se-env: check that the new key works (2/2)")
            .map_err(cng)?;
        if pt[..] != probe[..] {
            return Err("the new key does not decrypt what was encrypted to it".into());
        }
        let id = Identity {
            key_name: name.clone(),
            public_sha256: core_::sha256(&public),
        };
        Ok((id, core_::encode_recipient(&public)))
    })();
    let (id, recipient) = match made {
        Ok(v) => v,
        Err(e) => {
            let _ = key.delete();
            return Err(e);
        }
    };
    let text = format!(
        "# Windows Hello key: {}\n# public key: {}\n{}\n",
        id.key_name,
        recipient,
        id.encode()
    );
    match out {
        Some(p) => {
            let mut f = std::fs::OpenOptions::new()
                .write(true)
                .create_new(true)
                .open(&p)?;
            f.write_all(text.as_bytes())?;
            eprintln!("age-plugin-winhello: created");
        }
        None => print!("{text}"),
    }
    Ok(())
}

fn identity_lines() -> Res<Vec<Identity>> {
    let mut out = vec![];
    for line in io::stdin().lock().lines() {
        let line = line?;
        let l = line.trim();
        if l.is_empty() || l.starts_with('#') {
            continue;
        }
        out.push(Identity::decode(l)?);
    }
    Ok(out)
}

/// Opens the Hello key of an identity and checks, without asking for Hello,
/// that it is the key the identity names and that it is still protected.
/// Returns the key and its public blob.
fn checked_key(id: &Identity) -> Result<(Key, Vec<u8>), String> {
    let key = Key::open(&id.key_name).map_err(cng)?;
    let public = key.public_blob().map_err(cng)?;
    if !id.matches(&public) {
        return Err("the Windows Hello key is not the one this identity was made for".into());
    }
    if !key.is_protected().map_err(cng)? {
        return Err(
            "the Windows Hello key is exportable, usable without Windows Hello, or a software key"
                .into(),
        );
    }
    Ok((key, public))
}

fn recipients() -> Res<()> {
    for id in identity_lines()? {
        let (_, public) = checked_key(&id)?;
        println!("{}", core_::encode_recipient(&public));
    }
    Ok(())
}

fn delete() -> Res<()> {
    for id in identity_lines()? {
        Key::open(&id.key_name).and_then(Key::delete).map_err(cng)?;
    }
    Ok(())
}

// ---- the age plugin protocol -----------------------------------------------------

struct Handler;

impl PluginHandler for Handler {
    type RecipientV1 = RecipientPlugin;
    type IdentityV1 = IdentityPlugin;

    fn recipient_v1(self) -> io::Result<RecipientPlugin> {
        Ok(RecipientPlugin { publics: vec![] })
    }

    fn identity_v1(self) -> io::Result<IdentityPlugin> {
        Ok(IdentityPlugin { ids: vec![] })
    }
}

struct RecipientPlugin {
    publics: Vec<Vec<u8>>,
}

impl recipient::RecipientPluginV1 for RecipientPlugin {
    fn add_recipient(
        &mut self,
        index: usize,
        plugin_name: &str,
        bytes: &[u8],
    ) -> Result<(), recipient::Error> {
        if plugin_name != core_::PLUGIN_NAME {
            return Err(recipient::Error::Recipient {
                index,
                message: "unsupported plugin".into(),
            });
        }
        let public =
            core_::public_from_recipient(bytes).map_err(|e| recipient::Error::Recipient {
                index,
                message: e.to_string(),
            })?;
        self.publics.push(public.to_vec());
        Ok(())
    }

    fn add_identity(
        &mut self,
        index: usize,
        plugin_name: &str,
        bytes: &[u8],
    ) -> Result<(), recipient::Error> {
        if plugin_name != core_::PLUGIN_NAME {
            return Err(recipient::Error::Identity {
                index,
                message: "unsupported plugin".into(),
            });
        }
        let err = |message: String| recipient::Error::Identity { index, message };
        let id = Identity::from_bytes(bytes).map_err(|e| err(e.to_string()))?;
        let (_, public) = checked_key(&id).map_err(err)?;
        self.publics.push(public);
        Ok(())
    }

    fn labels(&mut self) -> HashSet<String> {
        HashSet::new()
    }

    fn wrap_file_keys(
        &mut self,
        file_keys: Vec<FileKey>,
        _callbacks: impl Callbacks<recipient::Error>,
    ) -> io::Result<Result<Vec<Vec<Stanza>>, Vec<recipient::Error>>> {
        let mut out = vec![];
        for fk in &file_keys {
            let mut stanzas = vec![];
            for (index, public) in self.publics.iter().enumerate() {
                match core_::wrap(fk, public, |seed| ngc::encrypt(public, seed)) {
                    Ok(st) => stanzas.push(st),
                    Err(e) => {
                        return Ok(Err(vec![recipient::Error::Recipient {
                            index,
                            message: cng(e),
                        }]));
                    }
                }
            }
            out.push(stanzas);
        }
        Ok(Ok(out))
    }
}

struct IdentityPlugin {
    ids: Vec<Identity>,
}

/// What the Hello dialog says: the reason se-env gave, if this plugin was
/// started by the se-env next to it; otherwise only who is asking.
fn context() -> String {
    let fallback = || "age-plugin-winhello: decrypt a file (not started by se-env)".to_string();
    let Ok(r) = std::env::var(REASON_ENV) else {
        return fallback();
    };
    if r.is_empty() || r.len() > MAX_REASON {
        return fallback();
    }
    let started_by_se_env = (|| {
        let me = std::env::current_exe().ok()?;
        let want = me.parent()?.join("se-env.exe");
        let (_, parent) = sewin::process::parent()?;
        Some(parent.eq_ignore_ascii_case(&want.to_string_lossy()))
    })();
    if started_by_se_env == Some(true) {
        r
    } else {
        fallback()
    }
}

impl identity::IdentityPluginV1 for IdentityPlugin {
    fn add_identity(
        &mut self,
        index: usize,
        plugin_name: &str,
        bytes: &[u8],
    ) -> Result<(), identity::Error> {
        if plugin_name != core_::PLUGIN_NAME {
            return Err(identity::Error::Identity {
                index,
                message: "unsupported plugin".into(),
            });
        }
        let id = Identity::from_bytes(bytes).map_err(|e| identity::Error::Identity {
            index,
            message: e.to_string(),
        })?;
        self.ids.push(id);
        Ok(())
    }

    fn unwrap_file_keys(
        &mut self,
        files: Vec<Vec<Stanza>>,
        _callbacks: impl Callbacks<identity::Error>,
    ) -> io::Result<HashMap<usize, Result<FileKey, Vec<identity::Error>>>> {
        let mut out = HashMap::new();
        for (fi, stanzas) in files.iter().enumerate() {
            let mut errors = vec![];
            'file: for (si, st) in stanzas.iter().enumerate() {
                for (ii, id) in self.ids.iter().enumerate() {
                    // Only a stanza for this key is worth a Hello prompt.
                    if !core_::is_for(st, &id.public_sha256) {
                        continue;
                    }
                    let stanza_err = |message: String| identity::Error::Stanza {
                        file_index: fi,
                        stanza_index: si,
                        message,
                    };
                    let ct = match core_::rsa_part(st) {
                        Ok(ct) => ct,
                        Err(e) => {
                            errors.push(stanza_err(e.to_string()));
                            continue;
                        }
                    };
                    let key = match checked_key(id) {
                        Ok((key, _)) => key,
                        Err(message) => {
                            errors.push(identity::Error::Identity { index: ii, message });
                            continue;
                        }
                    };
                    // The private key stays in the Hello container: it
                    // decrypts the seed there, after the user approved.
                    match key.decrypt(ct, &context()) {
                        Err(e) => errors.push(identity::Error::Identity {
                            index: ii,
                            message: cng(e),
                        }),
                        Ok(seed) => match core_::unwrap(st, &id.public_sha256, &seed) {
                            Ok(fk) => {
                                out.insert(fi, Ok(fk));
                                errors.clear();
                                break 'file;
                            }
                            Err(e) => errors.push(stanza_err(e.to_string())),
                        },
                    }
                }
            }
            if !errors.is_empty() {
                out.insert(fi, Err(errors));
            }
        }
        Ok(out)
    }
}
