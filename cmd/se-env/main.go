// Command se-env stores secrets in profiles encrypted with a Secure Enclave
// key (via age and age-plugin-se) and hands them to commands on demand.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sync"
	"syscall"
)

const version = "0.7.0"

// exitTampered is the exit status when a profile's public values were changed
// outside se-env. Nothing is run or printed in that case.
const exitTampered = 3

const pluginName = "age-plugin-se"

var (
	reProfile = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	reKey     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	reSession = regexp.MustCompile(`^[0-9a-f]{16}$`)
)

// app holds resolved paths and the localizer for one invocation.
type app struct {
	self, home, bin, vault string
	identityPath           string
	runDir, origPath       string
	t                      *translator

	sess        *session // set by activeSession
	sessChecked bool

	lockMu sync.Mutex
	locked map[string]bool // profiles this process holds the vault lock for
}

// userError is printed as "se-env: <msg>" and exits with status 1.
type userError struct{ msg string }

func (e *userError) Error() string { return e.msg }

// exitCode carries a child's exit status out of run().
type exitCode int

func (e exitCode) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func (a *app) fail(id string, kv ...any) error { return &userError{a.t.S(id, kv...)} }

func newApp() (*app, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	self, err := filepath.EvalSymlinks(exe)
	if err != nil {
		self = exe
	}
	a := &app{self: self, origPath: os.Getenv("PATH"), t: newTranslator()}
	// The home is always the directory above the program (<home>/bin/se-env).
	// It cannot be redirected through the environment.
	a.home = filepath.Dir(filepath.Dir(self))
	a.bin = filepath.Join(a.home, "bin")
	a.vault = filepath.Join(a.home, "vault")
	a.identityPath = filepath.Join(a.home, "identity.txt")
	a.runDir = filepath.Join(a.home, "run")

	// AGEDEBUG=plugin makes the age library copy the plugin protocol, file key
	// included, to stderr.
	os.Unsetenv("AGEDEBUG")
	// The age library starts the plugin by name through PATH. Narrow PATH to
	// the one directory that holds it; commands started by exec and session
	// get the original PATH back (childEnv).
	dir := a.bin
	if p := a.pluginPath(); p != "" {
		dir = filepath.Dir(p)
	}
	os.Setenv("PATH", dir)
	return a, nil
}

// pluginPath finds age-plugin-se: <home>/bin first, then the absolute
// directories of the caller's PATH. Relative PATH entries are never used.
func (a *app) pluginPath() string {
	dirs := append([]string{a.bin}, filepath.SplitList(a.origPath)...)
	for _, dir := range dirs {
		if !filepath.IsAbs(dir) {
			continue
		}
		p := filepath.Join(dir, pluginName)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func ageVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "filippo.io/age" {
				return d.Version
			}
		}
	}
	return "unknown"
}

func main() {
	syscall.Umask(0o077)
	// Secrets live in this process's memory: never write a core file.
	_ = syscall.Setrlimit(syscall.RLIMIT_CORE, &syscall.Rlimit{})
	a, err := newApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "se-env:", err)
		os.Exit(1)
	}
	os.Exit(report(a.run(os.Args[1:])))
}

// report prints an error, if it still needs printing, and returns the exit status.
func report(err error) int {
	var ue *userError
	var ec exitCode
	var te *tamperError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ec):
		return int(ec)
	case errors.As(err, &te): // already reported by tampered()
		return exitTampered
	case errors.As(err, &ue):
		fmt.Fprintln(os.Stderr, "se-env:", ue.msg)
	default:
		fmt.Fprintln(os.Stderr, "se-env:", err)
	}
	return 1
}

func (a *app) run(args []string) error {
	if len(args) == 0 {
		a.cmdHelp()
		return exitCode(1)
	}
	cmd, rest := args[0], args[1:]
	if cmd != "session" { // session forwards signals to its command instead
		installInterruptHandler()
	}
	switch cmd {
	case "init":
		return a.cmdInit()
	case "list", "tree":
		return a.cmdList(rest)
	case "profiles":
		return a.cmdProfiles()
	case "ls":
		return a.cmdLs(rest)
	case "set":
		return a.cmdSet(rest)
	case "get":
		return a.cmdGet(rest)
	case "header":
		return a.cmdHeader(rest)
	case "exec":
		return a.cmdExec(rest)
	case "rm":
		return a.cmdRm(rest)
	case "rm-profile":
		return a.cmdRmProfile(rest)
	case "session":
		return a.cmdSession(rest)
	case "lock":
		return a.cmdLock()
	case "cache":
		return a.cmdCache(rest)
	case "destroy":
		return a.cmdDestroy(rest)
	case "__complete":
		return a.cmdComplete(rest)
	case "help", "-h", "--help":
		a.cmdHelp()
		return nil
	case "version", "--version":
		fmt.Printf("se-env %s (age %s)\n", version, ageVersion())
		return nil
	default:
		return a.fail("err_unknown_command", "Cmd", cmd)
	}
}

func (a *app) checkProfile(p string) error {
	if !reProfile.MatchString(p) {
		return a.fail("err_invalid_profile", "Name", orEmpty(p))
	}
	return nil
}

func (a *app) checkKey(k string) error {
	if !reKey.MatchString(k) {
		return a.fail("err_invalid_key", "Name", orEmpty(k))
	}
	return nil
}

func orEmpty(s string) string {
	if s == "" {
		return "<empty>"
	}
	return s
}

func (a *app) requireInit() error {
	if !exists(a.identityPath) {
		return a.fail("err_not_initialized", "Home", a.home)
	}
	return a.checkPerms()
}

// checkPerms refuses to work when something se-env trusts could be replaced
// by another user: every such path must be owned by this user (or root) and
// must not be writable by group or others.
func (a *app) checkPerms() error {
	paths := []string{a.home, a.bin, a.self, a.identityPath, a.vault, a.runDir}
	if p := a.pluginPath(); p != "" {
		paths = append(paths, filepath.Dir(p), p)
	}
	uid := uint32(os.Getuid())
	for _, p := range paths {
		if p == "" {
			continue
		}
		fi, err := os.Stat(p)
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if fi.Mode().Perm()&0o022 != 0 || (ok && st.Uid != uid && st.Uid != 0) {
			return a.fail("err_unsafe_perms", "Path", p)
		}
	}
	return nil
}

func (a *app) profileFile(p string) string { return filepath.Join(a.vault, p+".age") }
func (a *app) indexFile(p string) string   { return filepath.Join(a.vault, p+".keys") }
func (a *app) lockFile(p string) string    { return filepath.Join(a.vault, "."+p+".lock") }

// publicFile is the plaintext copy of a profile's public values, for display.
func (a *app) publicFile(p string) string { return filepath.Join(a.vault, p+".public") }

// alertFile is locked by the dialog that reports tampering while it is on screen.
func (a *app) alertFile(p string) string { return filepath.Join(a.vault, "."+p+".alert") }

func (a *app) requireProfile(p string) error {
	if !exists(a.profileFile(p)) {
		return a.fail("err_no_profile", "Name", p)
	}
	return nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }
