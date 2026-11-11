package main

// macOS: the Secure Enclave through age-plugin-se, Unix permissions,
// flock, LOCAL_PEERCRED and sysctl, osascript dialogs, POSIX signals.
// The Windows counterparts are in sys_windows.go.

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	pluginName      = "age-plugin-se"
	pluginFile      = pluginName
	identityPrefix  = "AGE-PLUGIN-SE-"
	recipientPrefix = "age1se1"
	sockPathMax     = 103 // sun_path on macOS is 104 bytes including the NUL
)

// keygenArgs are the plugin arguments that create the key in identity.txt.
func keygenArgs(out string) []string {
	return []string{"keygen", "--access-control", "any-biometry-or-passcode", "-o", out}
}

func platformInit() error {
	syscall.Umask(0o077)
	// Secrets live in this process's memory: never write a core file.
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{})
	return nil
}

// platformPaths are the files besides se-env and the plugin that se-env
// trusts. On macOS there are none.
func (a *app) platformPaths() []string { return nil }

// platformEnv: PATH is narrowed to the plugin's directory (newApp), and
// macOS never looks for a program in the current directory by itself.
func (a *app) platformEnv() {}

// unsafePath reports whether a path se-env trusts could be replaced by
// another user: it must be owned by this user (or root) and must not be
// writable by group or others.
func unsafePath(p string, fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	uid := uint32(os.Getuid())
	return fi.Mode().Perm()&0o022 != 0 || (ok && st.Uid != uid && st.Uid != 0)
}

// secureDir creates a directory that only this user can use.
func secureDir(p string) error {
	if err := os.MkdirAll(p, 0o700); err != nil {
		return err
	}
	return os.Chmod(p, 0o700)
}

func isExecutable(fi os.FileInfo) bool { return !fi.IsDir() && fi.Mode()&0o111 != 0 }

// lockFile takes an advisory lock on f, released when f is closed.
func lockFile(f *os.File, exclusive bool) error {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	return syscall.Flock(int(f.Fd()), how)
}

// tryLockFile takes an exclusive lock without waiting.
func tryLockFile(f *os.File) bool {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil
}

// envKey is the form in which two variable names are the same variable.
func envKey(k string) string { return k }

// platformUnsafeNames adds to unsafeKeyNames. Nothing on macOS.
var platformUnsafeNames = map[string]bool{}

// lookPathIn finds an executable like exec.LookPath, but in the given PATH.
func lookPathIn(name, pathList string) (string, error) {
	if strings.Contains(name, "/") {
		if fi, err := os.Stat(name); err == nil && isExecutable(fi) {
			return name, nil
		}
		return "", exec.ErrNotFound
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && isExecutable(fi) {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

func defaultShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/zsh"
}

// runCommand replaces se-env with the command (exec). It returns only on error.
func runCommand(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}

// ---- processes ---------------------------------------------------------------

func systemLang() string { return "en" }

// peerOf tells whether the process that connected runs as this user, and its pid.
func peerOf(c *net.UnixConn) (sameUser bool, pid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return false, 0, err
	}
	var cerr error
	var uid uint32
	err = raw.Control(func(fd uintptr) {
		var cred *unix.Xucred
		if cred, cerr = unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED); cerr != nil {
			return
		}
		uid = cred.Uid
		pid, cerr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	})
	if err == nil {
		err = cerr
	}
	return err == nil && uid == uint32(os.Getuid()), pid, err
}

func procInfo(pid int) (ppid int, started time.Time, err error) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return 0, time.Time{}, err
	}
	if int(kp.Proc.P_pid) != pid {
		return 0, time.Time{}, errors.New("no such process")
	}
	st := kp.Proc.P_starttime
	return int(kp.Eproc.Ppid), time.Unix(int64(st.Sec), int64(st.Usec)*1000), nil
}

// procName returns a process's parent and a short name for it: the
// application bundle for a program inside one ("Ghostty"), otherwise the
// file name it was started as ("claude", not the version-named file a
// symlink points to). Falls back to the kernel's 16-byte command name.
func procName(pid int) (ppid int, name string, ok bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return 0, "", false
	}
	ppid = int(kp.Eproc.Ppid)
	comm := kp.Proc.P_comm[:]
	if i := bytes.IndexByte(comm, 0); i >= 0 {
		comm = comm[:i]
	}
	name = string(comm)
	if exe := execPath(pid); exe != "" {
		name = displayName(exe)
	}
	name = printable(name)
	if name == "" {
		name = "?"
	}
	return ppid, name, true
}

// execPath reads the path a process was started with (kern.procargs2:
// argc, then the path). Empty when it cannot be read, as for processes of
// other users.
func execPath(pid int) string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 5 {
		return ""
	}
	b = b[4:] // argc
	if i := bytes.IndexByte(b, 0); i > 0 {
		return string(b[:i])
	}
	return ""
}

// ---- sessions ----------------------------------------------------------------

// sessionSys is the platform part of an agent: the pid of the session's command.
type sessionSys struct{ root int }

// bindDirect: the socket is bound under a temporary name and published by
// serve() once the command's pid is known (isMember needs it).
const bindDirect = false

func (ag *agent) prepare() error { return nil }

func restrictSocket(p string) error { return os.Chmod(p, 0o600) }

func (ag *agent) setRoot(pid int) { ag.sys.root = pid }

// isMember decides whether the connecting process belongs to this session.
func (ag *agent) isMember(pid int, accepted time.Time) bool {
	_, started, err := procInfo(pid)
	if err != nil {
		return false
	}
	// The pid was read from the socket; a process that began after the
	// connection was accepted is a different process that reused the pid.
	if started.After(accepted) {
		return false
	}
	return descendsFrom(pid, ag.sys.root)
}

func isConnRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

// ---- signals -----------------------------------------------------------------

// On SIGINT/SIGTERM/SIGHUP se-env stops its own child processes (the
// age-plugin-se instance waiting for Touch ID) before exiting, so no orphaned
// Touch ID prompt is left on screen. Locks need no cleanup: the vault lock is
// released by the kernel and a session lock by the closed connection.
// `session` does not use this: it forwards signals to the command it runs.

func stopChildren() {
	_ = exec.Command("/usr/bin/pkill", "-TERM", "-P", strconv.Itoa(os.Getpid())).Run()
}

func installInterruptHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		s := <-sigs
		stopChildren()
		code := 1
		if sig, ok := s.(syscall.Signal); ok {
			code = 128 + int(sig)
		}
		os.Exit(code)
	}()
}

// ignoreInterrupts keeps destroy from being stopped half-way.
func ignoreInterrupts()  { signal.Ignore(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP) }
func restoreInterrupts() { signal.Reset(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP) }

// sessionSignals: Ctrl-C / Ctrl-\ are meant for the command (it gets them
// from the terminal); TERM/HUP are forwarded. Handlers, unlike SIG_IGN, are
// not inherited by exec. Call forward once the command has started.
type sessionSignals struct{ ch chan os.Signal }

func catchSessionSignals() *sessionSignals {
	s := &sessionSignals{ch: make(chan os.Signal, 4)}
	signal.Notify(s.ch, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	return s
}

func (s *sessionSignals) forward(p *os.Process) {
	go func() {
		for sig := range s.ch {
			if sig == syscall.SIGTERM || sig == syscall.SIGHUP {
				_ = p.Signal(sig)
			}
		}
	}()
}

func (s *sessionSignals) stop() { signal.Stop(s.ch) }

// childExit turns the command's end into se-env's exit status.
func childExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return exitCode(128 + int(ws.Signal()))
		}
		return exitCode(ee.ExitCode())
	}
	return err
}

// ---- dialogs -----------------------------------------------------------------

// Fixed path: the dialog returns a secret, so it is never looked up on PATH.
const osascript = "/usr/bin/osascript"

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// dialogSecret asks for a value in a dialog with a hidden answer.
func (a *app) dialogSecret(label string) (string, error) {
	script := fmt.Sprintf(`display dialog %s default answer "" with hidden answer with title "se-env"`,
		appleScriptString(a.t.S("dialog_value", "Label", label))) + "\n" + `text returned of result`
	out, err := exec.Command(osascript, "-e", script).Output()
	if err != nil {
		return "", a.fail("err_cancelled")
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// dialogConfirm asks yes/no in a dialog; Cancel is the default.
func (a *app) dialogConfirm(msg string) error {
	cancel, del := appleScriptString(a.t.S("button_cancel")), appleScriptString(a.t.S("button_delete"))
	script := fmt.Sprintf(`display dialog %s buttons {%s, %s} default button %s cancel button %s with icon caution with title "se-env"`,
		appleScriptString(msg), cancel, del, cancel, cancel)
	if err := exec.Command(osascript, "-e", script).Run(); err != nil {
		return a.fail("err_aborted")
	}
	return nil
}

// alertDialog shows a critical alert that stays on screen until the user
// dismisses it, even after se-env has exited: se-env is often run by another
// program that never shows its stderr. The dialog process holds a lock for
// the profile, so repeated failures do not stack up dialogs. A variable so
// the tests can replace it.
var alertDialog = func(a *app, profile, title, msg string) {
	f, err := os.OpenFile(a.alertFile(profile), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if !tryLockFile(f) {
		return // the alert for this profile is already on screen
	}
	script := fmt.Sprintf(`display alert %s message %s as critical buttons {%s} default button 1`,
		appleScriptString(title), appleScriptString(msg), appleScriptString(a.t.S("button_ok")))
	cmd := exec.Command(osascript, "-e", script)
	cmd.ExtraFiles = []*os.File{f} // the lock lives as long as the dialog
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
}

func (ag *agent) release() {}

// platformLocales: no message differs from locales/<lang>.toml on macOS.
var platformLocales []string

func platformText(s string) string { return s }

func (a *app) platformReady() error { return nil }

// deleteKeyLine: a Secure Enclave key exists only as its identity line,
// which goes with the file.
func (a *app) deleteKeyLine(string) {}

// sealSecret / openSecret: how the agent keeps a decrypted profile between
// requests. macOS keeps other processes out of the agent's memory itself
// (hardened runtime, SIP), so it is kept as it is.
func sealSecret(plain []byte) ([]byte, bool)  { return append([]byte(nil), plain...), true }
func openSecret(sealed []byte) ([]byte, bool) { return append([]byte(nil), sealed...), true }

func platformTick() {}
