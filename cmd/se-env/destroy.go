package main

import (
	"bufio"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// destroy — erase the Secure Enclave key and every secret. Terminal only, several gates.
func (a *app) cmdDestroy(args []string) error {
	if len(args) != 0 {
		return a.fail("usage_destroy")
	}
	if !isTTY(os.Stdin) || !isTTY(os.Stdout) || !isTTY(os.Stderr) {
		return a.fail("err_destroy_tty")
	}
	if os.Getenv("SE_ENV_SESSION") != "" {
		return a.fail("err_destroy_session")
	}
	if a.home == "" || a.home == "/" || !exists(a.bin) || filepath.Dir(a.self) != a.bin {
		return a.fail("err_destroy_home", "Home", a.home)
	}
	if !exists(a.identityPath) {
		return a.fail("err_destroy_nothing", "Path", a.identityPath)
	}
	if err := a.checkPerms(); err != nil {
		return err
	}

	// 1. show everything that will be lost
	profiles := a.allProfiles()
	total, unknown := 0, false
	var lines []string
	for _, p := range profiles {
		if keys, ok := a.indexKeys(p); ok {
			total += len(keys)
			lines = append(lines, "    "+a.t.N("destroy_profile_line", len(keys), "Name", p))
		} else {
			unknown = true
			lines = append(lines, "    "+a.t.S("destroy_profile_unknown", "Name", p))
		}
	}
	created := ""
	if b, err := os.ReadFile(a.identityPath); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "# created") {
				created = l
				break
			}
		}
	}
	atLeast := ""
	if unknown {
		atLeast = a.t.S("destroy_at_least")
	}
	e := os.Stderr
	fmt.Fprintln(e)
	fmt.Fprintln(e, a.t.S("destroy_banner"))
	fmt.Fprintln(e)
	fmt.Fprintln(e, a.t.S("destroy_summary", "Home", a.home, "Identity", a.identityPath, "Created", created,
		"Profiles", len(profiles), "Keys", total, "AtLeast", atLeast))
	for _, l := range lines {
		fmt.Fprintln(e, l)
	}
	fmt.Fprintln(e)
	fmt.Fprintln(e, a.t.S("destroy_scope"))
	fmt.Fprintln(e)

	// 2. first confirmation
	in := bufio.NewReader(os.Stdin)
	fmt.Fprint(e, a.t.S("destroy_confirm")+" [y/N] ")
	line, _ := in.ReadString('\n')
	if s := strings.ToLower(strings.TrimSpace(line)); s != "y" && s != "yes" {
		return a.fail("err_aborted")
	}

	// 3. type the phrase exactly (ASCII in every language)
	phrase := fmt.Sprintf("destroy all %d keys", total)
	fmt.Fprintln(e)
	fmt.Fprintln(e, a.t.S("destroy_type_phrase", "Phrase", phrase))
	fmt.Fprint(e, "> ")
	typed, err := in.ReadString('\n')
	if err != nil || strings.TrimRight(typed, "\r\n") != phrase {
		return a.fail("err_phrase_mismatch")
	}

	// 4. last chance (Ctrl-C keeps the default action: exit, nothing deleted)
	fmt.Fprintln(e)
	for i := 10; i > 0; i-- {
		fmt.Fprintf(e, "\r  %s ", a.t.S("destroy_countdown", "Seconds", fmt.Sprintf("%2d", i)))
		time.Sleep(time.Second)
	}
	fmt.Fprintln(e)

	// 5. Touch ID with the Secure Enclave key (never a session)
	fmt.Fprintln(e, a.t.S("prompt_auth"))
	a.want(a.t.S("auth_target_everything", "Profiles", len(profiles)), a.t.S("auth_do_destroy"))
	if !a.authPresence() {
		return a.fail("err_auth_failed_nothing")
	}

	// 6. delete and verify; do not leave a half-deleted state
	signal.Ignore(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP)
	for _, si := range a.sessions() { // running sessions hold decrypted profiles in memory
		a.endSession(si.ID)
	}
	legacyRecipient := filepath.Join(a.home, "recipient.txt") // written by se-env 0.4 and earlier
	os.RemoveAll(a.vault)
	os.RemoveAll(a.runDir)
	os.Remove(a.identityPath)
	os.Remove(legacyRecipient)
	signal.Reset(syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTSTP)
	var left []string
	for _, p := range []string{a.identityPath, legacyRecipient, a.vault, a.runDir} {
		if exists(p) {
			left = append(left, "  "+p)
		}
	}
	if len(left) > 0 {
		return a.fail("err_destroy_left", "Paths", strings.Join(left, "\n"))
	}
	fmt.Fprintln(e)
	fmt.Fprintln(e, a.t.S("destroy_done", "Profiles", len(profiles), "Keys", total))
	return nil
}
