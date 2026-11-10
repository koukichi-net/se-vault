package main

import (
	"fmt"
	"path/filepath"
)

func (a *app) mark(p string) string {
	if exists(p) {
		return p
	}
	return p + "  " + a.t.S("word_not_found")
}

func (a *app) cmdHelp() {
	plugin := a.pluginPath()
	if plugin == "" {
		plugin = pluginName + "  " + a.t.S("help_plugin_missing")
	}
	fmt.Print(a.t.S("help",
		"Version", version,
		"AgeVersion", ageVersion(),
		"Lang", a.t.lang,
		"Self", a.mark(a.self),
		"Home", a.mark(a.home),
		"Identity", a.mark(a.identityPath),
		"Sessions", a.runDir,
		"Vault", a.mark(a.vault),
		"Completion", a.mark(filepath.Join(a.home, "completions", "_kc-env")),
		"Plugin", plugin,
	))
	fmt.Println()
}
