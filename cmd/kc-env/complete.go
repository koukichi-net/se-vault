package main

import (
	"fmt"
	"strings"

	"golang.org/x/text/width"
)

// cmdComplete serves shell completion. It never decrypts, so it never prompts.
//
//	__complete profiles        profile names
//	__complete keys <profile>  key names from the index
//	__complete sessions        "<id8>:<description>" for each running session
//	__complete msgs            "<id>\t<text>" for completion descriptions
func (a *app) cmdComplete(args []string) error {
	if len(args) == 0 {
		return nil
	}
	switch args[0] {
	case "profiles":
		for _, p := range a.allProfiles() {
			fmt.Println(p)
		}
	case "keys":
		if len(args) > 1 && reProfile.MatchString(args[1]) {
			keys, _ := a.indexKeys(args[1])
			for _, k := range keys {
				fmt.Println(k)
			}
		}
	case "sessions":
		for _, si := range a.sessions() {
			fmt.Printf("%s:%s\n", si.ID[:8],
				a.t.S("comp_session_desc", "Cmd", a.cmdText(si, ":"), "Scope", a.scopeText(si)))
		}
	case "msgs":
		for _, id := range a.t.messageIDs("comp_") {
			fmt.Printf("%s\t%s\n", id, strings.ReplaceAll(a.t.S(id), "\n", " "))
		}
	}
	return nil
}

// displayWidth counts East Asian wide characters as two columns.
func displayWidth(s string) int {
	n := 0
	for _, r := range s {
		switch width.LookupRune(r).Kind() {
		case width.EastAsianWide, width.EastAsianFullwidth:
			n += 2
		default:
			n++
		}
	}
	return n
}
