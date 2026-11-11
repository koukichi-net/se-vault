//! Keeping secrets in memory unreadable while they are not in use.

use crate::{Result, last_error};
use windows_sys::Win32::Security::Cryptography::{
    CRYPTPROTECTMEMORY_BLOCK_SIZE, CRYPTPROTECTMEMORY_SAME_PROCESS, CryptProtectMemory,
    CryptUnprotectMemory,
};

/// Encrypts or decrypts a buffer in place (CryptProtectMemory) with a key
/// that only this process can use and that is not in its address space. The
/// length must be a multiple of 16. Whoever reads this process's memory, or
/// a dump of it, sees ciphertext; only code running in this process can undo it.
///
/// # Safety
/// `buf` must point to `len` writable bytes.
pub unsafe fn protect(buf: *mut u8, len: usize, encrypt: bool) -> Result<()> {
    if len == 0
        || !len.is_multiple_of(CRYPTPROTECTMEMORY_BLOCK_SIZE as usize)
        || len > u32::MAX as usize
    {
        return Err(87); // ERROR_INVALID_PARAMETER
    }
    let ok = unsafe {
        if encrypt {
            CryptProtectMemory(buf.cast(), len as u32, CRYPTPROTECTMEMORY_SAME_PROCESS)
        } else {
            CryptUnprotectMemory(buf.cast(), len as u32, CRYPTPROTECTMEMORY_SAME_PROCESS)
        }
    };
    if ok == 0 { Err(last_error()) } else { Ok(()) }
}
