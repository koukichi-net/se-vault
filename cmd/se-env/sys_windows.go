package main

// Windows: Windows Hello through age-plugin-winhello, ACLs, LockFileEx,
// AF_UNIX peer pids and a job object for sessions, dialogs from se-env-ui.
// The Windows API calls themselves are in se_env_win.dll (win_windows.go).
// See docs/windows.md.

import (
	"encoding/binary"
	"errors"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

const (
	pluginName      = "age-plugin-winhello"
	pluginFile      = pluginName + ".exe"
	identityPrefix  = "AGE-PLUGIN-WINHELLO-"
	recipientPrefix = "age1winhello1"
	sockPathMax     = 107 // sun_path in afunix.h is 108 bytes including the NUL
	uiFile          = "se-env-ui.exe"
)

func keygenArgs(out string) []string { return []string{"keygen", "-o", out} }

// hardenErr is why this process could not be hardened when it started (nil:
// it was, or this is a test, which never calls platformInit). Nothing that
// touches the vault runs then (platformReady).
var hardenErr error

// platformInit hardens the process before any secret is read. Without the
// DLL, or when hardening fails, only commands that need no secret work:
// requireInit refuses (platformReady).
func platformInit() error {
	if hardenErr = winHarden(); hardenErr == nil {
		go keepThreadsHardened()
	}
	return nil
}

// keepThreadsHardened re-applies the thread ACLs for as long as the process
// runs. A thread that starts later has the default ACL until then, and the
// Go runtime starts threads whenever it needs one: while a command started
// by exec runs (the values are in this process until it ends), while the
// session agent serves. So: at once when the runtime has started a thread,
// and every two seconds for threads Windows itself starts (thread pool,
// Ctrl-C). The first check costs no system call.
func keepThreadsHardened() {
	const poll, full = 50 * time.Millisecond, 2 * time.Second
	seen, _ := runtime.ThreadCreateProfile(nil)
	last := time.Now()
	for {
		time.Sleep(poll)
		n, _ := runtime.ThreadCreateProfile(nil)
		if n != seen || time.Since(last) >= full {
			seen, last = n, time.Now()
			_ = winHarden()
		}
	}
}

// platformEnv keeps the current directory out of the search for programs.
// The age library starts the plugin by its name. Windows (and so Go) looks
// for a name in the current directory before PATH; Go then refuses to run
// what it found there, so a file called age-plugin-winhello.cmd in a
// directory se-env is run from would stop every decryption. With this
// variable set, the current directory is not searched. Commands started by
// exec and session do not get it (childEnv), unless the caller had set it.
func (a *app) platformEnv() {
	const k = "NoDefaultCurrentDirectoryInExePath"
	if _, ok := os.LookupEnv(k); !ok {
		os.Setenv(k, "1")
		a.addedEnv = append(a.addedEnv, k)
	}
}

func (a *app) uiPath() string { return filepath.Join(filepath.Dir(a.self), uiFile) }

// platformPaths: the DLL and the dialog program are trusted like se-env.
func (a *app) platformPaths() []string {
	return []string{filepath.Join(dllDir(), dllFile), a.uiPath()}
}

func unsafePath(p string, fi os.FileInfo) bool { return !winPathSafe(p) }

func secureDir(p string) error {
	if err := os.MkdirAll(p, 0o700); err != nil {
		return err
	}
	return winSecureDir(p)
}

func isExecutable(fi os.FileInfo) bool {
	return !fi.IsDir() && strings.EqualFold(filepath.Ext(fi.Name()), ".exe")
}

func lockFile(f *os.File, exclusive bool) error { return winLock(f, exclusive, true) }

func tryLockFile(f *os.File) bool { return winLock(f, true, false) == nil }

// envKey: Windows variable names are case-insensitive.
func envKey(k string) string { return strings.ToUpper(k) }

// platformUnsafeNames are refused as key names on Windows (upper case).
var platformUnsafeNames = map[string]bool{
	"PATHEXT": true, "COMSPEC": true, "PSMODULEPATH": true, "SYSTEMROOT": true, "WINDIR": true,
	"SYSTEMDRIVE": true, "USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
	"PROGRAMDATA": true, "TEMP": true, "TMP": true, "__COMPAT_LAYER": true,
	"HOMEDRIVE": true, "HOMEPATH": true, "PUBLIC": true, "ALLUSERSPROFILE": true,
	"PROGRAMFILES": true, "PROGRAMW6432": true, "COMMONPROGRAMFILES": true, "COMMONPROGRAMW6432": true,
	"NODEFAULTCURRENTDIRECTORYINEXEPATH": true, "PSEXECUTIONPOLICYPREFERENCE": true, "__PSLOCKDOWNPOLICY": true,
	"COR_ENABLE_PROFILING": true, "COR_PROFILER": true, "COR_PROFILER_PATH": true,
	"CORECLR_ENABLE_PROFILING": true, "CORECLR_PROFILER": true, "CORECLR_PROFILER_PATH": true,
	"DOTNET_STARTUP_HOOKS": true, "DOTNET_ROOT": true, "DOTNET_ADDITIONAL_DEPS": true,
	"DOTNET_SHARED_STORE": true,
}

func pathExts() []string {
	var out []string
	for _, e := range strings.Split(os.Getenv("PATHEXT"), ";") {
		if e = strings.TrimSpace(e); strings.HasPrefix(e, ".") {
			out = append(out, strings.ToLower(e))
		}
	}
	if len(out) == 0 {
		out = []string{".com", ".exe", ".bat", ".cmd"}
	}
	return out
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

// lookPathIn finds an executable like exec.LookPath, but in the given PATH.
// Unlike cmd.exe it never searches the current directory or a relative
// PATH entry.
func lookPathIn(name, pathList string) (string, error) {
	exts := pathExts()
	try := func(p string) (string, bool) {
		if ext := strings.ToLower(filepath.Ext(p)); ext != "" {
			for _, e := range exts {
				if e == ext && isFile(p) {
					return p, true
				}
			}
		}
		for _, e := range exts {
			if isFile(p + e) {
				return p + e, true
			}
		}
		return "", false
	}
	if strings.ContainsAny(name, `/\:`) {
		if !filepath.IsAbs(name) {
			return "", exec.ErrNotFound
		}
		if p, ok := try(name); ok {
			return p, nil
		}
		return "", exec.ErrNotFound
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" || !filepath.IsAbs(dir) {
			continue
		}
		if p, ok := try(filepath.Join(dir, name)); ok {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

func defaultShell() string {
	if sh := os.Getenv("COMSPEC"); sh != "" {
		return sh
	}
	return `C:\Windows\System32\cmd.exe`
}

// runCommand runs the command and waits for it: Windows has no exec. The
// values stay in this (hardened) process until the command ends. Ctrl-C
// reaches the command through the console; se-env itself ignores it.
func runCommand(path string, args, env []string) error {
	cmd := exec.Command(path, args[1:]...)
	cmd.Args[0] = args[0]
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	signal.Ignore(os.Interrupt)
	if err := cmd.Start(); err != nil {
		return err
	}
	return childExit(cmd.Wait())
}

// ---- processes ---------------------------------------------------------------

func systemLang() string {
	if l := winUILang(); l != "" {
		return normalizeLocale(l)
	}
	return "en"
}

func peerOf(c *net.UnixConn) (sameUser bool, pid int, err error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return false, 0, err
	}
	var cerr error
	err = raw.Control(func(fd uintptr) { pid, cerr = winPeerPid(fd) })
	if err == nil {
		err = cerr
	}
	if err != nil {
		return false, 0, err
	}
	return winSameUser(pid), pid, nil
}

func procInfo(pid int) (ppid int, started time.Time, err error) { return winProcInfo(pid) }

// procName returns a process's parent and the name of its executable
// without ".exe" ("WindowsTerminal", "claude").
func procName(pid int) (ppid int, name string, ok bool) {
	ppid, _, err := winProcInfo(pid)
	if err != nil {
		return 0, "", false
	}
	name = "?"
	if exe, err := winProcImage(pid); err == nil && exe != "" {
		base := displayName(exe)
		if strings.EqualFold(filepath.Ext(base), ".exe") {
			base = base[:len(base)-4]
		}
		if base = printable(base); base != "" {
			name = base
		}
	}
	return ppid, name, true
}

// ---- sessions ----------------------------------------------------------------

// sessionSys: the job object the agent created and joined before it started
// the command. Members are the processes in it (they cannot break away), so
// the command's pid is not needed and a reused pid does not matter.
type sessionSys struct{ job uintptr }

// bindDirect: members are known before the command starts, so the socket is
// bound under its final name at once.
const bindDirect = true

func (ag *agent) prepare() error {
	job, err := winJobCreateJoin()
	if err != nil {
		return err
	}
	ag.sys.job = job
	return nil
}

// restrictSocket: the socket inherits the protected ACL of run\.
func restrictSocket(string) error { return nil }

func (ag *agent) setRoot(int) {}

// release: the job handle stays open until the process ends, so a
// connection still being examined never sees a reused handle value.
func (ag *agent) release() {}

func (ag *agent) isMember(pid int, accepted time.Time) bool {
	return winJobContains(ag.sys.job, pid)
}

const wsaECONNREFUSED = 10061

func isConnRefused(err error) bool { return errors.Is(err, syscall.Errno(wsaECONNREFUSED)) }

// ---- signals -----------------------------------------------------------------

// Ctrl-C reaches every process on the console, the plugin included, so there
// is nothing to stop.
func stopChildren() {}

func installInterruptHandler() {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	go func() {
		<-sigs
		os.Exit(130)
	}()
}

func ignoreInterrupts()  { signal.Ignore(os.Interrupt) }
func restoreInterrupts() { signal.Reset(os.Interrupt) }

type sessionSignals struct{ ch chan os.Signal }

// catchSessionSignals: Ctrl-C is meant for the command, which gets it from
// the console.
func catchSessionSignals() *sessionSignals {
	s := &sessionSignals{ch: make(chan os.Signal, 4)}
	signal.Notify(s.ch, os.Interrupt)
	return s
}

func (s *sessionSignals) forward(*os.Process) {}

func (s *sessionSignals) stop() { signal.Stop(s.ch) }

func childExit(err error) error {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return exitCode(ee.ExitCode())
	}
	return err
}

// ---- dialogs (se-env-ui.exe) -------------------------------------------------

func (a *app) dialogSecret(label string) (string, error) {
	out, err := exec.Command(a.uiPath(), "secret", "se-env", a.t.S("dialog_value", "Label", label)).Output()
	if err != nil {
		return "", a.fail("err_cancelled")
	}
	return string(out), nil
}

func (a *app) dialogConfirm(msg string) error {
	cmd := exec.Command(a.uiPath(), "confirm", "se-env", msg)
	if err := cmd.Run(); err != nil {
		return a.fail("err_aborted")
	}
	return nil
}

// alertDialog: se-env-ui takes the lock on the alert file itself and keeps
// it while the dialog is on screen. It is detached, so it stays after se-env
// has exited. A variable so the tests can replace it.
var alertDialog = func(a *app, profile, title, msg string) {
	cmd := exec.Command(a.uiPath(), "alert", a.alertFile(profile), title, msg)
	const detachedProcess, newProcessGroup = 0x00000008, 0x00000200
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: detachedProcess | newProcessGroup}
	_ = cmd.Start()
}

// ---- messages ----------------------------------------------------------------

// platformLocales replace the messages that differ on Windows.
var platformLocales = []string{"locales/windows"}

// platformWords: the messages are written for macOS; these names are swapped
// in every message on Windows. Longer names come first.
var platformWords = strings.NewReplacer(
	"age-plugin-se", "age-plugin-winhello",
	"Touch ID (or your password)", "Windows Hello (face, fingerprint or PIN)",
	"Touch ID（またはパスワード）", "Windows Hello（顔・指紋・PIN）",
	"Touch ID / password", "Windows Hello",
	"Touch ID / パスワード", "Windows Hello",
	"Secure Enclave（Touch ID）", "Windows Hello",
	"Secure Enclave key (Touch ID)", "Windows Hello key",
	"Secure Enclave の鍵", "Windows Hello の鍵",
	"Secure Enclave 鍵", "Windows Hello の鍵",
	"Touch ID", "Windows Hello",
	"Secure Enclave", "Windows Hello",
	"この Mac", "この PC",
	"このMac", "このPC",
	"this Mac", "this PC",
	"the Mac", "the PC",
)

func platformText(s string) string { return platformWords.Replace(s) }

// platformReady: nothing that touches the vault runs without the DLL, or in
// a process that could not be hardened.
func (a *app) platformReady() error {
	if err := loadWin(); err != nil {
		return a.fail("err_dll_missing", "Path", filepath.Join(dllDir(), dllFile))
	}
	if hardenErr != nil {
		return a.fail("err_harden_failed", "Value", hardenErr.Error())
	}
	return nil
}

// deleteKeyLine removes the Windows Hello key of an identity line (destroy,
// rekey). It asks for nothing.
func (a *app) deleteKeyLine(l string) {
	plug := a.pluginPath()
	if plug == "" {
		return
	}
	cmd := exec.Command(plug, "delete")
	cmd.Stdin = strings.NewReader(l + "\n")
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
}

// ---- secrets in memory --------------------------------------------------------

// sealSecret returns the form in which the agent keeps a decrypted profile
// between requests: encrypted with a key only this process can use, so that
// reading the agent's memory (or a dump of it) does not show the values.
// Layout: one version byte (1), then the encrypted block
// "length (4 bytes) | data | padding to 16". ok is false when the block
// could not be encrypted: the agent does not keep the profile then.
func sealSecret(plain []byte) (sealed []byte, ok bool) {
	n := (4 + len(plain) + 15) / 16 * 16
	out := make([]byte, 1+n)
	out[0] = 1
	blk := out[1:]
	binary.LittleEndian.PutUint32(blk, uint32(len(plain)))
	copy(blk[4:], plain)
	if winProtect(blk, true) != nil {
		wipe(out)
		return nil, false
	}
	return out, true
}

// openSecret returns a new copy of the plaintext of a sealed profile. ok is
// false when it cannot be opened; an empty profile opens to nothing with ok.
func openSecret(sealed []byte) (plain []byte, ok bool) {
	if len(sealed) < 1+16 || sealed[0] != 1 {
		return nil, false
	}
	blk := append([]byte(nil), sealed[1:]...)
	defer wipe(blk)
	if winProtect(blk, false) != nil {
		return nil, false
	}
	n := int(binary.LittleEndian.Uint32(blk))
	if n > len(blk)-4 {
		return nil, false
	}
	return append([]byte(nil), blk[4:4+n]...), true
}

// platformTick re-applies the hardening to threads started since the last
// call. The agent calls it while it serves; se-env calls it before it decrypts.
func platformTick() { _ = winHarden() }
