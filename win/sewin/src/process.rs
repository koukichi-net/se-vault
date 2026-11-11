//! Processes: who they run as, their parent and start time, their
//! executable, and hardening this process against the same user.

use crate::{Error, Local, Owned, Result, from_wide, last_error, wide};
use core::ffi::c_void;
use windows_sys::Win32::Foundation::{ERROR_NOT_FOUND, FILETIME, HANDLE, INVALID_HANDLE_VALUE};
use windows_sys::Win32::Security::Authorization::{
    ConvertSidToStringSidW, ConvertStringSecurityDescriptorToSecurityDescriptorW, SDDL_REVISION_1,
    SE_KERNEL_OBJECT, SetSecurityInfo,
};
use windows_sys::Win32::Security::{
    ACL, DACL_SECURITY_INFORMATION, EqualSid, GetSecurityDescriptorDacl, GetTokenInformation,
    PROTECTED_DACL_SECURITY_INFORMATION, TOKEN_QUERY, TOKEN_USER, TokenUser,
};
use windows_sys::Win32::System::Diagnostics::ToolHelp::{
    CreateToolhelp32Snapshot, PROCESSENTRY32W, Process32FirstW, Process32NextW, TH32CS_SNAPPROCESS,
};
use windows_sys::Win32::System::Threading::{
    GetCurrentProcess, GetCurrentProcessId, GetProcessTimes, OpenProcess, OpenProcessToken,
    PROCESS_NAME_WIN32, PROCESS_QUERY_LIMITED_INFORMATION, QueryFullProcessImageNameW,
};

/// Opens a process for queries only. `PROCESS_QUERY_LIMITED_INFORMATION` is
/// what a hardened process still grants (see [`harden_self`]).
pub(crate) fn open_query(pid: u32) -> Result<Owned> {
    let h = unsafe { OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, 0, pid) };
    if h.is_null() {
        return Err(last_error());
    }
    Ok(Owned(h))
}

/// TOKEN_USER of a process. The SID pointer inside points into the same
/// buffer, so the buffer is kept as it is (a u64 buffer keeps it aligned).
fn token_user(process: HANDLE) -> Result<Vec<u64>> {
    let mut token: HANDLE = core::ptr::null_mut();
    if unsafe { OpenProcessToken(process, TOKEN_QUERY, &mut token) } == 0 {
        return Err(last_error());
    }
    let token = Owned(token);
    let mut len = 0u32;
    unsafe { GetTokenInformation(token.0, TokenUser, core::ptr::null_mut(), 0, &mut len) };
    if len == 0 {
        return Err(last_error());
    }
    let mut buf = vec![0u64; (len as usize).div_ceil(8)];
    if unsafe { GetTokenInformation(token.0, TokenUser, buf.as_mut_ptr().cast(), len, &mut len) }
        == 0
    {
        return Err(last_error());
    }
    Ok(buf)
}

/// The SID of a process's user (its TOKEN_USER buffer).
pub struct UserSid(Vec<u64>);

impl UserSid {
    pub fn current() -> Result<Self> {
        Self::of(unsafe { GetCurrentProcess() })
    }

    fn of(process: HANDLE) -> Result<Self> {
        Ok(UserSid(token_user(process)?))
    }

    pub fn psid(&self) -> *mut c_void {
        unsafe { (*(self.0.as_ptr() as *const TOKEN_USER)).User.Sid }
    }

    /// # Safety
    /// `sid` must be null or point to a valid SID.
    pub unsafe fn equals(&self, sid: *mut c_void) -> bool {
        !sid.is_null() && unsafe { EqualSid(self.psid(), sid) } != 0
    }

    /// "S-1-5-21-…"
    pub fn string(&self) -> Result<String> {
        let mut p: *mut u16 = core::ptr::null_mut();
        if unsafe { ConvertSidToStringSidW(self.psid(), &mut p) } == 0 {
            return Err(last_error());
        }
        let _free = Local(p.cast());
        Ok(unsafe { crate::from_wide_ptr(p) })
    }
}

/// Whether process `pid` runs as the same user as this one.
pub fn same_user(pid: u32) -> Result<bool> {
    let me = UserSid::current()?;
    let h = open_query(pid)?;
    let other = UserSid::of(h.0)?;
    Ok(unsafe { me.equals(other.psid()) })
}

fn filetime_u64(ft: &FILETIME) -> u64 {
    ((ft.dwHighDateTime as u64) << 32) | ft.dwLowDateTime as u64
}

/// The parent's pid and the creation time (100 ns since 1601) of a process.
/// The parent pid is the one recorded when the process started; that
/// process may have exited and its pid been reused since (compare start times).
pub fn info(pid: u32) -> Result<(u32, u64)> {
    let snap = unsafe { CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0) };
    if snap == INVALID_HANDLE_VALUE {
        return Err(last_error());
    }
    let snap = Owned(snap);
    let mut e: PROCESSENTRY32W = unsafe { core::mem::zeroed() };
    e.dwSize = core::mem::size_of::<PROCESSENTRY32W>() as u32;
    let mut ppid = None;
    let mut ok = unsafe { Process32FirstW(snap.0, &mut e) };
    while ok != 0 {
        if e.th32ProcessID == pid {
            ppid = Some(e.th32ParentProcessID);
            break;
        }
        ok = unsafe { Process32NextW(snap.0, &mut e) };
    }
    let ppid = ppid.ok_or(ERROR_NOT_FOUND)?;
    let h = open_query(pid)?;
    let zero = FILETIME {
        dwLowDateTime: 0,
        dwHighDateTime: 0,
    };
    let (mut c, mut x, mut k, mut u) = (zero, zero, zero, zero);
    if unsafe { GetProcessTimes(h.0, &mut c, &mut x, &mut k, &mut u) } == 0 {
        return Err(last_error());
    }
    Ok((ppid, filetime_u64(&c)))
}

/// The full path of a process's executable.
pub fn image(pid: u32) -> Result<String> {
    let h = open_query(pid)?;
    let mut buf = vec![0u16; 32768];
    let mut n = buf.len() as u32;
    if unsafe { QueryFullProcessImageNameW(h.0, PROCESS_NAME_WIN32, buf.as_mut_ptr(), &mut n) } == 0
    {
        return Err(last_error());
    }
    Ok(from_wide(&buf[..n as usize]))
}

pub fn current_pid() -> u32 {
    unsafe { GetCurrentProcessId() }
}

/// The parent of this process, if it is still the process that started us
/// (it began before we did).
pub fn parent() -> Option<(u32, String)> {
    let (ppid, me_started) = info(current_pid()).ok()?;
    let (_, parent_started) = info(ppid).ok()?;
    if parent_started > me_started {
        return None; // the pid was reused by a later process
    }
    Some((ppid, image(ppid).ok()?))
}

/// What other processes may do to this one: query basic information, wait
/// for it and end it. Nothing else, for anyone but SYSTEM: not read or
/// write its memory, not duplicate its handles, not start threads in it,
/// not create processes with it as their parent. The OWNER RIGHTS entry
/// takes away the owner's implicit right to change this list again.
const PROCESS_RIGHTS: &str = "0x101001"; // SYNCHRONIZE | PROCESS_QUERY_LIMITED_INFORMATION | PROCESS_TERMINATE

/// The same for a thread: wait and query only. Not suspend it, not read or
/// set its registers (which would run code in this process).
const THREAD_RIGHTS: &str = "0x100800"; // SYNCHRONIZE | THREAD_QUERY_LIMITED_INFORMATION

/// A DACL that grants `rights` to the owner, this user and Administrators,
/// and everything to SYSTEM.
struct Dacl {
    _sd: Local,
    acl: *mut ACL,
}

impl Dacl {
    fn new(rights: &str) -> Result<Dacl> {
        let user = UserSid::current()?.string()?;
        let sddl = wide(&format!(
            "D:P(A;;{rights};;;OW)(A;;{rights};;;{user})(A;;{rights};;;BA)(A;;GA;;;SY)"
        ));
        let mut sd: *mut c_void = core::ptr::null_mut();
        if unsafe {
            ConvertStringSecurityDescriptorToSecurityDescriptorW(
                sddl.as_ptr(),
                SDDL_REVISION_1,
                &mut sd,
                core::ptr::null_mut(),
            )
        } == 0
        {
            return Err(last_error());
        }
        let sd = Local(sd);
        let (mut present, mut defaulted) = (0, 0);
        let mut acl: *mut ACL = core::ptr::null_mut();
        if unsafe { GetSecurityDescriptorDacl(sd.0, &mut present, &mut acl, &mut defaulted) } == 0 {
            return Err(last_error());
        }
        Ok(Dacl { _sd: sd, acl })
    }

    fn apply(&self, object: HANDLE) -> Result<()> {
        let r = unsafe {
            SetSecurityInfo(
                object,
                SE_KERNEL_OBJECT,
                DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
                core::ptr::null_mut(),
                core::ptr::null_mut(),
                self.acl,
                core::ptr::null(),
            )
        };
        if r != 0 { Err(r as Error) } else { Ok(()) }
    }
}

/// Restricts the DACL of every thread this process has now (see
/// [`THREAD_RIGHTS`]). Threads started later have the default DACL until
/// this is called again, so long-running programs call it from time to time.
pub fn harden_threads() -> Result<()> {
    use windows_sys::Win32::System::Diagnostics::ToolHelp::{
        TH32CS_SNAPTHREAD, THREADENTRY32, Thread32First, Thread32Next,
    };
    use windows_sys::Win32::System::Threading::OpenThread;
    const WRITE_DAC: u32 = 0x0004_0000;
    let dacl = Dacl::new(THREAD_RIGHTS)?;
    let snap = unsafe { CreateToolhelp32Snapshot(TH32CS_SNAPTHREAD, 0) };
    if snap == INVALID_HANDLE_VALUE {
        return Err(last_error());
    }
    let snap = Owned(snap);
    let me = current_pid();
    let mut e: THREADENTRY32 = unsafe { core::mem::zeroed() };
    e.dwSize = core::mem::size_of::<THREADENTRY32>() as u32;
    let mut ok = unsafe { Thread32First(snap.0, &mut e) };
    while ok != 0 {
        if e.th32OwnerProcessID == me {
            // Fails for a thread that is already restricted: nothing to do then.
            let h = unsafe { OpenThread(WRITE_DAC, 0, e.th32ThreadID) };
            if !h.is_null() {
                let h = Owned(h);
                let _ = dacl.apply(h.0);
            }
        }
        ok = unsafe { Thread32Next(snap.0, &mut e) };
    }
    Ok(())
}

/// Process mitigation policies that keep foreign code out and addresses
/// unpredictable. Each is best effort: one that this Windows refuses does
/// not stop the others. They are not inherited by child processes.
fn set_mitigations() {
    use windows_sys::Win32::System::Threading::{
        ProcessASLRPolicy, ProcessDynamicCodePolicy, ProcessExtensionPointDisablePolicy,
        ProcessImageLoadPolicy, ProcessSignaturePolicy, SetProcessMitigationPolicy,
    };
    let set = |policy, flags: u32| unsafe {
        SetProcessMitigationPolicy(policy, (&flags as *const u32).cast(), 4) != 0
    };
    // ASLR: bottom-up randomization and 64-bit entropy (already on: the
    // programs are built as relocatable 64-bit images), and on top of that
    // relocate every image even if it was not built for it and refuse
    // images that cannot be relocated. All four flags must be given
    // together: Windows refuses a request that leaves out one already on.
    let _ = set(ProcessASLRPolicy, 0x1 | 0x2 | 0x4 | 0x8);
    // No AppInit DLLs, window hooks or other legacy ways to load a DLL into
    // this process.
    let _ = set(ProcessExtensionPointDisablePolicy, 0x1);
    // No DLLs from network shares; prefer System32 over the program's directory.
    let _ = set(ProcessImageLoadPolicy, 0x1 | 0x4);
    // No new executable memory: nothing here generates code.
    let _ = set(ProcessDynamicCodePolicy, 0x1);
    // From now on only DLLs signed by Microsoft can be loaded. The DLLs of
    // se-env itself are loaded before this.
    let _ = set(ProcessSignaturePolicy, 0x1);
}

/// Hardens this process against other processes of the same user:
///   - the process and its threads cannot be opened for anything but a
///     query, a wait or termination ([`PROCESS_RIGHTS`], [`THREAD_RIGHTS`]),
///   - mitigation policies ([`set_mitigations`]),
///   - the heap is kept out of Windows Error Reporting dumps.
///
/// Calling it again only re-applies the thread DACLs (new threads). An
/// elevated process with SeDebugPrivilege is not stopped by this.
pub fn harden_self() -> Result<()> {
    use std::sync::Once;
    static ONCE: Once = Once::new();
    let mut first = Ok(());
    ONCE.call_once(|| {
        first = Dacl::new(PROCESS_RIGHTS).and_then(|d| d.apply(unsafe { GetCurrentProcess() }));
        set_mitigations();
        unsafe {
            use windows_sys::Win32::System::ErrorReporting::{
                WER_FAULT_REPORTING_FLAG_NOHEAP, WerSetFlags,
            };
            WerSetFlags(WER_FAULT_REPORTING_FLAG_NOHEAP);
        }
    });
    first?;
    harden_threads()
}

#[cfg(test)]
mod tests {
    use super::*;
    use windows_sys::Win32::System::Threading::{
        GetProcessMitigationPolicy, ProcessASLRPolicy, ProcessDynamicCodePolicy,
        ProcessExtensionPointDisablePolicy, ProcessImageLoadPolicy, ProcessSignaturePolicy,
    };

    fn policy(p: i32) -> u32 {
        let mut flags = 0u32;
        let ok = unsafe {
            GetProcessMitigationPolicy(GetCurrentProcess(), p, (&mut flags as *mut u32).cast(), 4)
        };
        assert!(ok != 0, "GetProcessMitigationPolicy({p})");
        flags
    }

    #[test]
    fn hardening_sets_the_policies() {
        harden_self().unwrap();
        harden_self().unwrap(); // again: only the threads
        assert_eq!(
            policy(ProcessASLRPolicy) & 0xf,
            0xf,
            "bottom-up, forced relocation, high entropy, no stripped images"
        );
        assert!(
            policy(ProcessExtensionPointDisablePolicy) & 0x1 != 0,
            "extension points"
        );
        assert!(policy(ProcessImageLoadPolicy) & 0x1 != 0, "remote images");
        assert!(policy(ProcessDynamicCodePolicy) & 0x1 != 0, "dynamic code");
        assert!(
            policy(ProcessSignaturePolicy) & 0x1 != 0,
            "Microsoft-signed only"
        );
        // The process still works: it can see itself and its parent.
        assert!(info(current_pid()).is_ok());
    }
}
