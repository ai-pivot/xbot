package cli

import (
	"go/ast"
	goparser "go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	ch "xbot/channel"
	"xbot/protocol"
)

func TestCommandCatalogFeedsHelpCompletionAndPalette(t *testing.T) {
	m := newCLIModel()
	m.width = 240
	m.commandCatalogFn = func() []protocol.CommandInfo {
		return []protocol.CommandInfo{
			{Name: "/continue", Usage: "/continue", Description: "continue interrupted turn"},
			{Name: "/set-model", Usage: "/set-model <subscription> <model>", Description: "switch model"},
		}
	}

	got := m.getCommandCompletions("/cont")
	found := false
	for _, command := range got {
		found = found || command == "/continue"
	}
	if !found {
		t.Fatalf("completion = %v, want /continue present", got)
	}

	if help := m.renderHelpPanel(); !strings.Contains(help, "/continue") {
		t.Fatalf("help does not contain provider command /continue:\n%s", help)
	} else {
		plainHelp := strings.ReplaceAll(stripANSI(help), "│", "")
		compactHelp := strings.Join(strings.Fields(plainHelp), "")
		if !strings.Contains(compactHelp, "/set-model<subscription><model>") {
			t.Fatalf("help does not contain provider usage:\n%s", help)
		}
	}

	for _, item := range m.buildPaletteCommands() {
		if item.ActionData == "/continue" || item.ActionData == "/continue " {
			return
		}
	}
	t.Fatal("palette does not contain provider command /continue")
}

func TestLocalCommandCatalogIncludesHandledVisibleCommands(t *testing.T) {
	catalog := make(map[string]struct{})
	for _, command := range ch.TUICommandList() {
		catalog[command.Name] = struct{}{}
	}

	file, err := goparser.ParseFile(token.NewFileSet(), "cli_slash.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	foundDispatch := false
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "handleSlashCommand" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			dispatch, ok := node.(*ast.SwitchStmt)
			if !ok {
				return true
			}
			tag, ok := dispatch.Tag.(*ast.Ident)
			if !ok || tag.Name != "command" {
				return true
			}
			foundDispatch = true
			for _, statement := range dispatch.Body.List {
				clause := statement.(*ast.CaseClause)
				for _, expression := range clause.List {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					name, err := strconv.Unquote(literal.Value)
					if err != nil {
						t.Fatal(err)
					}
					if _, exists := catalog[name]; !exists {
						t.Errorf("visible slash dispatch case %q is missing from TUICommandList", name)
					}
				}
			}
			return false
		})
	}
	if !foundDispatch {
		t.Fatal("handleSlashCommand command dispatch switch not found")
	}
}

func TestDeprecatedCommandNamesProviderStillFeedsCatalog(t *testing.T) {
	m := newCLIModel()
	m.commandNamesFn = func() []string { return []string{"/legacy-plugin"} }

	if got := m.getCommandCompletions("/legacy"); len(got) != 1 || got[0] != "/legacy-plugin" {
		t.Fatalf("completion = %v, want legacy provider command", got)
	}
}

func TestRichPaletteCommandMetadataUsesCatalogSemantics(t *testing.T) {
	m := newCLIModel()
	m.locale = ch.GetLocale("en")

	want := map[string]struct {
		title       string
		description string
	}{
		"clear": {title: "Clear Display", description: "Clear the current TUI display"},
		"new":   {title: "Reset Conversation", description: "Archive memory and reset this conversation"},
	}
	for _, item := range m.buildPaletteCommands() {
		expected, ok := want[item.ID]
		if !ok {
			continue
		}
		if item.Title != expected.title || item.Description != expected.description {
			t.Errorf("palette %s = title %q, description %q", item.ID, item.Title, item.Description)
		}
		delete(want, item.ID)
	}
	if len(want) != 0 {
		t.Fatalf("missing palette command rows: %v", want)
	}
}

func TestCommandCatalogReflectsProviderRemoval(t *testing.T) {
	m := newCLIModel()
	commands := []protocol.CommandInfo{{Name: "/dynamic"}}
	m.commandCatalogFn = func() []protocol.CommandInfo { return commands }
	if got := m.getCommandCompletions("/dyn"); len(got) != 1 {
		t.Fatalf("initial completion = %v, want /dynamic", got)
	}
	if help := m.renderHelpPanel(); !strings.Contains(help, "/dynamic") {
		t.Fatalf("initial help is missing /dynamic:\n%s", help)
	}
	if !paletteHasActionData(m.buildPaletteCommands(), "/dynamic") {
		t.Fatal("initial palette is missing /dynamic")
	}
	commands = nil
	if got := m.getCommandCompletions("/dyn"); len(got) != 0 {
		t.Fatalf("completion after removal = %v, want none", got)
	}
	if help := m.renderHelpPanel(); strings.Contains(help, "/dynamic") {
		t.Fatalf("help after removal still contains /dynamic:\n%s", help)
	}
	if paletteHasActionData(m.buildPaletteCommands(), "/dynamic") {
		t.Fatal("palette after removal still contains /dynamic")
	}
}

func paletteHasActionData(commands []paletteCommand, actionData string) bool {
	for _, command := range commands {
		if strings.TrimSpace(command.ActionData) == actionData {
			return true
		}
	}
	return false
}

func TestPluginContributionKeepsCategoryWithoutCatalogDuplicate(t *testing.T) {
	m := newCLIModel()
	m.commandCatalogFn = func() []protocol.CommandInfo {
		return []protocol.CommandInfo{{Name: "/deploy", Description: "registered deploy command"}}
	}
	m.paletteContributor = func() []PaletteExternalCommand {
		return []PaletteExternalCommand{{
			Title:       "/deploy",
			Description: "plugin manifest description",
			Category:    PaletteCategoryPlugins,
			Content:     "/deploy ",
		}}
	}

	var matches []paletteCommand
	for _, item := range m.buildPaletteCommands() {
		if strings.TrimSpace(item.ActionData) == "/deploy" {
			matches = append(matches, item)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("/deploy palette entries = %#v, want exactly one", matches)
	}
	if matches[0].Category != PaletteCategoryPlugins || matches[0].ActionData != "/deploy " {
		t.Fatalf("/deploy entry = %#v, want manifest category and insertion text preserved", matches[0])
	}
}

// TestPaletteIncludesCatalogSubcommandWithParent guards the present-map logic
// against subcommand collisions: a catalog subcommand ("/context mode") whose
// parent ("/context") is a rich action must still get its own insert-text
// entry, and the parent must not be duplicated.
func TestPaletteIncludesCatalogSubcommandWithParent(t *testing.T) {
	m := newCLIModel()
	m.commandCatalogFn = func() []protocol.CommandInfo {
		return []protocol.CommandInfo{
			{Name: "/context", Usage: "/context", Description: "view context stats"},
			{Name: "/context mode", Usage: "/context mode [phase1|none|default]", Description: "switch compression mode"},
		}
	}

	var insertText []string
	for _, item := range m.buildPaletteCommands() {
		if item.ActionKind == paletteActionInsertText && strings.HasPrefix(item.ActionData, "/context") {
			insertText = append(insertText, item.ActionData)
		}
	}
	if len(insertText) != 1 || insertText[0] != "/context mode" {
		t.Fatalf("catalog subcommand insert entries = %v, want exactly [/context mode]", insertText)
	}

	// The rich "context" action must still be present as the parent sender.
	if !paletteHasActionData(m.buildPaletteCommands(), "/context") {
		t.Fatal("rich palette action for /context is missing")
	}
}
