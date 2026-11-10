package main

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"filippo.io/age"
	"filippo.io/age/plugin"
)

// ---- age identity / recipient ------------------------------------------------
// identity.txt holds exactly one AGE-PLUGIN-SE-1... line (the Secure Enclave
// key). The public key is derived from it every time; no separate file is
// trusted for encryption.

const (
	seIdentityPrefix  = "AGE-PLUGIN-SE-"
	seRecipientPrefix = "age1se1"
	softwarePrefix    = "AGE-SECRET-KEY-"
)

// softwareKeys lets the unit tests run without a Secure Enclave by accepting
// a plain X25519 key in identity.txt. Nothing in the program sets it.
var softwareKeys bool

func (a *app) pluginUI() *plugin.ClientUI {
	return &plugin.ClientUI{
		DisplayMessage: func(name, msg string) error {
			fmt.Fprintf(os.Stderr, "age-plugin-%s: %s\n", name, msg)
			return nil
		},
		RequestValue: func(name, prompt string, secret bool) (string, error) {
			return "", errors.New(a.t.S("err_plugin_input", "Prompt", prompt))
		},
		Confirm: func(name, prompt, yes, no string) (bool, error) {
			return false, errors.New(a.t.S("err_plugin_input", "Prompt", prompt))
		},
		WaitTimer: func(name string) { fmt.Fprintln(os.Stderr, a.t.S("msg_waiting_auth")) },
	}
}

func dataLines(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// identityLine returns the single key line of identity.txt.
func (a *app) identityLine() (string, error) {
	b, err := os.ReadFile(a.identityPath)
	if err != nil {
		return "", err
	}
	lines := dataLines(b)
	if len(lines) == 1 {
		l := lines[0]
		if strings.HasPrefix(l, seIdentityPrefix) || (softwareKeys && strings.HasPrefix(l, softwarePrefix)) {
			return l, nil
		}
	}
	return "", a.fail("err_bad_identity", "Path", a.identityPath)
}

func (a *app) parseIdentity(l string) (age.Identity, error) {
	if strings.HasPrefix(l, seIdentityPrefix) {
		return plugin.NewIdentity(l, a.pluginUI())
	}
	return age.ParseX25519Identity(l)
}

func (a *app) identity() (age.Identity, error) {
	l, err := a.identityLine()
	if err != nil {
		return nil, err
	}
	return a.parseIdentity(l)
}

// deriveRecipient computes the public key that belongs to an identity line.
// For a Secure Enclave key this asks the plugin (no Touch ID is needed).
func (a *app) deriveRecipient(l string) (age.Recipient, error) {
	if !strings.HasPrefix(l, seIdentityPrefix) {
		id, err := age.ParseX25519Identity(l)
		if err != nil {
			return nil, err
		}
		return id.Recipient(), nil
	}
	plug := a.pluginPath()
	if plug == "" {
		return nil, a.fail("err_plugin_missing")
	}
	cmd := exec.Command(plug, "recipients")
	cmd.Stdin = strings.NewReader(l + "\n")
	out, err := cmd.Output()
	if err != nil {
		return nil, a.fail("err_bad_identity", "Path", a.identityPath)
	}
	lines := dataLines(out)
	if len(lines) != 1 || !strings.HasPrefix(lines[0], seRecipientPrefix) {
		return nil, a.fail("err_bad_identity", "Path", a.identityPath)
	}
	return plugin.NewRecipient(lines[0], a.pluginUI())
}

func (a *app) recipient() (age.Recipient, error) {
	l, err := a.identityLine()
	if err != nil {
		return nil, err
	}
	return a.deriveRecipient(l)
}

// ---- generic age helpers -----------------------------------------------------

func encryptBytes(plain []byte, rs ...age.Recipient) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, rs...)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decryptBytes(ct []byte, ids ...age.Identity) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ct), ids...)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

// writeAtomic writes data to path via a temporary file in the same directory.
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func sumHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func fileSHA256(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return sumHex(b), nil
}

func wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// ---- profile format: one "KEY=base64(value)" line per key ---------------------

// Names that change how a command is loaded or started. A profile is handed
// to commands as environment variables, so these are refused both when a key
// is set and when a profile is read (a profile file can be replaced by anyone
// who can write to the vault: age has no sender authentication). The list is
// a safety net, not a complete catalogue.
var (
	unsafeKeyNames = map[string]bool{
		"PATH": true, "IFS": true, "ENV": true, "BASH_ENV": true, "ZDOTDIR": true, "HOME": true,
		"SHELL": true, "AGEDEBUG": true, "NODE_OPTIONS": true, "PYTHONSTARTUP": true,
		"PYTHONPATH": true, "PERL5OPT": true, "RUBYOPT": true, "GIT_SSH_COMMAND": true,
	}
	unsafeKeyPrefixes = []string{"DYLD_", "LD_", "KC_ENV_"}
)

func unsafeKey(k string) bool {
	if unsafeKeyNames[k] {
		return true
	}
	for _, p := range unsafeKeyPrefixes {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// unsafeKeyError reports a refused key name found inside a profile.
type unsafeKeyError struct{ key string }

func (e *unsafeKeyError) Error() string { return "unsafe key name: " + e.key }

func serialize(kv map[string]string) []byte {
	var b strings.Builder
	for _, k := range sortedKeys(kv) {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(base64.StdEncoding.EncodeToString([]byte(kv[k])))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func parseProfile(plain []byte) (map[string]string, error) { return parseProfileOpt(plain, false) }

// parseProfileOpt parses a decrypted profile. allowUnsafe keeps refused key
// names, which `rm` needs in order to remove them.
func parseProfileOpt(plain []byte, allowUnsafe bool) (map[string]string, error) {
	kv := map[string]string{}
	for _, l := range strings.Split(string(plain), "\n") {
		if l == "" {
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || !reKey.MatchString(k) {
			return nil, errors.New("malformed line")
		}
		if !allowUnsafe && unsafeKey(k) {
			return nil, &unsafeKeyError{k}
		}
		if _, dup := kv[k]; dup {
			return nil, errors.New("duplicate key")
		}
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, err
		}
		if bytes.IndexByte(dec, 0) >= 0 {
			return nil, errors.New("NUL in value")
		}
		kv[k] = string(dec)
	}
	return kv, nil
}

func sortedKeys(kv map[string]string) []string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---- load / save -------------------------------------------------------------

// decryptVault decrypts a profile with the Secure Enclave key (Touch ID). It
// also returns the SHA-256 of the ciphertext it decrypted.
func (a *app) decryptVault(p string) (plain []byte, sum string, err error) {
	ct, err := os.ReadFile(a.profileFile(p))
	if err != nil {
		return nil, "", err
	}
	id, err := a.identity()
	if err != nil {
		return nil, "", err
	}
	plain, err = decryptBytes(ct, id)
	if err != nil {
		return nil, "", a.fail("err_decrypt_failed")
	}
	return plain, sumHex(ct), nil
}

// load returns a profile's keys. Touch ID unless the session holds the profile.
func (a *app) load(p string) (map[string]string, error) {
	if !exists(a.profileFile(p)) {
		return map[string]string{}, nil
	}
	if s := a.activeSession(); s != nil {
		// A miss leaves this process holding the profile's lock in the session
		// until the lease is closed, so parallel callers share one Touch ID.
		if l := s.acquire(p); l != nil {
			defer l.close()
			if l.hit {
				return a.parse(p, l.data)
			}
			plain, sum, err := a.decryptVault(p)
			if err != nil {
				return nil, err
			}
			l.put(sum, plain)
			return a.parse(p, plain)
		}
	}
	return a.loadFresh(p, false)
}

// loadFresh always decrypts with the Secure Enclave key, never from a session.
func (a *app) loadFresh(p string, allowUnsafe bool) (map[string]string, error) {
	plain, _, err := a.decryptVault(p)
	if err != nil {
		return nil, err
	}
	return a.parseOpt(p, plain, allowUnsafe)
}

func (a *app) parse(p string, plain []byte) (map[string]string, error) {
	return a.parseOpt(p, plain, false)
}

func (a *app) parseOpt(p string, plain []byte, allowUnsafe bool) (map[string]string, error) {
	kv, err := parseProfileOpt(plain, allowUnsafe)
	var uk *unsafeKeyError
	if errors.As(err, &uk) {
		return nil, a.fail("err_unsafe_key_in_profile", "Name", p, "Key", uk.key)
	}
	if err != nil {
		return nil, a.fail("err_corrupt_profile", "Name", p)
	}
	return kv, nil
}

// save encrypts kv to the profile file (public key only: no Touch ID).
func (a *app) save(p string, kv map[string]string) error {
	r, err := a.recipient()
	if err != nil {
		return err
	}
	plain := serialize(kv)
	ct, err := encryptBytes(plain, r)
	if err != nil {
		return a.fail("err_encrypt_failed")
	}
	if err := os.MkdirAll(a.vault, 0o700); err != nil {
		return err
	}
	if err := writeAtomic(a.profileFile(p), ct); err != nil {
		return err
	}
	if err := a.writeIndex(p, sortedKeys(kv)); err != nil {
		return err
	}
	if s := a.activeSession(); s != nil {
		s.refresh(p, sumHex(ct), plain) // only replaces a profile the session already holds
	}
	return nil
}

// lockProfile serialises read-modify-write of one profile across processes.
func (a *app) lockProfile(p string) (unlock func(), err error) {
	if err := os.MkdirAll(a.vault, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(a.lockFile(p), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil // closing releases the lock; so does exiting
}

// ---- key-name index (plaintext, no values) -----------------------------------

func (a *app) writeIndex(p string, keys []string) error {
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('\n')
	}
	return writeAtomic(a.indexFile(p), []byte(b.String()))
}

// indexKeys returns key names from the index; ok=false if there is no usable
// index. The index is an unauthenticated plaintext file: it is only a hint
// for listing, and anything that is not a key name makes it unusable.
func (a *app) indexKeys(p string) (keys []string, ok bool) {
	b, err := os.ReadFile(a.indexFile(p))
	if err != nil {
		return nil, false
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l == "" {
			continue
		}
		if !reKey.MatchString(l) {
			return nil, false
		}
		keys = append(keys, l)
	}
	return keys, true
}

func (a *app) allProfiles() []string {
	m, _ := filepath.Glob(filepath.Join(a.vault, "*.age"))
	var out []string
	for _, f := range m {
		name := strings.TrimSuffix(filepath.Base(f), ".age")
		if reProfile.MatchString(name) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// authPresence proves presence with the Secure Enclave key itself, never a
// session: encrypt a random challenge to the key and decrypt it.
func (a *app) authPresence() bool {
	l, err := a.identityLine()
	if err != nil {
		return false
	}
	r, err := a.deriveRecipient(l)
	if err != nil {
		return false
	}
	id, err := a.parseIdentity(l)
	if err != nil {
		return false
	}
	challenge := []byte(randomHex(24))
	ct, err := encryptBytes(challenge, r)
	if err != nil {
		return false
	}
	got, err := decryptBytes(ct, id)
	return err == nil && bytes.Equal(got, challenge)
}
