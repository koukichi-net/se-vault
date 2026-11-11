//! Windows Hello keys through CNG (the "Microsoft Passport Key Storage
//! Provider"). The RSA key is created in the user's Hello container, which
//! the TPM protects; it never leaves it. Decryption happens inside the
//! provider and asks for the Hello gesture every time. Opening the key and
//! reading its public part ask for nothing.

use crate::{Error, Result, wide};
use windows_sys::Win32::Security::Cryptography::*;

const PROVIDER: &str = "Microsoft Passport Key Storage Provider";
const CACHE_TYPE: &str = "NgcCacheType";
const CACHE_AUTH_MANDATORY: u32 = 1;
const GESTURE_REQUIRED: &str = "PinCacheIsGestureRequired";
const USE_CONTEXT: &str = "Use Context";
const HWND_HANDLE: &str = "HWND Handle";
const EXPORT_POLICY: &str = "Export Policy";
const LENGTH: &str = "Length";
const KEY_USAGE: &str = "Key Usage";
/// Where the Hello container keeps the key (NCRYPT_IMPL_* flags). The
/// generic "Impl Type" property cannot be read from this provider; this one can.
const IMPL_TYPE: &str = "NgcKeyImplType";
const IMPL_HARDWARE: u32 = 1;
const ALLOW_DECRYPT: u32 = 1;
const PAD_PKCS1: u32 = 2;

/// The provider's name for a key of this user: `<SID>//se-env//<name>`
/// (`<SID>//<domain>/<subdomain>/<name>` with an empty subdomain).
fn full_name(name: &str) -> Result<Vec<u16>> {
    let sid = crate::process::UserSid::current()?.string()?;
    Ok(wide(&format!("{sid}//se-env//{name}")))
}

fn hr(r: i32) -> Result<()> {
    if r == 0 { Ok(()) } else { Err(r as Error) }
}

struct Handle(usize);
impl Drop for Handle {
    fn drop(&mut self) {
        if self.0 != 0 {
            unsafe { NCryptFreeObject(self.0) };
        }
    }
}

fn provider() -> Result<Handle> {
    let mut h = 0usize;
    hr(unsafe { NCryptOpenStorageProvider(&mut h, wide(PROVIDER).as_ptr(), 0) })?;
    Ok(Handle(h))
}

fn set_u32(h: usize, name: &str, v: u32) -> Result<()> {
    hr(unsafe { NCryptSetProperty(h, wide(name).as_ptr(), (&v as *const u32).cast(), 4, 0) })
}

fn get_u32(h: usize, name: &str) -> Result<u32> {
    let (mut v, mut n) = (0u32, 0u32);
    hr(unsafe {
        NCryptGetProperty(
            h,
            wide(name).as_ptr(),
            (&mut v as *mut u32).cast(),
            4,
            &mut n,
            0,
        )
    })?;
    Ok(v)
}

/// What the Hello dialog says, and the window it belongs to.
fn set_ui(h: usize, context: &str) {
    use windows_sys::Win32::UI::WindowsAndMessaging::GetForegroundWindow;
    let hwnd = unsafe { GetForegroundWindow() };
    if !hwnd.is_null() {
        let _ = hr(unsafe {
            NCryptSetProperty(
                h,
                wide(HWND_HANDLE).as_ptr(),
                (&hwnd as *const _ as *const u8).cast(),
                size_of_val(&hwnd) as u32,
                0,
            )
        });
    }
    if !context.is_empty() {
        let w = wide(context);
        let _ = hr(unsafe {
            NCryptSetProperty(
                h,
                wide(USE_CONTEXT).as_ptr(),
                w.as_ptr().cast(),
                (w.len() * 2) as u32,
                0,
            )
        });
    }
}

pub struct Key(Handle);

impl Key {
    /// Creates an RSA-2048 decryption key that needs the Hello gesture for
    /// every use. Asks for Hello.
    pub fn create(name: &str, context: &str) -> Result<Key> {
        let p = provider()?;
        let mut h = 0usize;
        let full = full_name(name)?;
        hr(unsafe {
            NCryptCreatePersistedKey(p.0, &mut h, wide("RSA").as_ptr(), full.as_ptr(), 0, 0)
        })?;
        let k = Key(Handle(h));
        set_u32(h, LENGTH, 2048)?;
        set_u32(h, KEY_USAGE, ALLOW_DECRYPT)?;
        set_u32(h, CACHE_TYPE, CACHE_AUTH_MANDATORY)?;
        set_ui(h, context);
        hr(unsafe { NCryptFinalizeKey(h, 0) })?;
        Ok(k)
    }

    /// Opens a key. Asks for nothing.
    pub fn open(name: &str) -> Result<Key> {
        let p = provider()?;
        let mut h = 0usize;
        let full = full_name(name)?;
        hr(unsafe { NCryptOpenKey(p.0, &mut h, full.as_ptr(), 0, 0) })?;
        Ok(Key(Handle(h)))
    }

    /// The public key as a BCRYPT_RSAPUBLIC_BLOB. Asks for nothing.
    pub fn public_blob(&self) -> Result<Vec<u8>> {
        let t = wide("RSAPUBLICBLOB");
        let mut n = 0u32;
        hr(unsafe {
            NCryptExportKey(
                self.0.0,
                0,
                t.as_ptr(),
                core::ptr::null(),
                core::ptr::null_mut(),
                0,
                &mut n,
                0,
            )
        })?;
        let mut b = vec![0u8; n as usize];
        hr(unsafe {
            NCryptExportKey(
                self.0.0,
                0,
                t.as_ptr(),
                core::ptr::null(),
                b.as_mut_ptr(),
                n,
                &mut n,
                0,
            )
        })?;
        b.truncate(n as usize);
        Ok(b)
    }

    /// Whether the key is what se-env requires: not exportable, every use
    /// needs the Hello gesture, and the key is not a software key. Asks for
    /// nothing.
    pub fn is_protected(&self) -> Result<bool> {
        Ok(self.export_policy()? == 0
            && self.cache_type()? == CACHE_AUTH_MANDATORY
            && self.in_hardware() != Some(false))
    }

    /// Whether the Hello container keeps this key in hardware (the TPM):
    /// `Some(false)` for a software key, `None` where this Windows does not
    /// say (then [`tpm20_present`] is all there is to go by). Asks for nothing.
    pub fn in_hardware(&self) -> Option<bool> {
        get_u32(self.0.0, IMPL_TYPE)
            .ok()
            .map(|v| v & IMPL_HARDWARE != 0)
    }

    pub fn export_policy(&self) -> Result<u32> {
        get_u32(self.0.0, EXPORT_POLICY)
    }

    pub fn cache_type(&self) -> Result<u32> {
        get_u32(self.0.0, CACHE_TYPE)
    }

    /// Decrypts (PKCS#1 v1.5) inside the provider. Always asks for Hello;
    /// `context` is shown in the dialog. The provider does not do OAEP.
    pub fn decrypt(&self, ct: &[u8], context: &str) -> Result<zeroize::Zeroizing<Vec<u8>>> {
        let h = self.0.0;
        set_u32(h, GESTURE_REQUIRED, 1)?;
        set_ui(h, context);
        let mut out = zeroize::Zeroizing::new(vec![0u8; 512]);
        let mut n = 0u32;
        hr(unsafe {
            NCryptDecrypt(
                h,
                ct.as_ptr(),
                ct.len() as u32,
                core::ptr::null(),
                out.as_mut_ptr(),
                out.len() as u32,
                &mut n,
                PAD_PKCS1,
            )
        })?;
        out.truncate(n as usize);
        Ok(out)
    }

    /// Deletes the key. Asks for nothing.
    pub fn delete(self) -> Result<()> {
        let h = self.0.0;
        core::mem::forget(self); // NCryptDeleteKey frees the handle
        hr(unsafe { NCryptDeleteKey(h, 0) })
    }
}

/// RSA encryption (PKCS#1 v1.5) to a BCRYPT_RSAPUBLIC_BLOB. Software; needs no key store.
pub fn encrypt(public_blob: &[u8], pt: &[u8]) -> Result<Vec<u8>> {
    let mut alg: BCRYPT_ALG_HANDLE = core::ptr::null_mut();
    hr(unsafe {
        BCryptOpenAlgorithmProvider(&mut alg, wide("RSA").as_ptr(), core::ptr::null(), 0)
    })?;
    let mut key: BCRYPT_KEY_HANDLE = core::ptr::null_mut();
    let r = unsafe {
        BCryptImportKeyPair(
            alg,
            core::ptr::null_mut(),
            wide("RSAPUBLICBLOB").as_ptr(),
            &mut key,
            public_blob.as_ptr(),
            public_blob.len() as u32,
            0,
        )
    };
    if r != 0 {
        unsafe { BCryptCloseAlgorithmProvider(alg, 0) };
        return Err(r as Error);
    }
    let mut out = vec![0u8; 1024];
    let mut n = 0u32;
    let r = unsafe {
        BCryptEncrypt(
            key,
            pt.as_ptr(),
            pt.len() as u32,
            core::ptr::null(),
            core::ptr::null_mut(),
            0,
            out.as_mut_ptr(),
            out.len() as u32,
            &mut n,
            PAD_PKCS1,
        )
    };
    unsafe {
        BCryptDestroyKey(key);
        BCryptCloseAlgorithmProvider(alg, 0);
    }
    hr(r)?;
    out.truncate(n as usize);
    Ok(out)
}

/// Whether this PC has a TPM 2.0. The Hello container uses it when there is one.
pub fn tpm20_present() -> bool {
    use windows_sys::Win32::System::TpmBaseServices::{TPM_DEVICE_INFO, Tbsi_GetDeviceInfo};
    let mut info: TPM_DEVICE_INFO = unsafe { core::mem::zeroed() };
    let r = unsafe {
        Tbsi_GetDeviceInfo(
            size_of::<TPM_DEVICE_INFO>() as u32,
            (&mut info as *mut TPM_DEVICE_INFO).cast(),
        )
    };
    r == 0 && info.tpmVersion == 2
}
