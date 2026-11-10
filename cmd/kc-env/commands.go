package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
)

func (a *app) cmdInit() error {
	if exists(a.identityPath) {
		return a.fail("err_already_initialized", "Path", a.identityPath)
	}
	if err := os.MkdirAll(a.vault, 0o700); err != nil {
		return err
	}
	if err := a.checkPerms(); err != nil {
		return err
	}
	plug := a.pluginPath()
	if plug == "" {
		return a.fail("err_plugin_missing")
	}
	gen := exec.Command(plug, "keygen", "--access-control", "any-biometry-or-passcode", "-o", a.identityPath)
	gen.Stdout, gen.Stderr = os.Stderr, os.Stderr
	if err := gen.Run(); err != nil {
		return a.fail("err_keygen_failed")
	}
	if _, err := a.recipient(); err != nil { // the public key must be derivable from the new key
		os.Remove(a.identityPath)
		return a.fail("err_keygen_failed")
	}
	fmt.Println(a.t.S("msg_initialized", "Home", a.home))
	return nil
}

func (a *app) cmdList(args []string) error {
	profiles := a.allProfiles()
	if len(args) > 0 {
		if err := a.checkProfile(args[0]); err != nil {
			return err
		}
		if err := a.requireProfile(args[0]); err != nil {
			return err
		}
		profiles = []string{args[0]}
	}
	if len(profiles) == 0 {
		fmt.Fprintln(os.Stderr, a.t.S("msg_no_profiles"))
		return nil
	}
	for _, p := range profiles {
		updated := ""
		if fi, err := os.Stat(a.profileFile(p)); err == nil {
			updated = fi.ModTime().Format("2006-01-02 15:04")
		}
		keys, ok := a.indexKeys(p)
		if !ok {
			fmt.Println(a.t.S("list_no_index", "Name", p, "Updated", updated))
			fmt.Println("  └─ " + a.t.S("list_no_index_hint", "Name", p))
			continue
		}
		fmt.Println(a.t.N("list_profile", len(keys), "Name", p, "Updated", updated))
		for i, k := range keys {
			branch := "├─"
			if i == len(keys)-1 {
				branch = "└─"
			}
			fmt.Printf("  %s %s\n", branch, k)
		}
	}
	return nil
}

func (a *app) cmdProfiles() error {
	for _, p := range a.allProfiles() {
		fmt.Println(p)
	}
	return nil
}

func (a *app) cmdLs(args []string) error {
	verify := false
	if len(args) > 0 && args[0] == "--verify" {
		verify, args = true, args[1:]
	}
	if len(args) != 1 {
		return a.fail("usage_ls")
	}
	p := args[0]
	if err := a.checkProfile(p); err != nil {
		return err
	}
	if err := a.requireProfile(p); err != nil {
		return err
	}
	keys, ok := a.indexKeys(p)
	if verify || !ok {
		if err := a.requireInit(); err != nil {
			return err
		}
		kv, err := a.load(p)
		if err != nil {
			return err
		}
		keys = sortedKeys(kv)
		if err := a.writeIndex(p, keys); err != nil {
			return err
		}
	}
	for _, k := range keys {
		fmt.Println(k)
	}
	return nil
}

func (a *app) profileKeyArgs(args []string, usage string) (string, string, error) {
	if len(args) != 2 {
		return "", "", a.fail(usage)
	}
	if err := a.checkProfile(args[0]); err != nil {
		return "", "", err
	}
	if err := a.checkKey(args[1]); err != nil {
		return "", "", err
	}
	return args[0], args[1], nil
}

func (a *app) cmdSet(args []string) error {
	p, k, err := a.profileKeyArgs(args, "usage_set")
	if err != nil {
		return err
	}
	if unsafeKey(k) {
		return a.fail("err_unsafe_key", "Name", k)
	}
	if err := a.requireInit(); err != nil {
		return err
	}
	value, err := a.readSecret(p + "/" + k)
	if err != nil {
		return err
	}
	if strings.ContainsRune(value, 0) {
		return a.fail("err_value_nul")
	}
	unlock, err := a.lockProfile(p)
	if err != nil {
		return err
	}
	defer unlock()
	kv, err := a.load(p)
	if err != nil {
		return err
	}
	kv[k] = value
	if err := a.save(p, kv); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, a.t.S("msg_set", "Profile", p, "Key", k))
	return nil
}

func (a *app) getValue(p, k string) (string, error) {
	if err := a.requireInit(); err != nil {
		return "", err
	}
	if err := a.requireProfile(p); err != nil {
		return "", err
	}
	kv, err := a.load(p)
	if err != nil {
		return "", err
	}
	v, ok := kv[k]
	if !ok {
		return "", a.fail("err_no_key", "Profile", p, "Key", k)
	}
	return v, nil
}

func (a *app) cmdGet(args []string) error {
	p, k, err := a.profileKeyArgs(args, "usage_get")
	if err != nil {
		return err
	}
	v, err := a.getValue(p, k)
	if err != nil {
		return err
	}
	fmt.Println(v)
	return nil
}

// header <profile> <KEY> [--name H] [--prefix P] — one HTTP header as a JSON object.
func (a *app) cmdHeader(args []string) error {
	name, prefix := "Authorization", "Bearer "
	var pos []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--name", "--prefix":
			if i+1 >= len(args) {
				return a.fail("err_option_value", "Opt", args[i])
			}
			if args[i] == "--name" {
				name = args[i+1]
			} else {
				prefix = args[i+1]
			}
			i++
		default:
			pos = append(pos, args[i])
		}
	}
	p, k, err := a.profileKeyArgs(pos, "usage_header")
	if err != nil {
		return err
	}
	v, err := a.getValue(p, k)
	if err != nil {
		return err
	}
	out, err := json.Marshal(map[string]string{name: prefix + v})
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
}

// lookPathIn finds an executable like exec.LookPath, but in the given PATH.
func lookPathIn(name, pathList string) (string, error) {
	if strings.Contains(name, "/") {
		if fi, err := os.Stat(name); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return name, nil
		}
		return "", exec.ErrNotFound
	}
	for _, dir := range filepath.SplitList(pathList) {
		if dir == "" {
			dir = "."
		}
		p := filepath.Join(dir, name)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

// childEnv returns the environment with PATH restored and extra variables set.
func (a *app) childEnv(extra map[string]string) []string {
	var env []string
	for _, e := range os.Environ() {
		k, _, _ := strings.Cut(e, "=")
		if k == "PATH" {
			continue
		}
		if _, override := extra[k]; override {
			continue
		}
		env = append(env, e)
	}
	env = append(env, "PATH="+a.origPath)
	for _, k := range sortedKeys(extra) {
		env = append(env, k+"="+extra[k])
	}
	return env
}

func (a *app) cmdExec(args []string) error {
	if len(args) < 1 {
		return a.fail("usage_exec")
	}
	p := args[0]
	if err := a.checkProfile(p); err != nil {
		return err
	}
	if err := a.requireInit(); err != nil {
		return err
	}
	args = args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return a.fail("usage_exec")
	}
	if err := a.requireProfile(p); err != nil {
		return err
	}
	kv, err := a.load(p)
	if err != nil {
		return err
	}
	path, err := lookPathIn(args[0], a.origPath)
	if err != nil {
		return a.fail("err_command_not_found", "Cmd", args[0])
	}
	return syscall.Exec(path, args, a.childEnv(kv))
}

// rm <profile> <KEY>...  |  rm --all <profile>
//
// Deleting always needs the Secure Enclave key (Touch ID), never a session:
// a session lets programs read secrets, not destroy them unattended.
func (a *app) cmdRm(args []string) error {
	all := false
	if len(args) > 0 && args[0] == "--all" {
		all, args = true, args[1:]
	}
	if len(args) == 0 {
		return a.fail("usage_rm")
	}
	p := args[0]
	if err := a.checkProfile(p); err != nil {
		return err
	}
	if err := a.requireInit(); err != nil {
		return err
	}
	if err := a.requireProfile(p); err != nil {
		return err
	}
	keys := args[1:]
	if all {
		if len(keys) != 0 {
			return a.fail("usage_rm_all")
		}
		n := "?"
		if ks, ok := a.indexKeys(p); ok {
			n = strconv.Itoa(len(ks))
		}
		if err := a.confirm(a.t.S("confirm_rm_all", "Count", n, "Name", p)); err != nil {
			return err
		}
		if err := a.presence(); err != nil {
			return err
		}
		unlock, err := a.lockProfile(p)
		if err != nil {
			return err
		}
		defer unlock()
		if err := a.save(p, map[string]string{}); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, a.t.S("msg_removed_all", "Name", p))
		return nil
	}
	if len(keys) == 0 {
		return a.fail("usage_rm")
	}
	for _, k := range keys {
		if err := a.checkKey(k); err != nil {
			return err
		}
	}
	unlock, err := a.lockProfile(p)
	if err != nil {
		return err
	}
	defer unlock()
	kv, err := a.loadFresh(p, true) // Touch ID; refused key names stay removable
	if err != nil {
		return err
	}
	for _, k := range keys {
		if _, ok := kv[k]; !ok {
			return a.fail("err_no_key_nothing_removed", "Profile", p, "Key", k)
		}
	}
	for _, k := range keys {
		delete(kv, k)
	}
	if err := a.save(p, kv); err != nil {
		return err
	}
	for _, k := range keys {
		fmt.Fprintln(os.Stderr, a.t.S("msg_removed_key", "Profile", p, "Key", k))
	}
	return nil
}

// presence asks for Touch ID with the Secure Enclave key.
func (a *app) presence() error {
	fmt.Fprintln(os.Stderr, a.t.S("prompt_auth"))
	if !a.authPresence() {
		return a.fail("err_auth_failed_nothing")
	}
	return nil
}

// rm-profile [--force] <profile>
func (a *app) cmdRmProfile(args []string) error {
	force := false
	if len(args) > 0 && args[0] == "--force" {
		force, args = true, args[1:]
	}
	if len(args) != 1 {
		return a.fail("usage_rm_profile")
	}
	p := args[0]
	if err := a.checkProfile(p); err != nil {
		return err
	}
	if err := a.requireInit(); err != nil {
		return err
	}
	if err := a.requireProfile(p); err != nil {
		return err
	}
	hasKeys := func(keys []string) error {
		fmt.Fprintln(os.Stderr, "kc-env: "+a.t.N("err_rm_profile_has_keys", len(keys), "Name", p))
		for _, k := range keys {
			fmt.Fprintln(os.Stderr, "  "+k)
		}
		return a.fail("hint_rm_profile", "Name", p)
	}
	keys, ok := a.indexKeys(p)
	if !force {
		if !ok {
			return a.fail("err_rm_profile_no_index", "Name", p)
		}
		if len(keys) > 0 {
			return hasKeys(keys)
		}
		if err := a.confirm(a.t.S("confirm_rm_empty_profile", "Name", p)); err != nil {
			return err
		}
		// The index is only a hint. Decrypt (Touch ID) to see that the
		// profile really is empty before deleting it.
		kv, err := a.loadFresh(p, true)
		if err != nil {
			return err
		}
		if len(kv) > 0 {
			_ = a.writeIndex(p, sortedKeys(kv))
			return hasKeys(sortedKeys(kv))
		}
	} else {
		n := "?"
		if ok {
			n = strconv.Itoa(len(keys))
		}
		if err := a.confirm(a.t.S("confirm_rm_profile_force", "Name", p, "Count", n)); err != nil {
			return err
		}
		if err := a.presence(); err != nil {
			return err
		}
	}
	os.Remove(a.profileFile(p))
	os.Remove(a.indexFile(p))
	os.Remove(a.lockFile(p))
	if s := a.activeSession(); s != nil {
		s.forget(p)
	}
	fmt.Fprintln(os.Stderr, a.t.S("msg_removed_profile", "Name", p))
	return nil
}

// ---- session -----------------------------------------------------------------

const maxTTL = 24 * time.Hour

func parseTTL(s string) (time.Duration, bool) {
	if s == "" {
		return 0, false
	}
	unit := time.Second
	switch s[len(s)-1] {
	case 's':
		s = s[:len(s)-1]
	case 'm':
		unit, s = time.Minute, s[:len(s)-1]
	case 'h':
		unit, s = time.Hour, s[:len(s)-1]
	case 'd':
		unit, s = 24*time.Hour, s[:len(s)-1]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || n > int(maxTTL/unit) {
		return 0, false
	}
	return time.Duration(n) * unit, true
}

// printable makes text from outside safe to show on a terminal.
func printable(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, s)
	if r := []rune(s); len(r) > 40 {
		s = string(r[:40])
	}
	return s
}

func (a *app) cmdSession(args []string) error {
	if len(args) > 0 && args[0] == "status" {
		return a.cacheInfo()
	}
	ttl, only := "30m", ""
loop:
	for len(args) > 0 {
		switch args[0] {
		case "--ttl", "--only":
			if len(args) < 2 {
				return a.fail("err_option_value", "Opt", args[0])
			}
			if args[0] == "--ttl" {
				ttl = args[1]
			} else {
				only = args[1]
			}
			args = args[2:]
		case "--":
			args = args[1:]
			break loop
		default:
			break loop
		}
	}
	if err := a.requireInit(); err != nil {
		return err
	}
	if a.activeSession() != nil {
		return a.fail("err_in_session")
	}
	d, ok := parseTTL(ttl)
	if !ok {
		return a.fail("err_invalid_ttl", "Value", ttl)
	}
	var onlyList []string
	if only != "" {
		for _, p := range strings.Split(only, ",") {
			if err := a.checkProfile(p); err != nil {
				return err
			}
			onlyList = append(onlyList, p)
		}
	}
	if len(args) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/zsh"
		}
		args = []string{sh}
	}
	path, err := lookPathIn(args[0], a.origPath)
	if err != nil {
		return a.fail("err_command_not_found", "Cmd", args[0])
	}
	a.sessions() // clears sockets left behind by killed sessions

	ag, err := a.newAgent(d, onlyList, printable(filepath.Base(args[0])))
	if err != nil {
		return err
	}
	defer ag.shutdown()

	scope := only
	if scope == "" {
		scope = a.t.S("word_all")
	}
	fmt.Fprintln(os.Stderr, "kc-env: "+a.t.S("msg_session_started", "Expires", ag.exp.Format("15:04"), "Scope", scope))

	cmd := exec.Command(path, args[1:]...)
	cmd.Args[0] = args[0]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = a.childEnv(map[string]string{"KC_ENV_SESSION": ag.id}) // an ID, not a secret
	// Ctrl-C / Ctrl-\ are meant for the child (it gets them from the terminal);
	// TERM/HUP are forwarded. Handlers, unlike SIG_IGN, are not inherited by exec.
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigs)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		for s := range sigs {
			if s == syscall.SIGTERM || s == syscall.SIGHUP {
				_ = cmd.Process.Signal(s)
			}
		}
	}()
	if err := ag.serve(cmd.Process.Pid); err != nil {
		fmt.Fprintln(os.Stderr, "kc-env:", err) // the command runs without a session
	}
	err = cmd.Wait()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return exitCode(128 + int(ws.Signal()))
		}
		return exitCode(ee.ExitCode())
	}
	return err
}

func (a *app) cmdLock() error {
	s := a.activeSession()
	if s == nil {
		fmt.Fprintln(os.Stderr, "kc-env: "+a.t.S("msg_no_session"))
		return nil
	}
	if rs, err := ask(s.path, request{Op: "lock"}, true); err != nil || !rs.OK {
		return a.fail("err_session_unreachable")
	}
	fmt.Fprintln(os.Stderr, "kc-env: "+a.t.S("msg_locked"))
	return nil
}

// ---- cache info / list / rm --------------------------------------------------

func (a *app) cmdCache(args []string) error {
	sub := "info"
	if len(args) > 0 {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "info":
		return a.cacheInfo()
	case "list", "ls":
		return a.cacheList()
	case "rm":
		return a.cacheRm(args)
	}
	return a.fail("usage_cache")
}

func (a *app) cmdText(si sessionInfo, sep string) string {
	cmd := printable(si.Cmd)
	if cmd == "" {
		cmd = "-"
	}
	if si.Pid > 0 {
		if sep == ":" {
			return cmd + ":" + strconv.Itoa(si.Pid)
		}
		return cmd + " (PID " + strconv.Itoa(si.Pid) + ")"
	}
	return cmd
}

func (a *app) cacheInfo() error {
	s := a.activeSession()
	if s == nil {
		fmt.Println(a.t.S("cache_outside"))
		return nil
	}
	si := s.info
	fmt.Print(a.t.S("cache_info", "ID", si.ID[:8], "Cmd", a.cmdText(si, " "),
		"Start", time.Unix(si.Start, 0).Format("2006-01-02 15:04"),
		"Expires", time.Unix(si.Exp, 0).Format("2006-01-02 15:04"), "Remaining", a.remaining(si.Exp),
		"Scope", a.scopeText(si), "Cached", a.cachedText(si, "word_none_yet")))
	fmt.Println()
	return nil
}

func (a *app) cacheList() error {
	all := a.sessions()
	if len(all) == 0 {
		fmt.Println(a.t.S("cache_none"))
		return nil
	}
	cur := ""
	if s := a.activeSession(); s != nil {
		cur = s.id
	}
	widths := []int{9, 12, 20, 16, 19}
	row := func(mark string, cols ...string) {
		var b strings.Builder
		b.WriteString(mark)
		for i, c := range cols {
			b.WriteByte(' ')
			b.WriteString(c)
			if i < len(widths) {
				pad := widths[i] - displayWidth(c)
				if pad < 1 {
					pad = 1
				}
				b.WriteString(strings.Repeat(" ", pad))
			}
		}
		fmt.Println(strings.TrimRight(b.String(), " "))
	}
	row(" ", a.t.S("col_id"), a.t.S("col_started"), a.t.S("col_expires"),
		a.t.S("col_cmd"), a.t.S("col_scope"), a.t.S("col_cached"))
	for _, si := range all {
		mark := " "
		if si.ID == cur {
			mark = "*"
		}
		row(mark, si.ID[:8], time.Unix(si.Start, 0).Format("01-02 15:04"),
			time.Unix(si.Exp, 0).Format("15:04")+"("+a.remaining(si.Exp)+")",
			a.cmdText(si, ":"), a.scopeText(si), a.cachedText(si, "word_none"))
	}
	fmt.Println(a.t.S("cache_list_footer"))
	return nil
}

func (a *app) cacheRm(args []string) error {
	if err := a.requireInit(); err != nil {
		return err
	}
	if len(args) == 0 {
		return a.fail("usage_cache_rm")
	}
	all := a.sessions()
	seen := map[string]bool{}
	var targets []sessionInfo
	add := func(si sessionInfo) {
		if !seen[si.ID] {
			seen[si.ID] = true
			targets = append(targets, si)
		}
	}
	for _, arg := range args {
		switch arg {
		case "--all":
			for _, si := range all {
				add(si)
			}
		case "--current":
			s := a.activeSession()
			if s == nil {
				return a.fail("err_outside_session")
			}
			add(s.info)
		default:
			if len(arg) < 4 || len(arg) > 16 || strings.Trim(arg, "0123456789abcdef") != "" {
				return a.fail("err_invalid_id", "ID", arg)
			}
			var matches []sessionInfo
			for _, si := range all {
				if strings.HasPrefix(si.ID, arg) {
					matches = append(matches, si)
				}
			}
			if len(matches) != 1 {
				return a.fail("err_id_matches", "ID", arg, "Count", len(matches))
			}
			add(matches[0])
		}
	}
	if len(targets) == 0 {
		fmt.Println(a.t.S("cache_nothing_to_remove"))
		return nil
	}
	fmt.Fprintln(os.Stderr, a.t.S("cache_rm_header"))
	for _, si := range targets {
		fmt.Fprintln(os.Stderr, "  "+a.t.S("cache_rm_item", "ID", si.ID[:8], "Cmd", a.cmdText(si, ":"),
			"Scope", a.scopeText(si), "Cached", a.cachedText(si, "word_none")))
	}
	if err := a.presence(); err != nil {
		return err
	}
	for _, si := range targets {
		a.endSession(si.ID)
	}
	fmt.Fprintln(os.Stderr, a.t.N("msg_cache_removed", len(targets)))
	return nil
}
