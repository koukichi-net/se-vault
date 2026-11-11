//! Whole-file locks (LockFileEx), released when the handle is closed.

use crate::{Result, last_error};
use windows_sys::Win32::Foundation::HANDLE;
use windows_sys::Win32::Storage::FileSystem::{
    LOCKFILE_EXCLUSIVE_LOCK, LOCKFILE_FAIL_IMMEDIATELY, LockFileEx,
};
use windows_sys::Win32::System::IO::OVERLAPPED;

/// Locks the whole file. Without `wait`, a lock held elsewhere fails at once
/// with ERROR_LOCK_VIOLATION (33).
///
/// # Safety
/// `file` must be an open file handle.
pub unsafe fn lock(file: HANDLE, exclusive: bool, wait: bool) -> Result<()> {
    let mut flags = 0;
    if exclusive {
        flags |= LOCKFILE_EXCLUSIVE_LOCK;
    }
    if !wait {
        flags |= LOCKFILE_FAIL_IMMEDIATELY;
    }
    let mut ov: OVERLAPPED = unsafe { core::mem::zeroed() };
    if unsafe { LockFileEx(file, flags, 0, u32::MAX, u32::MAX, &mut ov) } == 0 {
        return Err(last_error());
    }
    Ok(())
}
