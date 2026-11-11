package main

import (
	"embed"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"golang.org/x/text/language"
)

//go:embed locales/*.toml locales/windows/*.toml
var localeFS embed.FS

// translator wraps a go-i18n localizer. Messages live in locales/<lang>.toml;
// on Windows, locales/windows/<lang>.toml replaces the messages that differ
// (and platformText swaps the names of the macOS parts).
type translator struct {
	bundle *i18n.Bundle
	loc    *i18n.Localizer
	lang   string
}

func newBundle() *i18n.Bundle {
	b := i18n.NewBundle(language.English)
	b.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	for _, dir := range append([]string{"locales"}, platformLocales...) {
		entries, _ := localeFS.ReadDir(dir)
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			data, err := localeFS.ReadFile(dir + "/" + e.Name())
			if err == nil {
				b.MustParseMessageFileBytes(data, e.Name())
			}
		}
	}
	return b
}

func newTranslator() *translator {
	lang := detectLang()
	b := newBundle()
	return &translator{bundle: b, loc: i18n.NewLocalizer(b, lang, "en"), lang: lang}
}

// detectLang returns a BCP 47 tag from SE_ENV_LANG, LC_ALL, LC_MESSAGES or LANG
// (first non-empty wins, as in POSIX). "C"/"POSIX" mean English. Without any
// of them: English on macOS, the user's display language on Windows.
func detectLang() string {
	for _, k := range []string{"SE_ENV_LANG", "LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" {
			return normalizeLocale(v)
		}
	}
	return systemLang()
}

func normalizeLocale(v string) string {
	if i := strings.IndexAny(v, ".@"); i >= 0 { // ja_JP.UTF-8, de_DE@euro
		v = v[:i]
	}
	if v == "" || v == "C" || v == "POSIX" {
		return "en"
	}
	return strings.ReplaceAll(v, "_", "-")
}

// kvMap turns "Key", value, "Key2", value2 ... into template data.
func kvMap(kv []any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			m[k] = kv[i+1]
		}
	}
	return m
}

// S localizes a message. Falls back to the message ID if it is missing.
func (t *translator) S(id string, kv ...any) string {
	s, err := t.loc.Localize(&i18n.LocalizeConfig{MessageID: id, TemplateData: kvMap(kv)})
	if err != nil || s == "" {
		return id
	}
	return platformText(s)
}

// N localizes a message with plural forms selected by count. "Count" is set automatically.
func (t *translator) N(id string, count int, kv ...any) string {
	data := kvMap(kv)
	data["Count"] = count
	s, err := t.loc.Localize(&i18n.LocalizeConfig{MessageID: id, TemplateData: data, PluralCount: count})
	if err != nil || s == "" {
		return id
	}
	return platformText(s)
}

// messageIDs lists the IDs with the given prefix, sorted (used by `__complete msgs`).
func (t *translator) messageIDs(prefixes ...string) []string {
	var raw map[string]any
	data, _ := localeFS.ReadFile("locales/en.toml")
	_, _ = toml.Decode(string(data), &raw)
	var ids []string
	for id := range raw {
		for _, p := range prefixes {
			if strings.HasPrefix(id, p) {
				ids = append(ids, id)
				break
			}
		}
	}
	sort.Strings(ids)
	return ids
}
