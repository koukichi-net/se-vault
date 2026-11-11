//! se_env_win.dll: the Windows API calls of se-env (cmd/se-env/win_windows.go
//! loads it by absolute path, without cgo). Each function returns a Win32
//! error code (0 is success) unless its comment says otherwise. Strings are
//! NUL-terminated UTF-16.
#![cfg(windows)]
#![allow(clippy::missing_safety_doc)]

use sewin::{from_wide_ptr, process};

/// Bumped whenever a signature below changes; win_windows.go checks it.
const ABI_VERSION: u32 = 2;

const ERROR_INVALID_PARAMETER: u32 = 87;
const ERROR_INSUFFICIENT_BUFFER: u32 = 122;

fn code(r: sewin::Result<()>) -> u32 {
    match r {
        Ok(()) => 0,
        Err(e) => e,
    }
}

#[unsafe(no_mangle)]
pub extern "system" fn sew_abi() -> u32 {
    ABI_VERSION
}

/// See sewin::process::harden_self. Call again now and then: threads started
/// since the last call are covered then.
#[unsafe(no_mangle)]
pub extern "system" fn sew_harden() -> u32 {
    code(process::harden_self())
}

/// 0: safe to trust. Otherwise why not: 1 owner, 2 no DACL, 3 someone else
/// may write, 4 an unexpected ACE; a negative Win32 error code on failure.
#[unsafe(no_mangle)]
pub unsafe extern "system" fn sew_path_check(path: *const u16) -> i32 {
    if path.is_null() {
        return -87;
    }
    match sewin::acl::path_check(&unsafe { from_wide_ptr(path) }) {
        Ok(c) => c as i32,
        Err(e) => -(e as i32),
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "system" fn sew_secure_dir(path: *const u16) -> u32 {
    if path.is_null() {
        return ERROR_INVALID_PARAMETER;
    }
    code(sewin::acl::secure_dir(&unsafe { from_wide_ptr(path) }))
}

#[unsafe(no_mangle)]
pub extern "system" fn sew_lock(handle: isize, exclusive: i32, wait: i32) -> u32 {
    // SAFETY: the handle is the Go *os.File's, open for the whole call.
    code(unsafe { sewin::lock::lock(handle as *mut core::ffi::c_void, exclusive != 0, wait != 0) })
}

/// Parent pid and creation time (FILETIME as u64) of a process.
#[unsafe(no_mangle)]
pub unsafe extern "system" fn sew_proc_info(pid: u32, ppid: *mut u32, created: *mut u64) -> u32 {
    if ppid.is_null() || created.is_null() {
        return ERROR_INVALID_PARAMETER;
    }
    match process::info(pid) {
        Ok((p, c)) => {
            unsafe {
                *ppid = p;
                *created = c;
            }
            0
        }
        Err(e) => e,
    }
}

unsafe fn put_string(s: &str, buf: *mut u16, cap: u32, len: *mut u32) -> u32 {
    if buf.is_null() || len.is_null() {
        return ERROR_INVALID_PARAMETER;
    }
    let w: Vec<u16> = s.encode_utf16().collect();
    if w.len() > cap as usize {
        return ERROR_INSUFFICIENT_BUFFER;
    }
    unsafe {
        core::ptr::copy_nonoverlapping(w.as_ptr(), buf, w.len());
        *len = w.len() as u32;
    }
    0
}

/// Full path of a process's executable, into buf (cap UTF-16 units; len is set).
#[unsafe(no_mangle)]
pub unsafe extern "system" fn sew_proc_image(
    pid: u32,
    buf: *mut u16,
    cap: u32,
    len: *mut u32,
) -> u32 {
    match process::image(pid) {
        Ok(s) => unsafe { put_string(&s, buf, cap, len) },
        Err(e) => e,
    }
}

/// 1: same user as this process, 0: another user, -1: error.
#[unsafe(no_mangle)]
pub extern "system" fn sew_same_user(pid: u32) -> i32 {
    match process::same_user(pid) {
        Ok(true) => 1,
        Ok(false) => 0,
        Err(_) => -1,
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "system" fn sew_peer_pid(sock: usize, pid: *mut u32) -> u32 {
    if pid.is_null() {
        return ERROR_INVALID_PARAMETER;
    }
    match sewin::sock::peer_pid(sock) {
        Ok(p) => {
            unsafe { *pid = p };
            0
        }
        Err(e) => e,
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "system" fn sew_job_create_join(job: *mut isize) -> u32 {
    if job.is_null() {
        return ERROR_INVALID_PARAMETER;
    }
    match sewin::job::create_join() {
        Ok(h) => {
            unsafe { *job = h as isize };
            0
        }
        Err(e) => e,
    }
}

/// 1: the process is in the job, 0: it is not, -1: error.
#[unsafe(no_mangle)]
pub extern "system" fn sew_job_contains(job: isize, pid: u32) -> i32 {
    // SAFETY: the agent passes the handle sew_job_create_join gave it and never closes it.
    match unsafe { sewin::job::contains(job as *mut core::ffi::c_void, pid) } {
        Ok(true) => 1,
        Ok(false) => 0,
        Err(_) => -1,
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "system" fn sew_ui_lang(buf: *mut u16, cap: u32, len: *mut u32) -> u32 {
    match sewin::user_lang() {
        Ok(s) => unsafe { put_string(&s, buf, cap, len) },
        Err(e) => e,
    }
}

/// Encrypts (1) or decrypts (0) len bytes in place; len is a multiple of 16.
/// See sewin::memory::protect.
#[unsafe(no_mangle)]
pub unsafe extern "system" fn sew_protect(buf: *mut u8, len: usize, encrypt: i32) -> u32 {
    if buf.is_null() {
        return ERROR_INVALID_PARAMETER;
    }
    code(unsafe { sewin::memory::protect(buf, len, encrypt != 0) })
}
