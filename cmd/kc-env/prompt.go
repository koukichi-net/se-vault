package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"golang.org/x/term"
)

// Fixed path: the dialog returns a secret, so it is never looked up on PATH.
const osascript = "/usr/bin/osascript"

func isTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// appleScriptString quotes s as an AppleScript string literal.
func appleScriptString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// readSecret gets a value: hidden terminal prompt > piped stdin > hidden dialog
// (the dialog covers callers that run commands without a TTY).
func (a *app) readSecret(label string) (string, error) {
	var v string
	if isTTY(os.Stdin) {
		fmt.Fprint(os.Stderr, a.t.S("prompt_value", "Label", label))
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", a.fail("err_no_value")
		}
		v = string(b)
	} else {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", a.fail("err_no_value")
		}
		v = strings.TrimRight(string(b), "\r\n")
		if v == "" {
			script := fmt.Sprintf(`display dialog %s default answer "" with hidden answer with title "kc-env"`,
				appleScriptString(a.t.S("dialog_value", "Label", label))) + "\n" + `text returned of result`
			out, err := exec.Command(osascript, "-e", script).Output()
			if err != nil {
				return "", a.fail("err_cancelled")
			}
			v = strings.TrimRight(string(out), "\n")
		}
	}
	if v == "" {
		return "", a.fail("err_empty_value")
	}
	return v, nil
}

// confirm asks yes/no (default no): y/N on a terminal, otherwise a dialog.
func (a *app) confirm(msg string) error {
	if isTTY(os.Stdin) {
		fmt.Fprintf(os.Stderr, "%s [y/N] ", msg)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return nil
		}
		return a.fail("err_aborted")
	}
	cancel, del := appleScriptString(a.t.S("button_cancel")), appleScriptString(a.t.S("button_delete"))
	script := fmt.Sprintf(`display dialog %s buttons {%s, %s} default button %s cancel button %s with icon caution with title "kc-env"`,
		appleScriptString(msg), cancel, del, cancel, cancel)
	if err := exec.Command(osascript, "-e", script).Run(); err != nil {
		return a.fail("err_aborted")
	}
	return nil
}
