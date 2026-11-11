//! AF_UNIX sockets: the pid at the other end.

use crate::Result;
use windows_sys::Win32::Networking::WinSock::{SOCKET, WSAGetLastError, WSAIoctl};

/// `_WSAIOR(IOC_VENDOR, 256)` from afunix.h: IOC_OUT | IOC_VENDOR | 256 (no size bits, unlike BSD _IOR).
const SIO_AF_UNIX_GETPEERPID: u32 = 0x5800_0100;

/// The pid of the process at the other end of a connected AF_UNIX socket.
pub fn peer_pid(sock: usize) -> Result<u32> {
    let mut pid = 0u32;
    let mut n = 0u32;
    let r = unsafe {
        WSAIoctl(
            sock as SOCKET,
            SIO_AF_UNIX_GETPEERPID,
            core::ptr::null(),
            0,
            (&mut pid as *mut u32).cast(),
            4,
            &mut n,
            core::ptr::null_mut(),
            None,
        )
    };
    if r != 0 {
        return Err(unsafe { WSAGetLastError() } as u32);
    }
    Ok(pid)
}
