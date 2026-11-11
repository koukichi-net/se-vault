package main

// se-env does not call the Windows API itself. Everything that needs it is
// in se_env_win.dll (Rust, win/se-env-win), loaded from the directory of the
// se-env executable by its absolute path, without cgo. The calls return a
// Win32 error code (0 is success) unless noted otherwise.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

const (
	dllFile = "se_env_win.dll"
	dllABI  = 2 // must match se-env-win's ABI_VERSION
)

// winDLLDir overrides where the DLL is loaded from. Only the tests set it.
var winDLLDir string

var (
	winOnce sync.Once
	winErr  error
	winProc = map[string]*syscall.Proc{}
)

func dllDir() string {
	if winDLLDir != "" {
		return winDLLDir
	}
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if self, err := filepath.EvalSymlinks(exe); err == nil {
		exe = self
	}
	return filepath.Dir(exe)
}

// loadWin loads the DLL once. Every function below fails while it is missing.
func loadWin() error {
	winOnce.Do(func() {
		dir := dllDir()
		if dir == "" || !filepath.IsAbs(dir) {
			winErr = errors.New(dllFile + " not found")
			return
		}
		d, err := syscall.LoadDLL(filepath.Join(dir, dllFile))
		if err != nil {
			winErr = fmt.Errorf("%s: %w", filepath.Join(dir, dllFile), err)
			return
		}
		for _, name := range []string{"sew_abi", "sew_harden", "sew_path_check", "sew_secure_dir",
			"sew_lock", "sew_proc_info", "sew_proc_image", "sew_same_user", "sew_peer_pid",
			"sew_job_create_join", "sew_job_contains", "sew_ui_lang", "sew_protect"} {
			p, err := d.FindProc(name)
			if err != nil {
				winErr = fmt.Errorf("%s: %w", dllFile, err)
				return
			}
			winProc[name] = p
		}
		if abi, _, _ := winProc["sew_abi"].Call(); abi != dllABI {
			winErr = fmt.Errorf("%s: ABI %d, se-env needs %d", dllFile, abi, dllABI)
		}
	})
	return winErr
}

func winCall(name string, args ...uintptr) uintptr {
	r, _, _ := winProc[name].Call(args...)
	return r
}

func winErrno(r uintptr) error {
	if r == 0 {
		return nil
	}
	return syscall.Errno(r)
}

func utf16Ptr(s string) (*uint16, error) { return syscall.UTF16PtrFromString(s) }

// winHarden restricts who may open this process and its threads (the same
// user can no longer read its memory, duplicate its handles, hijack a thread
// or change that again) and sets mitigation policies: forced ASLR, no DLL
// injection through extension points, only Microsoft-signed DLLs from now
// on, no dynamic code. Calling it again covers threads started since.
func winHarden() error {
	if err := loadWin(); err != nil {
		return err
	}
	return winErrno(winCall("sew_harden"))
}

// winPathSafe: owned by this user, SYSTEM or Administrators, and nobody else
// may write to it.
func winPathSafe(p string) bool {
	if loadWin() != nil {
		return false
	}
	u, err := utf16Ptr(p)
	if err != nil {
		return false
	}
	// 0 is safe; 1-4 say why not (owner, no DACL, another writer, unknown
	// entry); negative is an error.
	return int32(winCall("sew_path_check", uintptr(unsafe.Pointer(u)))) == 0
}

// winSecureDir gives a directory a protected ACL: this user, SYSTEM and
// Administrators only, inherited by everything inside.
func winSecureDir(p string) error {
	if err := loadWin(); err != nil {
		return err
	}
	u, err := utf16Ptr(p)
	if err != nil {
		return err
	}
	return winErrno(winCall("sew_secure_dir", uintptr(unsafe.Pointer(u))))
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

const errLockViolation = 33 // ERROR_LOCK_VIOLATION

// winLock locks a whole file (LockFileEx). The lock ends when the handle closes.
func winLock(f *os.File, exclusive, wait bool) error {
	if err := loadWin(); err != nil {
		return err
	}
	return winErrno(winCall("sew_lock", f.Fd(), boolArg(exclusive), boolArg(wait)))
}

// fileTimeToTime converts 100 ns intervals since 1601 to a time.
func fileTimeToTime(ft uint64) time.Time {
	const epochDiff = 116444736000000000
	return time.Unix(0, int64(ft-epochDiff)*100)
}

func winProcInfo(pid int) (ppid int, created time.Time, err error) {
	if err := loadWin(); err != nil {
		return 0, time.Time{}, err
	}
	var pp uint32
	var ft uint64
	if err := winErrno(winCall("sew_proc_info", uintptr(pid),
		uintptr(unsafe.Pointer(&pp)), uintptr(unsafe.Pointer(&ft)))); err != nil {
		return 0, time.Time{}, err
	}
	return int(pp), fileTimeToTime(ft), nil
}

func winString(name string, args ...uintptr) (string, error) {
	if err := loadWin(); err != nil {
		return "", err
	}
	buf := make([]uint16, 1024)
	var n uint32
	args = append(args, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&n)))
	if err := winErrno(winCall(name, args...)); err != nil {
		return "", err
	}
	return syscall.UTF16ToString(buf[:n]), nil
}

// winProcImage is the full path of a process's executable.
func winProcImage(pid int) (string, error) { return winString("sew_proc_image", uintptr(pid)) }

func winUILang() string {
	s, _ := winString("sew_ui_lang")
	return s
}

// winSameUser: the process runs as the same user (token user SID) as this one.
func winSameUser(pid int) bool {
	if loadWin() != nil {
		return false
	}
	return int32(winCall("sew_same_user", uintptr(pid))) == 1
}

// winPeerPid asks an AF_UNIX socket for the pid at the other end
// (SIO_AF_UNIX_GETPEERPID).
func winPeerPid(sock uintptr) (int, error) {
	if err := loadWin(); err != nil {
		return 0, err
	}
	var pid uint32
	if err := winErrno(winCall("sew_peer_pid", sock, uintptr(unsafe.Pointer(&pid)))); err != nil {
		return 0, err
	}
	return int(pid), nil
}

// winJobCreateJoin creates a job object without breakaway and puts this
// process in it. Every process started from here on is in it too.
func winJobCreateJoin() (uintptr, error) {
	if err := loadWin(); err != nil {
		return 0, err
	}
	var job uintptr
	if err := winErrno(winCall("sew_job_create_join", uintptr(unsafe.Pointer(&job)))); err != nil {
		return 0, err
	}
	return job, nil
}

func winJobContains(job uintptr, pid int) bool {
	if loadWin() != nil {
		return false
	}
	return int32(winCall("sew_job_contains", job, uintptr(pid))) == 1
}

// winProtect encrypts or decrypts b in place (CryptProtectMemory, usable by
// this process only). len(b) must be a multiple of 16.
func winProtect(b []byte, encrypt bool) error {
	if err := loadWin(); err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return winErrno(winCall("sew_protect", uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), boolArg(encrypt)))
}
