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
	"io/fs"
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

// publicMark starts the line of a key whose value is not a secret (set with
// `set --public`). The mark lives inside the encrypted profile, so which keys
// are public, and their values, are covered by the same encryption as the
// secrets.
const publicMark = "+"

func serialize(kv map[string]string) []byte { return serializeProfile(kv, nil) }

func serializeProfile(kv map[string]string, pub map[string]bool) []byte {
	var b strings.Builder
	for _, k := range sortedKeys(kv) {
		if pub[k] {
			b.WriteString(publicMark)
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(base64.StdEncoding.EncodeToString([]byte(kv[k])))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func parseProfile(plain []byte) (map[string]string, error) { return parseProfileOpt(plain, false) }

func parseProfileOpt(plain []byte, allowUnsafe bool) (map[string]string, error) {
	kv, _, err := parseProfileFull(plain, allowUnsafe)
	return kv, err
}

// parseProfileFull parses a decrypted profile into its values and the set of
// public keys. allowUnsafe keeps refused key names, which `rm` needs in order
// to remove them.
func parseProfileFull(plain []byte, allowUnsafe bool) (map[string]string, map[string]bool, error) {
	kv, pub := map[string]string{}, map[string]bool{}
	for _, l := range strings.Split(string(plain), "\n") {
		if l == "" {
			continue
		}
		isPublic := strings.HasPrefix(l, publicMark)
		l = strings.TrimPrefix(l, publicMark)
		k, v, ok := strings.Cut(l, "=")
		if !ok || !reKey.MatchString(k) {
			return nil, nil, errors.New("malformed line")
		}
		if !allowUnsafe && unsafeKey(k) {
			return nil, nil, &unsafeKeyError{k}
		}
		if _, dup := kv[k]; dup {
			return nil, nil, errors.New("duplicate key")
		}
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, nil, err
		}
		if bytes.IndexByte(dec, 0) >= 0 {
			return nil, nil, errors.New("NUL in value")
		}
		kv[k] = string(dec)
		if isPublic {
			pub[k] = true
		}
	}
	return kv, pub, nil
}

// publicCopyOf is what vault/<profile>.public must contain for a profile: the
// public keys and their values, in the same line format, sorted. Empty when
// the profile has no public key (then the file does not exist).
func publicCopyOf(kv map[string]string, pub map[string]bool) []byte {
	sub := map[string]string{}
	for k, v := range kv {
		if pub[k] {
			sub[k] = v
		}
	}
	return serialize(sub)
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

// loaded is a decrypted profile together with the plaintext copy of its
// public values as it was on disk at the same moment.
type loaded struct {
	kv      map[string]string
	pub     map[string]bool
	pubCopy []byte
}

const maxPublicCopy = 1 << 20

// snapshot reads the ciphertext of a profile and the plaintext copy of its
// public values as one consistent pair. A writer replaces both while it holds
// the profile lock, so a reader takes the lock shared (unless this process is
// that writer).
func (a *app) snapshot(p string) (ct, pubCopy []byte, err error) {
	if !a.holdsLock(p) {
		if f, err := os.OpenFile(a.lockFile(p), os.O_CREATE|os.O_RDWR, 0o600); err == nil {
			defer f.Close()
			_ = syscall.Flock(int(f.Fd()), syscall.LOCK_SH)
		}
	}
	if ct, err = os.ReadFile(a.profileFile(p)); err != nil {
		return nil, nil, err
	}
	f, err := os.Open(a.publicFile(p))
	if errors.Is(err, fs.ErrNotExist) {
		return ct, nil, nil
	}
	// A copy that cannot be read, or is absurdly large, never matches.
	unreadable := []byte{0}
	if err != nil {
		return ct, unreadable, nil
	}
	defer f.Close()
	pubCopy, err = io.ReadAll(io.LimitReader(f, maxPublicCopy+1))
	if err != nil || len(pubCopy) > maxPublicCopy {
		return ct, unreadable, nil
	}
	return ct, pubCopy, nil
}

// decrypt decrypts a profile's ciphertext with the Secure Enclave key (Touch ID).
func (a *app) decrypt(ct []byte) ([]byte, error) {
	id, err := a.identity()
	if err != nil {
		return nil, err
	}
	plain, err := decryptBytes(ct, id)
	if err != nil {
		return nil, a.fail("err_decrypt_failed")
	}
	return plain, nil
}

func (a *app) parseLoaded(p string, plain, pubCopy []byte, allowUnsafe bool) (*loaded, error) {
	kv, pub, err := parseProfileFull(plain, allowUnsafe)
	var uk *unsafeKeyError
	if errors.As(err, &uk) {
		return nil, a.fail("err_unsafe_key_in_profile", "Name", p, "Key", uk.key)
	}
	if err != nil {
		return nil, a.fail("err_corrupt_profile", "Name", p)
	}
	return &loaded{kv: kv, pub: pub, pubCopy: pubCopy}, nil
}

// loadRaw decrypts a profile (Touch ID unless useSession and the session
// holds it) without judging the copy of the public values. Everything except
// `ls --verify` goes through loadProfile / loadFreshProfile, which do.
func (a *app) loadRaw(p string, useSession, allowUnsafe bool) (*loaded, error) {
	if !exists(a.profileFile(p)) {
		return &loaded{kv: map[string]string{}, pub: map[string]bool{}}, nil
	}
	if s := a.activeSession(); useSession && s != nil {
		for try := 0; try < 3; try++ {
			// A miss leaves this process holding the profile's lock in the
			// session until the lease is closed, so parallel callers share
			// one Touch ID.
			l := s.acquire(p)
			if l == nil {
				break
			}
			ct, pubCopy, err := a.snapshot(p)
			if err != nil {
				l.close()
				return nil, err
			}
			if l.hit {
				if l.sum == "" { // an agent of kc-env 0.5: it does not say what it cached
					l.close()
					break
				}
				if l.sum != sumHex(ct) { // the vault changed after the agent answered: ask again
					l.close()
					continue
				}
				ld, err := a.parseLoaded(p, l.data, pubCopy, allowUnsafe)
				l.close()
				return ld, err
			}
			plain, err := a.decrypt(ct)
			if err != nil {
				l.close()
				return nil, err
			}
			l.put(sumHex(ct), plain)
			l.close()
			return a.parseLoaded(p, plain, pubCopy, allowUnsafe)
		}
	}
	ct, pubCopy, err := a.snapshot(p)
	if err != nil {
		return nil, err
	}
	plain, err := a.decrypt(ct)
	if err != nil {
		return nil, err
	}
	return a.parseLoaded(p, plain, pubCopy, allowUnsafe)
}

// publicDiff lists the keys whose public value in the copy differs from the
// encrypted profile (missing on either side included). Empty means the copy
// is exactly what it should be.
func publicDiff(l *loaded) []string {
	if bytes.Equal(l.pubCopy, publicCopyOf(l.kv, l.pub)) {
		return nil
	}
	shown, _, err := parseProfileFull(l.pubCopy, true)
	if err != nil {
		shown = nil
	}
	seen := map[string]bool{}
	for k := range l.pub {
		if v, ok := shown[k]; !ok || v != l.kv[k] {
			seen[k] = true
		}
	}
	for k := range shown {
		if !l.pub[k] {
			seen[k] = true
		}
	}
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) == 0 { // same values, but not the bytes kc-env writes
		keys = []string{"?"}
	}
	return keys
}

// tamperError means the plaintext copy of a profile's public values does not
// match the encrypted profile. It has already been reported (tampered).
type tamperError struct{ profile string }

func (e *tamperError) Error() string {
	return "public values of " + e.profile + " were changed outside kc-env"
}

// tampered tells the user, on stderr and in a dialog, and stops the command.
func (a *app) tampered(p string, keys []string) error {
	names := strings.Join(keys, ", ")
	fmt.Fprintln(os.Stderr, a.t.S("alert_tampered", "Name", p, "Keys", names, "Path", a.publicFile(p)))
	alertDialog(a, p, a.t.S("alert_tampered_title"), a.t.S("alert_tampered_dialog", "Name", p, "Keys", names))
	return &tamperError{p}
}

// checked refuses a profile whose public copy was changed outside kc-env.
func (a *app) checked(p string, l *loaded, err error) (map[string]string, map[string]bool, error) {
	if err != nil {
		return nil, nil, err
	}
	if keys := publicDiff(l); len(keys) > 0 {
		return nil, nil, a.tampered(p, keys)
	}
	return l.kv, l.pub, nil
}

// loadProfile returns a profile's values and which of them are public.
// Touch ID unless the session holds the profile.
func (a *app) loadProfile(p string) (map[string]string, map[string]bool, error) {
	l, err := a.loadRaw(p, true, false)
	return a.checked(p, l, err)
}

// loadFreshProfile always decrypts with the Secure Enclave key, never from a session.
func (a *app) loadFreshProfile(p string, allowUnsafe bool) (map[string]string, map[string]bool, error) {
	l, err := a.loadRaw(p, false, allowUnsafe)
	return a.checked(p, l, err)
}

func (a *app) load(p string) (map[string]string, error) {
	kv, _, err := a.loadProfile(p)
	return kv, err
}

func (a *app) loadFresh(p string, allowUnsafe bool) (map[string]string, error) {
	kv, _, err := a.loadFreshProfile(p, allowUnsafe)
	return kv, err
}

// save encrypts a profile without public values.
func (a *app) save(p string, kv map[string]string) error { return a.saveProfile(p, kv, nil) }

// saveProfile encrypts kv to the profile file (public key only: no Touch ID)
// and rewrites the plaintext copy of the public values and the key index.
// Callers that modify an existing profile hold lockProfile.
func (a *app) saveProfile(p string, kv map[string]string, pub map[string]bool) error {
	r, err := a.recipient()
	if err != nil {
		return err
	}
	plain := serializeProfile(kv, pub)
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
	if err := a.writePublicCopy(p, publicCopyOf(kv, pub)); err != nil {
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

// writePublicCopy writes vault/<profile>.public, or removes it when the
// profile has no public value.
func (a *app) writePublicCopy(p string, data []byte) error {
	if len(data) == 0 {
		if err := os.Remove(a.publicFile(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	return writeAtomic(a.publicFile(p), data)
}

// publicValues reads the plaintext copy for display (list, ls). It is not
// authenticated: commands never take a value from it, and a copy with
// anything unexpected in it is not shown at all.
func (a *app) publicValues(p string) map[string]string {
	b, err := os.ReadFile(a.publicFile(p))
	if err != nil || len(b) > maxPublicCopy {
		return nil
	}
	kv, pub, err := parseProfileFull(b, true)
	if err != nil || len(pub) != 0 {
		return nil
	}
	return kv
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
	a.lockMu.Lock()
	if a.locked == nil {
		a.locked = map[string]bool{}
	}
	a.locked[p] = true
	a.lockMu.Unlock()
	return func() {
		a.lockMu.Lock()
		delete(a.locked, p)
		a.lockMu.Unlock()
		f.Close() // closing releases the lock; so does exiting
	}, nil
}

func (a *app) holdsLock(p string) bool {
	a.lockMu.Lock()
	defer a.lockMu.Unlock()
	return a.locked[p]
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
