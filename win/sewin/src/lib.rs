//! The Windows API calls of se-env. se_env_win.dll (for the Go program),
//! age-plugin-winhello and se-env-ui all use this crate, so each call is
//! written once. Errors are Win32 error codes.
#![cfg(windows)]

pub mod acl;
pub mod job;
pub mod lock;
pub mod memory;
pub mod ngc;
pub mod process;
pub mod sock;
pub mod ui;

use windows_sys::Win32::Foundation::{CloseHandle, GetLastError, HANDLE, LocalFree};

/// A Win32 error code.
pub type Error = u32;
pub type Result<T> = std::result::Result<T, Error>;

pub(crate) fn last_error() -> Error {
    match unsafe { GetLastError() } {
        0 => 1627, // ERROR_FUNCTION_FAILED: never report success for a failure
        e => e,
    }
}

/// NUL-terminated UTF-16.
pub fn wide(s: &str) -> Vec<u16> {
    s.encode_utf16().chain(std::iter::once(0)).collect()
}

pub fn from_wide(s: &[u16]) -> String {
    let n = s.iter().position(|&c| c == 0).unwrap_or(s.len());
    String::from_utf16_lossy(&s[..n])
}

/// Reads a NUL-terminated UTF-16 string.
///
/// # Safety
/// `p` must point to a NUL-terminated string.
pub unsafe fn from_wide_ptr(p: *const u16) -> String {
    if p.is_null() {
        return String::new();
    }
    let mut n = 0;
    while unsafe { *p.add(n) } != 0 {
        n += 1;
    }
    String::from_utf16_lossy(unsafe { std::slice::from_raw_parts(p, n) })
}

/// Closes a handle when dropped.
pub(crate) struct Owned(pub HANDLE);

impl Drop for Owned {
    fn drop(&mut self) {
        if !self.0.is_null() {
            unsafe { CloseHandle(self.0) };
        }
    }
}

/// Frees memory from LocalAlloc (SDDL conversions, security info) when dropped.
pub(crate) struct Local(pub *mut core::ffi::c_void);

impl Drop for Local {
    fn drop(&mut self) {
        if !self.0.is_null() {
            unsafe { LocalFree(self.0) };
        }
    }
}

/// The user's locale ("ja-JP"), for the messages.
pub fn user_lang() -> Result<String> {
    use windows_sys::Win32::Globalization::GetUserDefaultLocaleName;
    let mut buf = [0u16; 85];
    let n = unsafe { GetUserDefaultLocaleName(buf.as_mut_ptr(), buf.len() as i32) };
    if n == 0 {
        return Err(last_error());
    }
    Ok(from_wide(&buf))
}
