package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
)

// The tests use software (X25519) keys instead of the Secure Enclave. When
// KC_ENV_TEST_HOME is set, the test binary acts as kc-env itself with that
// home, so tests can run real kc-env processes (the session agent decides by
// process ancestry, which only real processes can exercise).
func TestMain(m *testing.M) {
	softwareKeys = true
	// No dialog in the tests: the alert is appended to <home>/alerts instead.
	alertDialog = func(a *app, profile, title, msg string) {
		f, err := os.OpenFile(filepath.Join(a.home, "alerts"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%s|%s|%s\n", profile, title, strings.ReplaceAll(msg, "\n", " "))
			f.Close()
		}
	}
	if path := os.Getenv("KC_ENV_TEST_FAKE_AGENT"); path != "" {
		fakeAgent(path)
	}
	if home := os.Getenv("KC_ENV_TEST_HOME"); home != "" {
		os.Exit(report(appAt(home).run(os.Args[1:])))
	}
	os.Exit(m.Run())
}

func localeIDs(t *testing.T, name string) map[string]any {
	t.Helper()
	data, err := localeFS.ReadFile("locales/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if _, err := toml.Decode(string(data), &m); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return m
}

// Every message in en.toml must exist in every other locale, and vice versa.
func TestLocalesHaveSameIDs(t *testing.T) {
	en := localeIDs(t, "en.toml")
	entries, _ := localeFS.ReadDir("locales")
	for _, e := range entries {
		if e.Name() == "en.toml" {
			continue
		}
		other := localeIDs(t, e.Name())
		for id := range en {
			if _, ok := other[id]; !ok {
				t.Errorf("%s: missing %q", e.Name(), id)
			}
		}
		for id := range other {
			if _, ok := en[id]; !ok {
				t.Errorf("%s: extra %q (not in en.toml)", e.Name(), id)
			}
		}
	}
}

// Every ID used in the Go sources must exist in en.toml.
func TestSourceIDsExist(t *testing.T) {
	en := localeIDs(t, "en.toml")
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, _ := os.ReadFile(f)
		src := string(b)
		for _, call := range []string{`.S("`, `.N("`, `.fail("`} {
			for _, part := range strings.Split(src, call)[1:] {
				id := part[:strings.Index(part, `"`)]
				if _, ok := en[id]; !ok {
					t.Errorf("%s: message %q is not in en.toml", f, id)
				}
			}
		}
		// usage IDs passed through profileKeyArgs
		for _, part := range strings.Split(src, `"usage_`)[1:] {
			id := "usage_" + part[:strings.Index(part, `"`)]
			if _, ok := en[id]; !ok {
				t.Errorf("%s: message %q is not in en.toml", f, id)
			}
		}
	}
}

// Rendering with full data must not leave "<no value>" (a misspelt placeholder).
func TestTemplatesRender(t *testing.T) {
	data := map[string]any{}
	for _, k := range []string{"Version", "AgeVersion", "Lang", "Self", "Home", "Identity",
		"Sessions", "Vault", "Completion", "Shell", "Plugin", "Name", "Profile", "Key", "Cmd", "Path",
		"Prompt", "Label", "Opt", "Value", "ID", "Start", "Expires", "Remaining", "Scope", "Cached",
		"State", "Created", "Profiles", "Keys", "AtLeast", "Servers", "Phrase", "Seconds", "Paths", "Count", "Updated",
		"Shown", "Real"} {
		data[k] = "X"
	}
	b := newBundle()
	for _, lang := range []string{"en", "ja"} {
		loc := i18n.NewLocalizer(b, lang)
		for id := range localeIDs(t, "en.toml") {
			s, err := loc.Localize(&i18n.LocalizeConfig{MessageID: id, TemplateData: data, PluralCount: 2})
			if err != nil {
				t.Errorf("%s/%s: %v", lang, id, err)
				continue
			}
			if strings.Contains(s, "<no value>") {
				t.Errorf("%s/%s: unknown placeholder in %q", lang, id, s)
			}
		}
	}
}

func TestNormalizeLocale(t *testing.T) {
	for in, want := range map[string]string{
		"ja_JP.UTF-8": "ja-JP", "en_US": "en-US", "C": "en", "POSIX": "en", "de_DE@euro": "de-DE", "ja": "ja",
	} {
		if got := normalizeLocale(in); got != want {
			t.Errorf("normalizeLocale(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDetectLangPrecedence(t *testing.T) {
	t.Setenv("KC_ENV_LANG", "")
	t.Setenv("LC_ALL", "")
	t.Setenv("LC_MESSAGES", "ja_JP.UTF-8")
	t.Setenv("LANG", "en_US.UTF-8")
	if got := detectLang(); got != "ja-JP" {
		t.Errorf("got %q, want ja-JP (LC_MESSAGES before LANG)", got)
	}
	t.Setenv("KC_ENV_LANG", "en")
	if got := detectLang(); got != "en" {
		t.Errorf("got %q, want en (KC_ENV_LANG wins)", got)
	}
}

func TestProfileRoundTrip(t *testing.T) {
	kv := map[string]string{"A": "plain", "B": "with = and spaces", "C": "multi\nline", "D": "日本語"}
	got, err := parseProfile(serialize(kv))
	if err != nil || !reflect.DeepEqual(got, kv) {
		t.Fatalf("round trip: %v %v", got, err)
	}
}

// Format written by the zsh version: `print -rn -- $v | base64`, sorted, one per line.
func TestParseLegacyFormat(t *testing.T) {
	legacy := "API_TOKEN=c2VjcmV0\nOTHER=dj0x\n"
	got, err := parseProfile([]byte(legacy))
	want := map[string]string{"API_TOKEN": "secret", "OTHER": "v=1"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v %v", got, err)
	}
	if string(serialize(want)) != legacy {
		t.Fatalf("serialize differs from legacy: %q", serialize(want))
	}
}

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]time.Duration{"90s": 90 * time.Second, "30m": 30 * time.Minute,
		"2h": 2 * time.Hour, "1d": 24 * time.Hour, "24h": 24 * time.Hour, "45": 45 * time.Second} {
		if got, ok := parseTTL(in); !ok || got != want {
			t.Errorf("parseTTL(%q) = %v %v", in, got, ok)
		}
	}
	for _, bad := range []string{"", "0m", "-1m", "5x", "m", "25h", "2d", "99999d", "999999999999999d", "99999999999999999999s"} {
		if _, ok := parseTTL(bad); ok {
			t.Errorf("parseTTL(%q) should fail", bad)
		}
	}
}

func TestDisplayWidth(t *testing.T) {
	if displayWidth("abc") != 3 || displayWidth("期限切れ") != 8 || displayWidth("a有効") != 5 {
		t.Error("displayWidth")
	}
}

const bogusIdentity = "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ9Z5\n"

func appAt(home string) *app {
	return &app{self: filepath.Join(home, "bin", "kc-env"), home: home, bin: filepath.Join(home, "bin"),
		vault: filepath.Join(home, "vault"), identityPath: filepath.Join(home, "identity.txt"),
		runDir: filepath.Join(home, "run"), origPath: os.Getenv("PATH"), t: newTranslator()}
}

// testApp builds an app in a temp home with a software (X25519) key. The home
// is under /tmp because a Unix socket path must stay short.
func testApp(t *testing.T) *app {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "kc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(home, "bin"), 0o700)
	os.WriteFile(filepath.Join(home, "identity.txt"), []byte("# test\n"+id.String()+"\n"), 0o600)
	t.Setenv("KC_ENV_SESSION", "")
	t.Setenv("KC_ENV_LANG", "en")
	return appAt(home)
}

func breakMainKey(a *app) { os.WriteFile(a.identityPath, []byte(bogusIdentity), 0o600) }

// fresh forgets the cached session lookup, like a new kc-env process.
func fresh(a *app) *app { a.sess, a.sessChecked = nil, false; return a }

// startAgent runs a session agent in this process for the tree rooted at root.
func startAgent(t *testing.T, a *app, ttl time.Duration, only []string, root int) *agent {
	t.Helper()
	ag, err := a.newAgent(ttl, only, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := ag.serve(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ag.shutdown)
	return ag
}

func TestSaveLoadAndIndex(t *testing.T) {
	a := testApp(t)
	if err := a.save("work", map[string]string{"B": "2", "A": "1"}); err != nil {
		t.Fatal(err)
	}
	kv, err := a.load("work")
	if err != nil || kv["A"] != "1" || kv["B"] != "2" {
		t.Fatalf("load: %v %v", kv, err)
	}
	keys, ok := a.indexKeys("work")
	if !ok || !reflect.DeepEqual(keys, []string{"A", "B"}) {
		t.Fatalf("index: %v %v", keys, ok)
	}
	if got := a.allProfiles(); !reflect.DeepEqual(got, []string{"work"}) {
		t.Fatalf("profiles: %v", got)
	}
	ct, _ := os.ReadFile(a.profileFile("work"))
	if !strings.HasPrefix(string(ct), "age-encryption.org/v1") || strings.Contains(string(ct), "A=") {
		t.Fatal("vault file is not age ciphertext")
	}
	if !a.authPresence() {
		t.Fatal("authPresence with the right key failed")
	}
	breakMainKey(a)
	if a.authPresence() {
		t.Fatal("authPresence passed without the key")
	}
}

// Only one Secure Enclave key line is accepted; software keys are test-only.
func TestIdentityIsRestricted(t *testing.T) {
	a := testApp(t)
	good, _ := os.ReadFile(a.identityPath)
	other, _ := age.GenerateX25519Identity()

	os.WriteFile(a.identityPath, append(good, []byte(other.String()+"\n")...), 0o600)
	if _, err := a.identityLine(); err == nil {
		t.Error("two identity lines were accepted")
	}
	os.WriteFile(a.identityPath, []byte("AGE-PLUGIN-EVIL-1QQQQ\n"), 0o600)
	if _, err := a.identityLine(); err == nil {
		t.Error("an identity of another plugin was accepted")
	}
	os.WriteFile(a.identityPath, good, 0o600)
	softwareKeys = false
	_, err := a.identityLine()
	softwareKeys = true
	if err == nil {
		t.Error("a software key was accepted outside the tests")
	}
}

// A stray recipient.txt (kc-env <= 0.4 trusted it) must have no effect:
// profiles are encrypted to the key derived from identity.txt only.
func TestRecipientFileIsIgnored(t *testing.T) {
	a := testApp(t)
	attacker, _ := age.GenerateX25519Identity()
	os.WriteFile(filepath.Join(a.home, "recipient.txt"), []byte(attacker.Recipient().String()+"\n"), 0o600)
	if err := a.save("work", map[string]string{"K": "secret"}); err != nil {
		t.Fatal(err)
	}
	ct, _ := os.ReadFile(a.profileFile("work"))
	if _, err := decryptBytes(ct, attacker); err == nil {
		t.Fatal("profile is readable with the key from recipient.txt")
	}
	if kv, err := a.load("work"); err != nil || kv["K"] != "secret" {
		t.Fatalf("load: %v %v", kv, err)
	}
}

func TestProfileRejectsUnsafeContent(t *testing.T) {
	b64 := "dg==" // "v"
	for _, bad := range []string{"PATH=" + b64, "DYLD_INSERT_LIBRARIES=" + b64, "LD_PRELOAD=" + b64,
		"NODE_OPTIONS=" + b64, "KC_ENV_SESSION=" + b64, "=" + b64, "A B=" + b64, "A=" + b64 + "\nA=" + b64,
		"A=AA=="} { // AA== decodes to a NUL byte
		if _, err := parseProfile([]byte(bad + "\n")); err == nil {
			t.Errorf("parseProfile accepted %q", bad)
		}
	}
	if kv, err := parseProfileOpt([]byte("PATH="+b64+"\nOK="+b64+"\n"), true); err != nil || len(kv) != 2 {
		t.Errorf("lenient parse (used by rm): %v %v", kv, err)
	}
	for _, k := range []string{"PATH", "HOME", "DYLD_X", "LD_X", "KC_ENV_X", "BASH_ENV", "NODE_OPTIONS"} {
		if !unsafeKey(k) {
			t.Errorf("unsafeKey(%q) = false", k)
		}
	}
	for _, k := range []string{"GITHUB_TOKEN", "API_KEY", "PATHS", "MY_PATH", "LDAP_PASSWORD"} {
		if unsafeKey(k) {
			t.Errorf("unsafeKey(%q) = true", k)
		}
	}
}

// A profile swapped for one with a refused key is not handed to commands,
// but the key can still be removed.
func TestUnsafeKeyInVault(t *testing.T) {
	a := testApp(t)
	r, _ := a.recipient()
	ct, _ := encryptBytes([]byte("NODE_OPTIONS=dg==\nTOKEN=dg==\n"), r)
	os.MkdirAll(a.vault, 0o700)
	os.WriteFile(a.profileFile("work"), ct, 0o600)
	if _, err := a.load("work"); err == nil || !strings.Contains(err.Error(), "NODE_OPTIONS") {
		t.Fatalf("load: %v", err)
	}
	if kv, err := a.loadFresh("work", true); err != nil || len(kv) != 2 {
		t.Fatalf("lenient load: %v %v", kv, err)
	}
}

func TestIndexAndProfileNamesAreFiltered(t *testing.T) {
	a := testApp(t)
	a.save("work", map[string]string{"A": "1"})
	os.WriteFile(a.indexFile("work"), []byte("A\n\x1b[31mEVIL\n"), 0o600)
	if _, ok := a.indexKeys("work"); ok {
		t.Error("an index with a non-key line was used")
	}
	os.WriteFile(filepath.Join(a.vault, "bad name\x1b.age"), []byte("x"), 0o600)
	if got := a.allProfiles(); !reflect.DeepEqual(got, []string{"work"}) {
		t.Errorf("profiles: %q", got)
	}
}

func TestUnsafePermissionsAreRefused(t *testing.T) {
	a := testApp(t)
	os.MkdirAll(a.vault, 0o700)
	if err := a.requireInit(); err != nil {
		t.Fatal(err)
	}
	os.Chmod(a.vault, 0o777)
	if err := a.requireInit(); err == nil {
		t.Error("a world-writable vault was accepted")
	}
	os.Chmod(a.vault, 0o700)
	os.Chmod(a.identityPath, 0o660)
	if err := a.requireInit(); err == nil {
		t.Error("a group-writable identity.txt was accepted")
	}
}

func TestVaultLockSerialises(t *testing.T) {
	a := testApp(t)
	unlock, err := a.lockProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u, _ := a.lockProfile("work")
		close(got)
		u()
	}()
	select {
	case <-got:
		t.Fatal("second lock did not wait")
	case <-time.After(200 * time.Millisecond):
	}
	unlock()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("lock was not released")
	}
}

// ---- sessions ------------------------------------------------------------------

func TestSessionCache(t *testing.T) {
	a := testApp(t)
	if err := a.save("work", map[string]string{"K": "v1"}); err != nil {
		t.Fatal(err)
	}
	if err := a.save("other", map[string]string{"K": "o1"}); err != nil {
		t.Fatal(err)
	}
	ag := startAgent(t, a, time.Minute, []string{"work"}, os.Getpid())
	t.Setenv("KC_ENV_SESSION", ag.id)

	if s := fresh(a).activeSession(); s == nil || !reflect.DeepEqual(s.info.Only, []string{"work"}) {
		t.Fatalf("activeSession: %+v", s)
	}
	if _, err := fresh(a).load("work"); err != nil { // first load fills the cache
		t.Fatal(err)
	}
	if _, err := fresh(a).load("other"); err != nil { // outside --only: decrypted, not cached
		t.Fatal(err)
	}
	if got := ag.info(true).Cached; len(got) != 1 || got[0].Name != "work" {
		t.Fatalf("cached: %+v", got)
	}
	// Nothing but the socket is on disk.
	if m, _ := filepath.Glob(filepath.Join(a.runDir, "*")); len(m) != 1 || !strings.HasSuffix(m[0], ".sock") {
		t.Fatalf("run dir: %v", m)
	}
	main, _ := os.ReadFile(a.identityPath)
	breakMainKey(a) // from here only the session can answer
	if kv, err := fresh(a).load("work"); err != nil || kv["K"] != "v1" {
		t.Fatalf("cached load: %v %v", kv, err)
	}
	if _, err := fresh(a).load("other"); err == nil {
		t.Fatal("a profile outside --only was served from the session")
	}
	// loadFresh (used by rm) never uses the session.
	if _, err := fresh(a).loadFresh("work", true); err == nil {
		t.Fatal("loadFresh used the session")
	}
	// A save through kc-env refreshes the entry...
	os.WriteFile(a.identityPath, main, 0o600)
	if err := fresh(a).save("work", map[string]string{"K": "v2"}); err != nil {
		t.Fatal(err)
	}
	breakMainKey(a)
	if kv, err := fresh(a).load("work"); err != nil || kv["K"] != "v2" {
		t.Fatalf("refreshed cache: %v %v", kv, err)
	}
	// ...but a vault file changed behind its back invalidates and wipes it.
	ct, _ := os.ReadFile(a.profileFile("other"))
	os.WriteFile(a.profileFile("work"), ct, 0o600)
	if _, err := fresh(a).load("work"); err == nil {
		t.Fatal("stale cache was served")
	}
	if got := ag.info(true).Cached; len(got) != 0 {
		t.Fatalf("stale entry kept: %+v", got)
	}
}

// A save never adds a profile to the session: that takes a decryption.
func TestSaveDoesNotSeedCache(t *testing.T) {
	a := testApp(t)
	ag := startAgent(t, a, time.Minute, nil, os.Getpid())
	t.Setenv("KC_ENV_SESSION", ag.id)
	if err := fresh(a).save("work", map[string]string{"K": "v"}); err != nil {
		t.Fatal(err)
	}
	if got := ag.info(true).Cached; len(got) != 0 {
		t.Fatalf("cached after save only: %+v", got)
	}
}

func TestSessionLockAndEnd(t *testing.T) {
	a := testApp(t)
	a.save("work", map[string]string{"K": "v"})
	ag := startAgent(t, a, time.Minute, nil, os.Getpid())
	t.Setenv("KC_ENV_SESSION", ag.id)
	fresh(a).load("work")
	if err := fresh(a).cmdLock(); err != nil {
		t.Fatal(err)
	}
	if got := ag.info(true).Cached; len(got) != 0 {
		t.Fatalf("lock kept: %+v", got)
	}
	fresh(a).load("work")
	if got := a.sessions(); len(got) != 1 || got[0].ID != ag.id || len(got[0].Cached) != 1 {
		t.Fatalf("sessions: %+v", got)
	}
	a.endSession(ag.id)
	if fresh(a).activeSession() != nil || exists(ag.path) || len(a.sessions()) != 0 {
		t.Fatal("session still reachable after end")
	}
	breakMainKey(a)
	if _, err := fresh(a).load("work"); err == nil {
		t.Fatal("ended session still served a profile")
	}
}

func TestSessionExpires(t *testing.T) {
	a := testApp(t)
	a.save("work", map[string]string{"K": "v"})
	ag := startAgent(t, a, time.Second, nil, os.Getpid())
	t.Setenv("KC_ENV_SESSION", ag.id)
	fresh(a).load("work")
	breakMainKey(a)
	if _, err := fresh(a).load("work"); err != nil {
		t.Fatalf("before expiry: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := fresh(a).load("work"); err == nil {
		t.Fatal("served after the TTL")
	}
	if exists(ag.path) {
		t.Fatal("socket left after expiry")
	}
}

// A process that is not under the session's command gets nothing, whatever
// KC_ENV_SESSION says. Here the "command" is a pid this test does not descend from.
func TestSessionRefusesOutsiders(t *testing.T) {
	a := testApp(t)
	a.save("work", map[string]string{"K": "v"})
	sleeper := exec.Command("/bin/sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()
	ag := startAgent(t, a, time.Minute, nil, sleeper.Process.Pid)
	ag.store("work", strings.Repeat("0", 64), []byte("K=dg==\n"), false) // pretend it is cached
	t.Setenv("KC_ENV_SESSION", ag.id)
	if fresh(a).activeSession() != nil {
		t.Fatal("an outsider was treated as a member")
	}
	for _, rq := range []request{{Op: "acquire", Profile: "work"},
		{Op: "put", Profile: "work", Sum: strings.Repeat("0", 64), Data: []byte("X=dg==\n")},
		{Op: "forget", Profile: "work"}} {
		if rs, err := ask(ag.path, rq, false); err != nil || rs.OK || rs.Hit || len(rs.Data) != 0 {
			t.Errorf("%s from an outsider: %+v %v", rq.Op, rs, err)
		}
	}
	if rs, err := ask(ag.path, request{Op: "status"}, false); err != nil || rs.Info == nil || rs.Info.Member {
		t.Errorf("status: %+v %v", rs, err)
	}
	if !descendsFrom(os.Getpid(), os.Getppid()) || descendsFrom(os.Getpid(), sleeper.Process.Pid) {
		t.Error("descendsFrom")
	}
}

// fakeAgent imitates a session agent that claims every caller is a member and
// serves a forged profile. Whatever a client sends is appended to <path>.got.
func fakeAgent(path string) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		os.Exit(2)
	}
	id := strings.TrimSuffix(filepath.Base(path), ".sock")
	for {
		c, err := ln.Accept()
		if err != nil {
			os.Exit(0)
		}
		go func() {
			defer c.Close()
			dec, enc := json.NewDecoder(c), json.NewEncoder(c)
			for {
				var rq request
				if dec.Decode(&rq) != nil {
					return
				}
				f, _ := os.OpenFile(path+".got", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
				fmt.Fprintf(f, "%s %s %s\n", rq.Op, rq.Profile, rq.Data)
				f.Close()
				enc.Encode(response{OK: true, Hit: rq.Op == "acquire", Data: []byte("K=Zm9yZ2Vk\n"), // "forged"
					Info: &sessionInfo{ID: id, Member: true, Exp: time.Now().Add(time.Hour).Unix(), Cmd: "x\x1b[31m"}})
			}
		}()
	}
}

// A socket served by some other process of this user is never treated as this
// process's session: nothing is read from it and no plaintext is sent to it.
func TestLookAlikeAgentIsNotTrusted(t *testing.T) {
	a := testApp(t)
	a.save("work", map[string]string{"K": "real"})
	os.MkdirAll(a.runDir, 0o700)
	id := "00000000deadbeef"
	fake := exec.Command(os.Args[0])
	fake.Env = append(os.Environ(), "KC_ENV_TEST_FAKE_AGENT="+a.sockPath(id))
	if err := fake.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { fake.Process.Kill(); fake.Wait() }()
	for i := 0; !exists(a.sockPath(id)); i++ {
		if i > 100 {
			t.Fatal("fake agent did not start")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Setenv("KC_ENV_SESSION", id)
	if fresh(a).activeSession() != nil {
		t.Fatal("a look-alike agent was accepted as this process's session")
	}
	if kv, err := fresh(a).load("work"); err != nil || kv["K"] != "real" {
		t.Fatalf("load: %v %v", kv, err)
	}
	if err := fresh(a).save("work", map[string]string{"K": "real2"}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(a.sockPath(id) + ".got")
	if strings.Contains(string(got), "real") || strings.Contains(string(got), "acquire") || strings.Contains(string(got), "put") {
		t.Fatalf("the look-alike agent was sent: %q", got)
	}
	// It still shows up in listings, as cleaned-up metadata only.
	if all := a.sessions(); len(all) != 1 || strings.ContainsRune(all[0].Cmd, 0x1b) {
		t.Fatalf("sessions: %+v", all)
	}
}

// kc runs the test binary as kc-env with the given home.
func kc(t *testing.T, a *app, env []string, stdin string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "KC_ENV_TEST_HOME="+a.home, "KC_ENV_BIN="+os.Args[0])
	cmd.Env = append(cmd.Env, env...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code
}

// End to end with real processes: commands under the session's command are
// served; the same request from outside that tree is not.
func TestSessionEndToEnd(t *testing.T) {
	a := testApp(t)
	for _, c := range [][]string{{"prod", "K", "REAL"}, {"other", "T", "OTHER"}} {
		if _, e, code := kc(t, a, nil, c[2], "set", c[0], c[1]); code != 0 {
			t.Fatalf("set: %s", e)
		}
	}
	os.WriteFile(filepath.Join(a.home, "bogus"), []byte(bogusIdentity), 0o600)
	script := `
"$KC_ENV_BIN" get prod K >/dev/null || exit 10            # decrypts and fills the session
cp "$KC_ENV_TEST_HOME/bogus" "$KC_ENV_TEST_HOME/identity.txt"   # main key gone
[ "$("$KC_ENV_BIN" get prod K)" = REAL ] || exit 11       # served by the session
"$KC_ENV_BIN" get other T >/dev/null 2>&1 && exit 12      # outside --only
"$KC_ENV_BIN" exec prod -- /bin/sh -c '[ "$K" = REAL ]' || exit 13
touch "$KC_ENV_TEST_HOME/ready"
i=0; while [ ! -e "$KC_ENV_TEST_HOME/go" ] && [ $i -lt 100 ]; do sleep 0.1; i=$((i+1)); done
[ "$("$KC_ENV_BIN" get prod K)" = REAL ] || exit 14       # the outsider changed nothing
(sleep 0.3; "$KC_ENV_BIN" get prod K > "$KC_ENV_TEST_HOME/late" 2>/dev/null) &   # outlives the command
exit 0`
	done := make(chan int, 1)
	var sessErr string
	go func() {
		_, e, code := kc(t, a, nil, "", "session", "--only", "prod", "--ttl", "1m", "--", "/bin/sh", "-c", script)
		sessErr = e
		done <- code
	}()
	deadline := time.Now().Add(15 * time.Second)
	for !exists(filepath.Join(a.home, "ready")) {
		if time.Now().After(deadline) {
			t.Fatalf("session did not get ready: %d %s", <-done, sessErr)
		}
		time.Sleep(50 * time.Millisecond)
	}
	socks, _ := filepath.Glob(filepath.Join(a.runDir, "*.sock"))
	if len(socks) != 1 {
		t.Fatalf("sockets: %v", socks)
	}
	id := strings.TrimSuffix(filepath.Base(socks[0]), ".sock")
	// Same user, right session ID, but not started under the session's command.
	if out, _, code := kc(t, a, []string{"KC_ENV_SESSION=" + id}, "", "get", "prod", "K"); code == 0 {
		t.Errorf("an outside process read the secret from the session: %q", out)
	}
	// The same, speaking to the agent directly instead of through kc-env.
	for _, rq := range []request{{Op: "acquire", Profile: "prod"},
		{Op: "put", Profile: "prod", Sum: strings.Repeat("0", 64), Data: []byte("K=eA==\n")}} {
		if rs, err := ask(socks[0], rq, false); err != nil || rs.OK || rs.Hit || len(rs.Data) != 0 {
			t.Errorf("%s sent straight to the agent from outside: %+v %v", rq.Op, rs, err)
		}
	}
	if out, _, _ := kc(t, a, []string{"KC_ENV_SESSION=" + id}, "", "cache"); !strings.Contains(out, "outside") {
		t.Errorf("an outside process was told it is in the session: %q", out)
	}
	if out, _, _ := kc(t, a, nil, "", "cache", "list"); !strings.Contains(out, id[:8]) || !strings.Contains(out, "prod") {
		t.Errorf("cache list: %q", out)
	}
	if out, _, code := kc(t, a, nil, "", "__complete", "sessions"); code != 0 || !strings.HasPrefix(out, id[:8]+":") {
		t.Errorf("__complete sessions: %q", out)
	}
	os.WriteFile(filepath.Join(a.home, "go"), nil, 0o600)
	if code := <-done; code != 0 {
		t.Fatalf("session script failed at step %d: %s", code, sessErr)
	}
	if m, _ := filepath.Glob(filepath.Join(a.runDir, "*")); len(m) != 0 {
		t.Errorf("left in run/: %v", m)
	}
	// A background process that outlived the command gets nothing: the cache
	// went away with the session.
	time.Sleep(600 * time.Millisecond)
	if b, _ := os.ReadFile(filepath.Join(a.home, "late")); len(b) != 0 {
		t.Errorf("a leftover process still read the secret: %q", b)
	}
}

// The TTL is enforced by the agent: there is no file a process could edit.
func TestSessionTTLEndToEnd(t *testing.T) {
	a := testApp(t)
	kc(t, a, nil, "REAL", "set", "prod", "K")
	os.WriteFile(filepath.Join(a.home, "bogus"), []byte(bogusIdentity), 0o600)
	script := `
"$KC_ENV_BIN" get prod K >/dev/null || exit 10
cp "$KC_ENV_TEST_HOME/bogus" "$KC_ENV_TEST_HOME/identity.txt"
"$KC_ENV_BIN" get prod K >/dev/null || exit 11
sleep 2.5
"$KC_ENV_BIN" get prod K >/dev/null 2>&1 && exit 12
exit 0`
	if _, e, code := kc(t, a, nil, "", "session", "--ttl", "2s", "--", "/bin/sh", "-c", script); code != 0 {
		t.Fatalf("step %d: %s", code, e)
	}
}

func TestCLIRefusals(t *testing.T) {
	a := testApp(t)
	if _, e, code := kc(t, a, nil, "x", "set", "prod", "DYLD_INSERT_LIBRARIES"); code == 0 || !strings.Contains(e, "refused") {
		t.Errorf("set of an unsafe key: %d %q", code, e)
	}
	if _, e, code := kc(t, a, nil, "a\x00b", "set", "prod", "K"); code == 0 || !strings.Contains(e, "NUL") {
		t.Errorf("set of a NUL value: %d %q", code, e)
	}
	if _, e, code := kc(t, a, nil, "", "session", "--ttl", "999999999999999d", "--", "/usr/bin/true"); code == 0 || !strings.Contains(e, "--ttl") {
		t.Errorf("huge ttl: %d %q", code, e)
	}
	kc(t, a, nil, "v", "set", "prod", "K")
	breakMainKey(a) // rm must need the main key
	if _, _, code := kc(t, a, nil, "", "rm", "prod", "K"); code == 0 {
		t.Error("rm worked without the main key")
	}
	if out, _, _ := kc(t, a, nil, "", "ls", "prod"); strings.TrimSpace(out) != "K" {
		t.Errorf("keys after refused rm: %q", out)
	}
}

// ---- public values -------------------------------------------------------------

func TestPublicRoundTrip(t *testing.T) {
	kv := map[string]string{"URL": "https://example.com/a?b=c", "TOKEN": "s3cret", "USER": "日本語 name"}
	pub := map[string]bool{"URL": true, "USER": true}
	plain := serializeProfile(kv, pub)
	if want := "TOKEN=czNjcmV0\n+URL="; !strings.HasPrefix(string(plain), want) {
		t.Fatalf("serialized: %q", plain)
	}
	gotKV, gotPub, err := parseProfileFull(plain, false)
	if err != nil || !reflect.DeepEqual(gotKV, kv) || !reflect.DeepEqual(gotPub, pub) {
		t.Fatalf("round trip: %v %v %v", gotKV, gotPub, err)
	}
	// The copy holds the public values only, and nothing marks them.
	cp := string(publicCopyOf(kv, pub))
	if strings.Contains(cp, "TOKEN") || strings.Contains(cp, publicMark) || strings.Count(cp, "\n") != 2 {
		t.Fatalf("copy: %q", cp)
	}
	if len(publicCopyOf(kv, nil)) != 0 {
		t.Fatal("a profile without public keys has a non-empty copy")
	}
	// A profile without public keys is byte-for-byte the format of kc-env 0.5.
	if string(serializeProfile(kv, nil)) != string(serialize(kv)) {
		t.Fatal("format changed for profiles without public keys")
	}
	// Refused names stay refused when marked public.
	if _, _, err := parseProfileFull([]byte("+PATH=dg==\n"), false); err == nil {
		t.Error("a public PATH was accepted")
	}
}

func TestPublicValuesSaveLoadList(t *testing.T) {
	a := testApp(t)
	kv := map[string]string{"URL": "https://example.com", "TOKEN": "s3cret"}
	if err := a.saveProfile("work", kv, map[string]bool{"URL": true}); err != nil {
		t.Fatal(err)
	}
	cp, _ := os.ReadFile(a.publicFile("work"))
	if string(cp) != "URL=aHR0cHM6Ly9leGFtcGxlLmNvbQ==\n" {
		t.Fatalf("copy on disk: %q", cp)
	}
	if fi, _ := os.Stat(a.publicFile("work")); fi.Mode().Perm() != 0o600 {
		t.Errorf("copy mode: %v", fi.Mode())
	}
	ct, _ := os.ReadFile(a.profileFile("work"))
	if strings.Contains(string(ct), "example") || strings.Contains(string(cp), "s3cret") {
		t.Fatal("the ciphertext shows a value, or the copy holds a secret")
	}
	gotKV, gotPub, err := a.loadProfile("work")
	if err != nil || !reflect.DeepEqual(gotKV, kv) || !reflect.DeepEqual(gotPub, map[string]bool{"URL": true}) {
		t.Fatalf("load: %v %v %v", gotKV, gotPub, err)
	}
	if got := a.publicValues("work"); !reflect.DeepEqual(got, map[string]string{"URL": "https://example.com"}) {
		t.Fatalf("publicValues: %v", got)
	}
	// Removing the last public value removes the copy.
	delete(kv, "URL")
	if err := a.saveProfile("work", kv, map[string]bool{"URL": true}); err != nil {
		t.Fatal(err)
	}
	if exists(a.publicFile("work")) {
		t.Fatal("copy left behind")
	}
	if _, _, err := a.loadProfile("work"); err != nil {
		t.Fatal(err)
	}
	// A copy with anything unexpected in it is not shown.
	os.WriteFile(a.publicFile("work"), []byte("URL=dg==\n\x1b[31mEVIL\n"), 0o600)
	if got := a.publicValues("work"); got != nil {
		t.Errorf("a malformed copy was shown: %v", got)
	}
	if got := showValue("a\x1b[31mb\nc"); strings.ContainsAny(got, "\x1b\n") {
		t.Errorf("showValue: %q", got)
	}
}

func alerts(a *app) string { b, _ := os.ReadFile(filepath.Join(a.home, "alerts")); return string(b) }

// The copy is display only, but a copy that does not match the encrypted
// profile means something else wrote to the vault: every decryption stops.
func TestPublicCopyTampering(t *testing.T) {
	enc := func(s string) string { return "URL=" + base64Std(s) + "\n" }
	cases := map[string]func(a *app){
		"changed": func(a *app) { os.WriteFile(a.publicFile("work"), []byte(enc("https://evil.example")), 0o600) },
		"removed": func(a *app) { os.Remove(a.publicFile("work")) },
		"emptied": func(a *app) { os.WriteFile(a.publicFile("work"), nil, 0o600) },
		"added": func(a *app) {
			os.WriteFile(a.publicFile("work"), []byte("EXTRA=dg==\n"+enc("https://example.com")), 0o600)
		},
		"garbage":  func(a *app) { os.WriteFile(a.publicFile("work"), []byte("\x00\x01 not a copy"), 0o600) },
		"reformat": func(a *app) { os.WriteFile(a.publicFile("work"), []byte(enc("https://example.com")+"\n"), 0o600) },
		"dir": func(a *app) {
			os.Remove(a.publicFile("work"))
			os.Mkdir(a.publicFile("work"), 0o700)
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			a := testApp(t)
			kv := map[string]string{"URL": "https://example.com", "TOKEN": "s3cret"}
			if err := a.saveProfile("work", kv, map[string]bool{"URL": true}); err != nil {
				t.Fatal(err)
			}
			tamper(a)
			var te *tamperError
			if _, _, err := a.loadProfile("work"); !errors.As(err, &te) || te.profile != "work" {
				t.Fatalf("loadProfile: %v", err)
			}
			if _, _, err := a.loadFreshProfile("work", true); !errors.As(err, &te) {
				t.Fatalf("loadFreshProfile (rm): %v", err)
			}
			if got := alerts(a); strings.Count(got, "work|") != 2 {
				t.Fatalf("alerts: %q", got)
			}
			if report(&tamperError{"work"}) != exitTampered {
				t.Fatal("exit status")
			}
		})
	}
	// A profile without public values, with a copy that claims one.
	a := testApp(t)
	a.save("plain", map[string]string{"TOKEN": "s3cret"})
	os.WriteFile(a.publicFile("plain"), []byte(enc("https://evil.example")), 0o600)
	var te *tamperError
	if _, err := a.load("plain"); !errors.As(err, &te) {
		t.Fatalf("stray copy: %v", err)
	}
}

func base64Std(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// End to end: nothing runs, nothing is printed, the exit status is 3, and
// `ls --verify` shows the difference and repairs the copy.
func TestPublicTamperingCLI(t *testing.T) {
	a := testApp(t)
	if _, e, code := kc(t, a, nil, "s3cret", "set", "work", "TOKEN"); code != 0 {
		t.Fatalf("set: %s", e)
	}
	if _, e, code := kc(t, a, nil, "", "set", "--public", "work", "URL", "https://example.com"); code != 0 || !strings.Contains(e, "public") {
		t.Fatalf("set --public: %d %s", code, e)
	}
	if out, _, _ := kc(t, a, nil, "", "ls", "work"); out != "TOKEN\nURL=https://example.com\n" {
		t.Fatalf("ls: %q", out)
	}
	if out, _, _ := kc(t, a, nil, "", "list"); !strings.Contains(out, "URL = https://example.com") || strings.Contains(out, "s3cret") {
		t.Fatalf("list: %q", out)
	}
	if out, _, code := kc(t, a, nil, "", "exec", "work", "--", "/bin/sh", "-c", `printf %s "$URL|$TOKEN"`); code != 0 || out != "https://example.com|s3cret" {
		t.Fatalf("exec: %d %q", code, out)
	}

	os.WriteFile(a.publicFile("work"), []byte("URL="+base64Std("https://evil.example")+"\n"), 0o600)
	marker := filepath.Join(a.home, "ran")
	for _, args := range [][]string{
		{"exec", "work", "--", "/usr/bin/touch", marker},
		{"get", "work", "TOKEN"},
		{"get", "work", "URL"},
		{"header", "work", "TOKEN"},
		{"rm", "work", "TOKEN"},
	} {
		out, e, code := kc(t, a, nil, "", args...)
		if code != exitTampered || out != "" || !strings.Contains(e, "WARNING") || !strings.Contains(e, "URL") ||
			!strings.Contains(e, "ls --verify work") || strings.Contains(e, "s3cret") {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q", args, code, out, e)
		}
	}
	if _, e, code := kc(t, a, nil, "new", "set", "work", "OTHER"); code != exitTampered {
		t.Errorf("set on a tampered profile: %d %s", code, e)
	}
	if exists(marker) {
		t.Fatal("the command ran although the copy was tampered with")
	}
	if got := alerts(a); strings.Count(got, "work|") != 6 || !strings.Contains(got, "URL") {
		t.Fatalf("alerts: %q", got)
	}
	// Still listed (from the copy, unverified) — and still stopped at the next use.
	if out, _, _ := kc(t, a, nil, "", "ls", "work"); !strings.Contains(out, "URL=https://evil.example") {
		t.Fatalf("ls shows the copy: %q", out)
	}

	out, e, code := kc(t, a, nil, "", "ls", "--verify", "work")
	if code != 0 || out != "TOKEN\nURL=https://example.com\n" ||
		!strings.Contains(e, "https://evil.example") || !strings.Contains(e, "https://example.com") {
		t.Fatalf("ls --verify: %d %q %q", code, out, e)
	}
	if out, _, code := kc(t, a, nil, "", "exec", "work", "--", "/bin/sh", "-c", `printf %s "$URL"`); code != 0 || out != "https://example.com" {
		t.Fatalf("exec after repair: %d %q", code, out)
	}
	if _, e, _ := kc(t, a, nil, "", "ls", "--verify", "work"); strings.Contains(e, "did not match") {
		t.Errorf("a second ls --verify still reports a difference: %q", e)
	}
}

func TestSetPublicCLI(t *testing.T) {
	a := testApp(t)
	// A secret is never accepted on the command line.
	if _, e, code := kc(t, a, nil, "", "set", "work", "TOKEN", "on-the-command-line"); code == 0 || !strings.Contains(e, "usage") {
		t.Errorf("secret as an argument: %d %q", code, e)
	}
	for _, bad := range []string{"two\nlines", "bell\a", ""} {
		if _, _, code := kc(t, a, nil, "", "set", "--public", "work", "URL", bad); code == 0 {
			t.Errorf("public value %q was accepted", bad)
		}
	}
	if _, e, code := kc(t, a, nil, "", "set", "--public", "work", "PATH", "/x"); code == 0 || !strings.Contains(e, "refused") {
		t.Errorf("public PATH: %d %q", code, e)
	}
	if exists(a.profileFile("work")) {
		t.Fatal("a refused set created the profile")
	}
	// From standard input, like a secret.
	if _, e, code := kc(t, a, nil, "eu-west-1", "set", "--public", "work", "REGION"); code != 0 {
		t.Fatalf("set --public from stdin: %s", e)
	}
	kc(t, a, nil, "", "set", "--public", "work", "URL", "https://example.com")
	kc(t, a, nil, "tok", "set", "work", "TOKEN")
	if out, _, _ := kc(t, a, nil, "", "ls", "work"); out != "REGION=eu-west-1\nTOKEN\nURL=https://example.com\n" {
		t.Fatalf("ls: %q", out)
	}
	// Setting it again without --public makes it a secret: gone from the copy.
	kc(t, a, nil, "now-secret", "set", "work", "REGION")
	cp, _ := os.ReadFile(a.publicFile("work"))
	if out, _, _ := kc(t, a, nil, "", "ls", "work"); out != "REGION\nTOKEN\nURL=https://example.com\n" || strings.Contains(string(cp), "REGION") {
		t.Fatalf("after re-set as a secret: %q / %q", out, cp)
	}
	// rm of a public key updates the copy; rm --all and rm-profile remove it.
	if _, e, code := kc(t, a, nil, "", "rm", "work", "URL"); code != 0 {
		t.Fatalf("rm: %s", e)
	}
	if exists(a.publicFile("work")) {
		t.Fatal("copy left after the last public key was removed")
	}
	if _, _, err := a.loadProfile("work"); err != nil {
		t.Fatalf("load after rm: %v", err)
	}
}

// Inside a session a cached profile is checked against the copy on every
// use, and a save by kc-env is never mistaken for tampering.
func TestPublicValuesInSession(t *testing.T) {
	a := testApp(t)
	kv := map[string]string{"URL": "https://example.com", "TOKEN": "s3cret"}
	pub := map[string]bool{"URL": true}
	if err := a.saveProfile("work", kv, pub); err != nil {
		t.Fatal(err)
	}
	ag := startAgent(t, a, time.Minute, nil, os.Getpid())
	t.Setenv("KC_ENV_SESSION", ag.id)
	if _, _, err := fresh(a).loadProfile("work"); err != nil {
		t.Fatal(err)
	}
	main, _ := os.ReadFile(a.identityPath)
	breakMainKey(a) // from here only the session can answer
	if got, gotPub, err := fresh(a).loadProfile("work"); err != nil || got["URL"] != "https://example.com" || !gotPub["URL"] {
		t.Fatalf("cached: %v %v %v", got, gotPub, err)
	}
	good, _ := os.ReadFile(a.publicFile("work"))
	os.WriteFile(a.publicFile("work"), []byte("URL="+base64Std("https://evil.example")+"\n"), 0o600)
	var te *tamperError
	if _, _, err := fresh(a).loadProfile("work"); !errors.As(err, &te) {
		t.Fatalf("tampered copy with a cached profile: %v", err)
	}
	os.WriteFile(a.publicFile("work"), good, 0o600)
	if _, _, err := fresh(a).loadProfile("work"); err != nil {
		t.Fatalf("restored copy: %v", err)
	}
	// A save through kc-env changes the vault file, the copy and the cache together.
	os.WriteFile(a.identityPath, main, 0o600)
	kv["URL"] = "https://example.org"
	if err := fresh(a).saveProfile("work", kv, pub); err != nil {
		t.Fatal(err)
	}
	breakMainKey(a)
	if got, _, err := fresh(a).loadProfile("work"); err != nil || got["URL"] != "https://example.org" {
		t.Fatalf("after save: %v %v", got, err)
	}
	if alerts(a) == "" || strings.Count(alerts(a), "work|") != 1 {
		t.Fatalf("alerts: %q", alerts(a))
	}
}

// Readers and a writer at the same time: a reader never sees the new
// ciphertext with the old copy (or the reverse), so it never cries tampering.
func TestPublicCopyNoFalseAlarm(t *testing.T) {
	a := testApp(t)
	pub := map[string]bool{"URL": true}
	if err := a.saveProfile("work", map[string]string{"URL": "v0"}, pub); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	errs := make(chan error, 8)
	for i := 0; i < 4; i++ {
		go func() {
			r := appAt(a.home)
			for {
				select {
				case <-stop:
					errs <- nil
					return
				default:
				}
				if _, _, err := r.loadProfile("work"); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	w := appAt(a.home)
	for i := 1; i <= 60; i++ {
		unlock, err := w.lockProfile("work")
		if err != nil {
			t.Fatal(err)
		}
		err = w.saveProfile("work", map[string]string{"URL": fmt.Sprintf("v%d", i)}, pub)
		unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	for i := 0; i < 4; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("reader: %v", err)
		}
	}
	if got := alerts(a); got != "" {
		t.Fatalf("false alarm: %q", got)
	}
}
