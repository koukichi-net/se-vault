package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// The Touch ID dialog is shown by age-plugin-se, so by default macOS names
// only the plugin. se-env puts what it is asking for in reasonEnv just for
// the moment it starts the plugin; a plugin built with
// patches/age-plugin-se-reason.patch shows it in the dialog
// (LAContext.localizedReason), and only when its parent process is the
// se-env next to it. Other plugins ignore the variable.
//
// macOS puts the reason after its own words: "“age-plugin-se” is trying to
// <reason>." / "“age-plugin-se”で次の操作を実行しようとしています: <reason>"
// (APP_ASKING_AUTH in LocalAuthentication's UIAgent.loctable). Line breaks
// are kept.
const reasonEnv = "SE_ENV_AUTH_REASON"

// maxReason bounds the text the plugin accepts (the patch refuses longer).
const maxReason = 2000

// want records what this invocation needs Touch ID for: the dialog's
// "target" and "action" lines. Commands call it before they decrypt.
func (a *app) want(target, action string) { a.target, a.purpose = target, action }

// authReason is the text of the Touch ID dialog. s is the session that will
// keep the decrypted profile, or nil.
func (a *app) authReason(s *session) string {
	or := func(v string) string {
		if v == "" {
			return "?"
		}
		return v
	}
	sess := ""
	if s != nil {
		sess = "\n" + a.t.S("auth_reason_session",
			"Cmd", a.cmdText(s.info, " "), "Expires", time.Unix(s.info.Exp, 0).Format("15:04"))
	}
	r := a.t.S("auth_reason", "Chain", callerChain(os.Getppid()),
		"Target", or(a.target), "Action", or(a.purpose), "Session", sess)
	if len(r) > maxReason {
		r = strings.ToValidUTF8(r[:maxReason-len("…")], "") + "…"
	}
	return r
}

// withReason runs f (which starts the plugin) with the dialog text in the
// environment, and takes it out again so no command started later sees it.
func (a *app) withReason(s *session, f func() error) error {
	os.Setenv(reasonEnv, a.authReason(s))
	defer os.Unsetenv(reasonEnv)
	return f()
}

// callerChain names the processes that led to this se-env, outermost first:
// "Ghostty › login › zsh › claude › zsh › se-env". The names are what those
// processes were started as. Any program running as this user can choose its
// own name, so they tell the user who is asking, not prove it.
func callerChain(pid int) string {
	const maxDepth = 8
	var names []string
	for len(names) < maxDepth && pid > 1 {
		ppid, name, ok := procName(pid)
		if !ok {
			break
		}
		names = append(names, name)
		pid = ppid
	}
	if len(names) == 0 {
		return "?"
	}
	out := make([]string, 0, len(names)+2)
	if pid > 1 { // more ancestors than shown
		out = append(out, "…")
	}
	for i := len(names) - 1; i >= 0; i-- {
		out = append(out, names[i])
	}
	return strings.Join(append(out, "se-env"), " › ")
}

// procName returns a process's parent and a short name for it: the
// application bundle for a program inside one ("Ghostty"), otherwise the
// file name it was started as ("claude", not the version-named file a
// symlink points to). Falls back to the kernel's 16-byte command name.
func procName(pid int) (ppid int, name string, ok bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || int(kp.Proc.P_pid) != pid {
		return 0, "", false
	}
	ppid = int(kp.Eproc.Ppid)
	comm := kp.Proc.P_comm[:]
	if i := bytes.IndexByte(comm, 0); i >= 0 {
		comm = comm[:i]
	}
	name = string(comm)
	if exe := execPath(pid); exe != "" {
		name = displayName(exe)
	}
	name = printable(name)
	if name == "" {
		name = "?"
	}
	return ppid, name, true
}

// execPath reads the path a process was started with (kern.procargs2:
// argc, then the path). Empty when it cannot be read, as for processes of
// other users.
func execPath(pid int) string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 5 {
		return ""
	}
	b = b[4:] // argc
	if i := bytes.IndexByte(b, 0); i > 0 {
		return string(b[:i])
	}
	return ""
}

// displayName turns an executable path into the name shown in the dialog.
func displayName(exe string) string {
	if i := strings.Index(exe, ".app/"); i > 0 {
		return filepath.Base(exe[:i])
	}
	return filepath.Base(exe)
}

// commandText shows the command exec is about to run, cut short and safe to
// display (printable keeps it to one line).
func commandText(args []string) string {
	s := printable(strings.Join(args, " "))
	if len([]rune(strings.Join(args, " "))) > len([]rune(s)) {
		s += "…"
	}
	return s
}
