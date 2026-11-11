//! The job object that marks the processes of a session.

use crate::process::open_query;
use crate::{Result, last_error};
use windows_sys::Win32::Foundation::{CloseHandle, HANDLE};
use windows_sys::Win32::System::JobObjects::{
    AssignProcessToJobObject, CreateJobObjectW, IsProcessInJob,
};
use windows_sys::Win32::System::Threading::GetCurrentProcess;

/// Creates an unnamed job object and puts this process in it. Every process
/// started from here on is in the job and cannot leave it: the job does not
/// allow breakaway (JOB_OBJECT_LIMIT_BREAKAWAY_OK is not set). The handle is
/// kept until the process ends.
pub fn create_join() -> Result<HANDLE> {
    let job = unsafe { CreateJobObjectW(core::ptr::null(), core::ptr::null()) };
    if job.is_null() {
        return Err(last_error());
    }
    if unsafe { AssignProcessToJobObject(job, GetCurrentProcess()) } == 0 {
        let e = last_error();
        unsafe { CloseHandle(job) };
        return Err(e);
    }
    Ok(job)
}

/// Whether process `pid` is in the job.
///
/// # Safety
/// `job` must be a job object handle from [`create_join`].
pub unsafe fn contains(job: HANDLE, pid: u32) -> Result<bool> {
    let h = open_query(pid)?;
    let mut r = 0;
    if unsafe { IsProcessInJob(h.0, job, &mut r) } == 0 {
        return Err(last_error());
    }
    Ok(r != 0)
}
