package main

import (
	"bytes"
	"debug/pe"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The test binary is not next to se_env_win.dll: SE_ENV_TEST_DLL_DIR says
// where it is (win\target\release after `cargo build --release`). The shell
// scripts of the tests need Git for Windows' sh and its tools on PATH.
//
// se-env refuses a DLL that others may write to, and a build directory
// often is one (a non-system drive lets Authenticated Users modify). So the
// DLL is copied to a new directory in %TEMP% once; the se-env processes the
// tests start use that copy.
func init() {
	if d := os.Getenv("SE_ENV_TEST_DLL_DIR"); d != "" {
		if os.Getenv("SE_ENV_TEST_DLL_COPIED") == "" {
			d = copyTestDLL(d)
			os.Setenv("SE_ENV_TEST_DLL_DIR", d)
			os.Setenv("SE_ENV_TEST_DLL_COPIED", "1")
			testCleanups = append(testCleanups, func() { os.RemoveAll(d) })
		}
		winDLLDir = d
	}
	if sh := testShell(); sh != "" {
		os.Setenv("PATH", filepath.Dir(sh)+";"+os.Getenv("PATH"))
	}
}

func copyTestDLL(from string) string {
	dir, err := os.MkdirTemp("", "sedll")
	if err != nil {
		return from
	}
	src, err := os.Open(filepath.Join(from, dllFile))
	if err != nil {
		return from
	}
	defer src.Close()
	dst, err := os.Create(filepath.Join(dir, dllFile))
	if err != nil {
		return from
	}
	defer dst.Close()
	if _, err := io.Copy(dst, src); err != nil {
		return from
	}
	return dir
}

// testTempDir: Windows sockets allow 108 bytes, so %TEMP% is short enough.
func testTempDir() string { return "" }

// testShell is Git for Windows' sh, or "" if it is not installed.
func testShell() string {
	for _, p := range []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Git", "usr", "bin", "sh.exe"),
		`C:\Program Files\Git\usr\bin\sh.exe`,
	} {
		if isFile(p) {
			return p
		}
	}
	return ""
}

func TestWinBasics(t *testing.T) {
	if err := loadWin(); err != nil {
		t.Skip("se_env_win.dll:", err)
	}
	ppid, created, err := winProcInfo(os.Getpid())
	if err != nil || ppid != os.Getppid() || created.IsZero() {
		t.Errorf("procInfo: %d %v %v (ppid %d)", ppid, created, err, os.Getppid())
	}
	if img, err := winProcImage(os.Getpid()); err != nil || !strings.EqualFold(img, os.Args[0]) && !strings.HasSuffix(strings.ToLower(img), ".exe") {
		t.Errorf("procImage: %q %v (args[0] %q)", img, err, os.Args[0])
	}
	if !winSameUser(os.Getpid()) {
		t.Error("sameUser(self)")
	}
	if winSameUser(4) {
		t.Error("sameUser(System)")
	}
	t.Logf("UI language %q", winUILang())

	dir := t.TempDir()
	if err := winSecureDir(dir); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "f")
	os.WriteFile(f, []byte("x"), 0o600)
	if !winPathSafe(dir) || !winPathSafe(f) {
		t.Fatal("a secured directory is not safe")
	}
	// Anyone may write: refused. icacls is part of Windows.
	if out, err := exec.Command("icacls", f, "/grant", "*S-1-1-0:(W)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v %s", err, out)
	}
	if winPathSafe(f) {
		t.Error("an Everyone-writable file is safe")
	}
}

// The same as TestUnsafePermissionsAreRefused, with ACLs.
func TestUnsafeACLsAreRefused(t *testing.T) {
	if err := loadWin(); err != nil {
		t.Skip("se_env_win.dll:", err)
	}
	a := testApp(t)
	os.MkdirAll(a.vault, 0o700)
	if err := secureDir(a.home); err != nil {
		t.Fatal(err)
	}
	if err := a.requireInit(); err != nil {
		t.Fatal(err)
	}
	exec.Command("icacls", a.vault, "/grant", "*S-1-5-11:(OI)(CI)(M)").Run() // Authenticated Users
	if err := a.requireInit(); err == nil {
		t.Error("a vault others can write to was accepted")
	}
}

func TestWinLock(t *testing.T) {
	if err := loadWin(); err != nil {
		t.Skip("se_env_win.dll:", err)
	}
	p := filepath.Join(t.TempDir(), "l")
	f1, _ := os.Create(p)
	defer f1.Close()
	f2, _ := os.OpenFile(p, os.O_RDWR, 0)
	defer f2.Close()
	if !tryLockFile(f1) {
		t.Fatal("first lock")
	}
	if tryLockFile(f2) {
		t.Fatal("second lock while the first is held")
	}
	f1.Close()
	if !tryLockFile(f2) {
		t.Fatal("lock after the first handle closed")
	}
}

// AF_UNIX: both ends learn the other's pid, and the job decides membership.
func TestWinSocketPeer(t *testing.T) {
	if err := loadWin(); err != nil {
		t.Skip("se_env_win.dll:", err)
	}
	p := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan error, 1)
	go func() {
		c, err := ln.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		same, pid, err := peerOf(c)
		if err == nil && (!same || pid != os.Getpid()) {
			err = fmt.Errorf("server side: same=%v pid=%d", same, pid)
		}
		done <- err
	}()
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: p, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	same, pid, err := peerOf(c)
	if err != nil || !same || pid != os.Getpid() {
		t.Errorf("client side: same=%v pid=%d err=%v", same, pid, err)
	}
	if err := <-done; err != nil {
		t.Error(err)
	}
	if !descendsFrom(os.Getpid(), os.Getpid()) || !descendsFrom(os.Getpid(), os.Getppid()) {
		t.Error("descendsFrom")
	}
}

// The cache of a session is encrypted while it is not being served.
func TestSealSecret(t *testing.T) {
	if err := loadWin(); err != nil {
		t.Skip("se_env_win.dll:", err)
	}
	for _, n := range []int{0, 1, 11, 12, 13, 28, 1000} {
		plain := bytes.Repeat([]byte("TOKEN=c2VjcmV0\n"), n)[:n]
		sealed, ok := sealSecret(plain)
		if !ok || sealed[0] != 1 {
			t.Fatalf("len %d: not encrypted", n)
		}
		if n >= 8 && bytes.Contains(sealed, plain[:8]) {
			t.Fatalf("len %d: the sealed form contains the plaintext", n)
		}
		if got, ok := openSecret(sealed); !ok || !bytes.Equal(got, plain) {
			t.Fatalf("len %d: round trip gave %q %v", n, got, ok)
		}
		// A block that was not encrypted is never taken for a profile.
		sealed[0] = 0
		if got, ok := openSecret(sealed); ok || got != nil {
			t.Fatalf("len %d: an unencrypted block was opened", n)
		}
	}
	for _, garbage := range [][]byte{nil, {1, 2, 3}, bytes.Repeat([]byte{1}, 17)} {
		if got, ok := openSecret(garbage); ok || got != nil {
			t.Errorf("garbage was opened: %x", garbage)
		}
	}
}

// Go builds position-independent executables on Windows: the image, heap and
// stacks land at addresses that differ from run to run (ASLR, 64-bit entropy),
// and data is never executable. se-env.exe must keep these.
func TestWinImageIsRandomized(t *testing.T) {
	f, err := pe.Open(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	oh, ok := f.OptionalHeader.(*pe.OptionalHeader64)
	if !ok {
		t.Fatal("not a 64-bit image")
	}
	const highEntropyVA, dynamicBase, nxCompat = 0x0020, 0x0040, 0x0100
	for name, flag := range map[string]uint16{"HIGH_ENTROPY_VA": highEntropyVA, "DYNAMIC_BASE": dynamicBase, "NX_COMPAT": nxCompat} {
		if oh.DllCharacteristics&flag == 0 {
			t.Errorf("the image lacks %s (DllCharacteristics %#x)", name, oh.DllCharacteristics)
		}
	}
}

// Hardening twice is fine, and the process still works afterwards (it can
// start a child, open files, and load nothing that is not Microsoft's).
func TestWinHardenTwice(t *testing.T) {
	if err := loadWin(); err != nil {
		t.Skip("se_env_win.dll:", err)
	}
	for i := 0; i < 2; i++ {
		if err := winHarden(); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("cmd", "/c", "echo ok").Output(); err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("child after hardening: %q %v", out, err)
	}
}

// The plugin is started by name. A file of that name in the current
// directory must neither run nor stand in the way, and the variable that
// arranges this stays out of the commands se-env starts.
func TestCurrentDirectoryIsNotSearched(t *testing.T) {
	const k = "NoDefaultCurrentDirectoryInExePath"
	keepEnv(t, k)
	os.Unsetenv(k)
	cwd, bin := t.TempDir(), t.TempDir()
	for _, dir := range []string{cwd, bin} {
		if err := os.WriteFile(filepath.Join(dir, pluginName+".cmd"), []byte("@echo off\r\n"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	t.Setenv("PATH", bin)
	if _, err := exec.LookPath(pluginName); !errors.Is(err, exec.ErrDot) {
		t.Fatalf("without the variable Go should find the file in the current directory and refuse it: %v", err)
	}
	a := testApp(t)
	a.ownEnv()
	got, err := exec.LookPath(pluginName)
	if err != nil || !strings.EqualFold(filepath.Dir(got), bin) {
		t.Fatalf("the plugin must come from PATH: %q %v", got, err)
	}
	for _, e := range a.childEnv(nil) {
		if name, _, _ := strings.Cut(e, "="); strings.EqualFold(name, k) {
			t.Fatalf("the command would see %s", e)
		}
	}
	// A caller who had set it keeps it.
	os.Setenv(k, "caller")
	b := testApp(t)
	b.ownEnv()
	if len(b.addedEnv) != 0 || !strings.Contains(strings.Join(b.childEnv(nil), "\n"), k+"=caller") {
		t.Fatalf("the caller's own setting was not kept: %v", b.addedEnv)
	}
}
