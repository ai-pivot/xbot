package channel

import "testing"

func TestHelpTranslationsCoverBuiltinCommandCatalog(t *testing.T) {
	names := make(map[string]struct{})
	for _, command := range TUICommandList() {
		names[command.Name] = struct{}{}
	}
	for _, name := range []string{
		"/new", "/version", "/help", "/prompt", "/set-llm", "/unset-llm",
		"/llm", "/llms", "/compress", "/continue", "/usage", "/context mode",
		"/context", "/models", "/set-model", "!", "/settings", "/plugin reload-all",
		"/app", "/goal clear", "/goal status", "/goal", "/info", "/export",
	} {
		names[name] = struct{}{}
	}

	for _, lang := range []string{"zh", "en", "ja"} {
		locale := GetLocale(lang)
		translated := make(map[string]struct{}, len(locale.HelpCmds))
		for _, entry := range locale.HelpCmds {
			if entry.Cmd == "" || entry.Desc == "" {
				t.Errorf("%s has incomplete help translation: %#v", lang, entry)
			}
			if _, duplicate := translated[entry.Cmd]; duplicate {
				t.Errorf("%s has duplicate help translation for %q", lang, entry.Cmd)
			}
			translated[entry.Cmd] = struct{}{}
		}
		for name := range names {
			if _, ok := translated[name]; !ok {
				t.Errorf("%s is missing help translation for %q", lang, name)
			}
		}
		for name := range translated {
			if _, ok := names[name]; !ok {
				t.Errorf("%s has stale help translation for %q", lang, name)
			}
		}
	}
}
