package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"filippo.io/age"
)

// rekey makes a new key and encrypts every profile to it again; then the old
// key is deleted (on Windows the Hello key too). It is for when the key
// should be replaced, after a change to the Windows Hello setup for example
// (docs/design-windows-hello.ja.md, 9).
//
// Order, so that an interruption never loses a profile:
//  1. the new key is written to identity.new.txt
//  2. identity.txt becomes identity.old.txt, identity.new.txt becomes identity.txt
//  3. each profile still on the old key is decrypted with it and encrypted to the new one
//  4. the old key is deleted and identity.old.txt removed
//
// While identity.old.txt exists, every decryption also tries it, and running
// rekey again continues at step 3.

func (a *app) oldIdentityPath() string { return filepath.Join(a.home, "identity.old.txt") }
func (a *app) newIdentityPath() string { return filepath.Join(a.home, "identity.new.txt") }

// newKeyFile creates a key with the plugin and writes its identity to path.
// A variable so the tests can use software keys.
var newKeyFile = func(a *app, path string) error {
	plug := a.pluginPath()
	if plug == "" {
		return a.fail("err_plugin_missing")
	}
	gen := exec.Command(plug, keygenArgs(path)...)
	gen.Stdout, gen.Stderr = os.Stderr, os.Stderr
	if err := gen.Run(); err != nil {
		return a.fail("err_keygen_failed")
	}
	return nil
}

// oldIdentity is the key of an unfinished rekey, or nil.
func (a *app) oldIdentity() (age.Identity, string) {
	l, err := a.identityLineAt(a.oldIdentityPath())
	if err != nil {
		return nil, ""
	}
	id, err := a.parseIdentity(l)
	if err != nil {
		return nil, ""
	}
	return id, l
}

func (a *app) cmdRekey(args []string) error {
	if len(args) != 0 {
		return a.fail("usage_rekey")
	}
	// Interrupted between the two renames of step 2.
	if !exists(a.identityPath) && exists(a.oldIdentityPath()) && exists(a.newIdentityPath()) {
		if err := os.Rename(a.newIdentityPath(), a.identityPath); err != nil {
			return err
		}
	}
	if err := a.requireInit(); err != nil {
		return err
	}
	profiles := a.allProfiles()
	for _, p := range profiles {
		unlock, err := a.lockProfile(p)
		if err != nil {
			return err
		}
		defer unlock()
	}
	e := os.Stderr
	if exists(a.oldIdentityPath()) {
		fmt.Fprintln(e, "se-env: "+a.t.S("msg_rekey_resume", "Path", a.oldIdentityPath()))
	} else {
		fmt.Fprintln(e, "se-env: "+a.t.S("msg_rekey_start", "Profiles", len(profiles)))
		os.Remove(a.newIdentityPath()) // left by a keygen that failed
		if err := newKeyFile(a, a.newIdentityPath()); err != nil {
			os.Remove(a.newIdentityPath())
			return err
		}
		l, err := a.identityLineAt(a.newIdentityPath())
		if err == nil {
			_, err = a.deriveRecipient(l)
		}
		if err != nil {
			os.Remove(a.newIdentityPath())
			return a.fail("err_keygen_failed")
		}
		if err := os.Rename(a.identityPath, a.oldIdentityPath()); err != nil {
			return err
		}
		if err := os.Rename(a.newIdentityPath(), a.identityPath); err != nil {
			return err
		}
	}
	old, oldLine := a.oldIdentity()
	if old == nil {
		return a.fail("err_bad_identity", "Path", a.oldIdentityPath())
	}
	r, err := a.recipient()
	if err != nil {
		return err
	}
	for _, p := range profiles {
		ct, err := os.ReadFile(a.profileFile(p))
		if err != nil {
			return err
		}
		a.want(a.t.S("auth_target_rekey", "Profile", p), a.t.S("auth_do_rekey"))
		var plain []byte
		err = a.withReason(nil, func() (err error) {
			plain, err = decryptBytes(ct, old)
			return err
		})
		var nomatch *age.NoIdentityMatchError
		if errors.As(err, &nomatch) {
			fmt.Fprintln(e, a.t.S("msg_rekey_profile_done", "Name", p))
			continue // encrypted to the new key by an earlier run
		}
		if err != nil {
			fmt.Fprintln(e, "se-env:", err)
			return a.fail("err_rekey_failed", "Name", p, "Path", a.oldIdentityPath())
		}
		// The same plaintext, so the index and the copy of public values stay valid.
		nct, err := encryptBytes(plain, r)
		wipe(plain)
		if err == nil {
			err = writeAtomic(a.profileFile(p), nct)
		}
		if err != nil {
			fmt.Fprintln(e, "se-env:", err)
			return a.fail("err_rekey_failed", "Name", p, "Path", a.oldIdentityPath())
		}
		fmt.Fprintln(e, a.t.S("msg_rekey_profile", "Name", p))
	}
	a.deleteKeyLine(oldLine)
	if err := os.Remove(a.oldIdentityPath()); err != nil {
		return err
	}
	fmt.Fprintln(e, "se-env: "+a.t.S("msg_rekey_done", "Profiles", len(profiles)))
	return nil
}
