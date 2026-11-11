package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

func isTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

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
			if v, err = a.dialogSecret(label); err != nil {
				return "", err
			}
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
	return a.dialogConfirm(msg)
}
