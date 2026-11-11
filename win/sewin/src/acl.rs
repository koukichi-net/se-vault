//! File and directory ACLs: who may replace what se-env trusts.

use crate::process::UserSid;
use crate::{Error, Local, Result, last_error, wide};
use core::ffi::c_void;
use windows_sys::Win32::Security::Authorization::{
    ConvertStringSecurityDescriptorToSecurityDescriptorW, ConvertStringSidToSidW,
    GetNamedSecurityInfoW, SDDL_REVISION_1, SE_FILE_OBJECT, SetNamedSecurityInfoW,
};
use windows_sys::Win32::Security::{
    ACCESS_ALLOWED_ACE, ACE_HEADER, ACL, ACL_SIZE_INFORMATION, AclSizeInformation,
    DACL_SECURITY_INFORMATION, GetAce, GetAclInformation, GetSecurityDescriptorDacl,
    OWNER_SECURITY_INFORMATION, PROTECTED_DACL_SECURITY_INFORMATION,
};

/// Rights that let someone change or replace a file, or what a directory holds.
const WRITE_RIGHTS: u32 = 0x0002 // FILE_WRITE_DATA / FILE_ADD_FILE
    | 0x0004 // FILE_APPEND_DATA / FILE_ADD_SUBDIRECTORY
    | 0x0010 // FILE_WRITE_EA
    | 0x0040 // FILE_DELETE_CHILD
    | 0x0100 // FILE_WRITE_ATTRIBUTES
    | 0x0001_0000 // DELETE
    | 0x0004_0000 // WRITE_DAC
    | 0x0008_0000 // WRITE_OWNER
    | 0x1000_0000 // GENERIC_ALL
    | 0x4000_0000; // GENERIC_WRITE

const ACCESS_ALLOWED: u8 = 0;
const ACCESS_ALLOWED_CALLBACK: u8 = 9;
const ACCESS_DENIED: u8 = 1;
const ACCESS_DENIED_CALLBACK: u8 = 10;
const SYSTEM_MANDATORY_LABEL: u8 = 0x11;

/// SIDs that may own or write to what se-env trusts besides the user.
const TRUSTED: &[&str] = &[
    "S-1-5-18",     // SYSTEM
    "S-1-5-32-544", // Administrators
    "S-1-3-0", // CREATOR OWNER: whoever creates the child, who must already be able to write here
    // TrustedInstaller
    "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464",
];

struct Sid(Local);

impl Sid {
    fn parse(s: &str) -> Result<Sid> {
        let w = wide(s);
        let mut p: *mut c_void = core::ptr::null_mut();
        if unsafe { ConvertStringSidToSidW(w.as_ptr(), &mut p) } == 0 {
            return Err(last_error());
        }
        Ok(Sid(Local(p)))
    }
}

struct Trusted {
    user: UserSid,
    others: Vec<Sid>,
}

impl Trusted {
    fn new() -> Result<Self> {
        let others = TRUSTED
            .iter()
            .map(|s| Sid::parse(s))
            .collect::<Result<Vec<_>>>()?;
        Ok(Trusted {
            user: UserSid::current()?,
            others,
        })
    }

    fn contains(&self, sid: *mut c_void) -> bool {
        use windows_sys::Win32::Security::EqualSid;
        (unsafe { self.user.equals(sid) })
            || self
                .others
                .iter()
                .any(|o| unsafe { EqualSid(o.0.0, sid) } != 0)
    }
}

/// Whether a file or directory is safe to trust: owned by this user,
/// SYSTEM, Administrators or TrustedInstaller, and nobody else is allowed
/// to write to it (inherited and inheritable entries included).
pub fn path_safe(path: &str) -> Result<bool> {
    path_check(path).map(|r| r == Check::Safe)
}

/// Why a path is or is not safe.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Check {
    Safe = 0,
    Owner = 1,
    NoDacl = 2,
    Writer = 3,
    UnknownAce = 4,
}

pub fn path_check(path: &str) -> Result<Check> {
    let trusted = Trusted::new()?;
    let w = wide(path);
    let mut owner: *mut c_void = core::ptr::null_mut();
    let mut dacl: *mut ACL = core::ptr::null_mut();
    let mut sd: *mut c_void = core::ptr::null_mut();
    let r = unsafe {
        GetNamedSecurityInfoW(
            w.as_ptr(),
            SE_FILE_OBJECT,
            OWNER_SECURITY_INFORMATION | DACL_SECURITY_INFORMATION,
            &mut owner,
            core::ptr::null_mut(),
            &mut dacl,
            core::ptr::null_mut(),
            &mut sd,
        )
    };
    if r != 0 {
        return Err(r as Error);
    }
    let _free = Local(sd);
    if !trusted.contains(owner) {
        return Ok(Check::Owner);
    }
    if dacl.is_null() {
        return Ok(Check::NoDacl); // no DACL: everyone has full access
    }
    let mut info: ACL_SIZE_INFORMATION = unsafe { core::mem::zeroed() };
    if unsafe {
        GetAclInformation(
            dacl,
            (&mut info as *mut ACL_SIZE_INFORMATION).cast(),
            size_of_val(&info) as u32,
            AclSizeInformation,
        )
    } == 0
    {
        return Err(last_error());
    }
    for i in 0..info.AceCount {
        let mut ace: *mut c_void = core::ptr::null_mut();
        if unsafe { GetAce(dacl, i, &mut ace) } == 0 {
            return Err(last_error());
        }
        let header = unsafe { &*(ace as *const ACE_HEADER) };
        match header.AceType {
            ACCESS_ALLOWED | ACCESS_ALLOWED_CALLBACK => {
                let a = unsafe { &*(ace as *const ACCESS_ALLOWED_ACE) };
                let sid = (&a.SidStart as *const u32).cast_mut().cast::<c_void>();
                if a.Mask & WRITE_RIGHTS != 0 && !trusted.contains(sid) {
                    return Ok(Check::Writer);
                }
            }
            ACCESS_DENIED | ACCESS_DENIED_CALLBACK | SYSTEM_MANDATORY_LABEL => {}
            _ => return Ok(Check::UnknownAce), // object ACEs and the like: not expected on these files
        }
    }
    Ok(Check::Safe)
}

/// Gives a directory a protected DACL: full control for this user, SYSTEM
/// and Administrators, inherited by everything in it; nobody else.
pub fn secure_dir(path: &str) -> Result<()> {
    let user = UserSid::current()?.string()?;
    let sddl = wide(&format!(
        "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)(A;OICI;FA;;;{user})"
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
    let _free = Local(sd);
    let (mut present, mut defaulted) = (0, 0);
    let mut dacl: *mut ACL = core::ptr::null_mut();
    if unsafe { GetSecurityDescriptorDacl(sd, &mut present, &mut dacl, &mut defaulted) } == 0 {
        return Err(last_error());
    }
    let w = wide(path);
    let r = unsafe {
        SetNamedSecurityInfoW(
            w.as_ptr(),
            SE_FILE_OBJECT,
            DACL_SECURITY_INFORMATION | PROTECTED_DACL_SECURITY_INFORMATION,
            core::ptr::null_mut(),
            core::ptr::null_mut(),
            dacl,
            core::ptr::null(),
        )
    };
    if r != 0 {
        return Err(r as Error);
    }
    Ok(())
}
