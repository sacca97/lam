package app

import (
	"strings"
	"testing"
)

func TestSessionWizardProviderScreenHighlightsSelection(t *testing.T) {
	wizard := sessionWizard{
		stage:            wizardProvider,
		workdir:          "/home/me/repo",
		workdirDisplay:   "~/repo",
		sessions:         []sessionInfo{{Name: "codex-repo"}},
		providers:        []string{"claude", "codex", "terminal"},
		selectedProvider: 1,
	}
	lines, row, _ := wizard.screen(nil, 20)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Choose how to start (last used highlighted):") || !strings.Contains(joined, "\033[32m  › Codex\033[0m") || !strings.Contains(joined, "Terminal") {
		t.Fatalf("provider screen does not show the selected provider:\n%s", joined)
	}
	if !strings.Contains(joined, "codex-repo-1 · ~/repo") {
		t.Fatalf("provider screen does not show the generated name:\n%s", joined)
	}
	if row != -1 {
		t.Fatalf("provider screen cursor row = %d, want hidden cursor", row)
	}
}

func TestSessionProviderChoicesIncludesTerminalAndDefaultsSensibly(t *testing.T) {
	choices, selected := sessionProviderChoices([]string{"claude", "codex"}, "codex")
	if selected != 1 || choices[selected] != "codex" || choices[len(choices)-1] != "terminal" {
		t.Fatalf("choices=%v selected=%d; expected codex selected and terminal offered", choices, selected)
	}

	choices, selected = sessionProviderChoices(nil, "claude")
	if len(choices) != 1 || choices[selected] != "terminal" {
		t.Fatalf("choices=%v selected=%d; want terminal as the only available choice", choices, selected)
	}
}

func TestWizardTextEditingPreservesUTF8AndCursor(t *testing.T) {
	input := []byte("a界")
	input, cursor := insertWizardText(input, 1, "x")
	if got := string(input); got != "ax界" || cursor != 2 {
		t.Fatalf("insertWizardText = %q at %d, want ax界 at 2", got, cursor)
	}
	input, cursor = backspaceWizardText(input, cursor)
	if got := string(input); got != "a界" || cursor != 1 {
		t.Fatalf("backspaceWizardText = %q at %d, want a界 at 1", got, cursor)
	}
	input, cursor = deleteWizardText(input, cursor)
	if got := string(input); got != "a" || cursor != 1 {
		t.Fatalf("deleteWizardText = %q at %d, want a at 1", got, cursor)
	}
}

func TestInsertWizardTextIsBounded(t *testing.T) {
	input, cursor := []byte(nil), 0
	for i := 0; i < 1_000_000; i++ {
		input, cursor = insertWizardText(input, cursor, "x")
	}
	if len(input) > maxFieldBytes || cursor != len(input) {
		t.Fatalf("field grew to %d bytes, cursor %d", len(input), cursor)
	}
}
