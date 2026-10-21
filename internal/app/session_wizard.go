package app

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	wizardPrompt   = "prompt"
	wizardWorkdir  = "workdir"
	wizardProvider = "provider"
)

type sessionWizard struct {
	stage             string
	sessions          []sessionInfo // existing sessions, which the generated name must not collide with
	prompt            string
	defaultDir        string
	defaultDirDisplay string
	workdir           string
	workdirDisplay    string
	pathInput         []byte
	cursor            int
	providers         []string
	selectedProvider  int
	completionMatches []string
	lastCompletion    string
	showCompletions   bool
	error             string
	promptEnabled     bool
}

func (w sessionWizard) screen(input []byte, maxRows int) (lines []string, cursorRow, cursorCol int) {
	cursorRow, cursorCol = -1, 0
	if maxRows <= 0 {
		return nil, cursorRow, cursorCol
	}
	if w.stage == wizardProvider {
		lines = []string{
			"New session",
			fmt.Sprintf("  %s · %s", w.nameFor(w.selectedProvider), w.workdirDisplay),
			"",
			"Choose how to start (last used highlighted):",
		}
		for i, provider := range w.providers {
			if i == w.selectedProvider {
				lines = append(lines, "\033[32m  › "+launcherLabel(provider)+"\033[0m")
			} else {
				lines = append(lines, "    "+launcherLabel(provider))
			}
		}
		if w.selectedProvider >= 0 && w.selectedProvider < len(w.providers) && w.providers[w.selectedProvider] == "terminal" && strings.TrimSpace(w.prompt) != "" {
			lines = append(lines, "", "Starting prompts are only used by agent providers.")
		}
		lines = append(lines, "", "↑/↓ choose · Enter create · Esc back")
		if w.error != "" {
			lines = append(lines, "Error: "+w.error)
		}
		return capWizardLines(lines, maxRows), cursorRow, cursorCol
	}

	var fieldLabel string
	switch w.stage {
	case wizardPrompt:
		fieldLabel = "Starting prompt (agent only, optional): "
	case wizardWorkdir:
		fieldLabel = "Workdir [" + w.defaultDirDisplay + "]: "
	default:
		return nil, cursorRow, cursorCol
	}
	lines = []string{"New session", "", fieldLabel + string(input)}
	cursorRow = len(lines) - 1
	position := min(w.cursor, len(input))
	cursorCol = ansi.StringWidth(fieldLabel + string(input[:position]))
	switch w.stage {
	case wizardPrompt:
		lines = append(lines, "Enter continue · Esc cancel")
	case wizardWorkdir:
		lines = append(lines,
			"Enter uses the default · relative paths start here",
			"Tab completes directories · Enter continue · Esc back",
		)
		if w.showCompletions && len(w.completionMatches) > 0 {
			lines = append(lines, "Matching directories:")
			options := directoryCompletionLabels(string(input), w.completionMatches)
			for i, option := range options {
				lines = append(lines, "  "+option)
				if len(lines) >= maxRows && i < len(options)-1 {
					lines[len(lines)-1] = fmt.Sprintf("  … %d more", len(options)-i-1)
					break
				}
			}
		}
	}
	if w.error != "" {
		lines = append(lines, "Error: "+w.error)
	}
	if cursorRow >= maxRows {
		cursorRow = -1
	}
	return capWizardLines(lines, maxRows), cursorRow, cursorCol
}

// nameFor is the name the session would get with the provider at index i: the
// tool, then the last folder of the working directory (see generatedSessionName).
func (w sessionWizard) nameFor(i int) string {
	provider := "terminal"
	if i >= 0 && i < len(w.providers) {
		provider = w.providers[i]
	}
	return launcherSessionName(provider, w.workdir, w.sessions)
}

func sessionProviderChoices(available []string, lastUsed string) ([]string, int) {
	choices := append([]string(nil), available...)
	choices = append(choices, "terminal")
	preferred := preferredLauncher(available, lastUsed)
	if preferred == "" {
		return choices, len(choices) - 1
	}
	for i, choice := range choices {
		if choice == preferred {
			return choices, i
		}
	}
	return choices, 0
}

func capWizardLines(lines []string, maxRows int) []string {
	if len(lines) > maxRows {
		return lines[:maxRows]
	}
	return lines
}

// maxFieldBytes bounds a one-line field (directory, name, filter). Each insert
// copies the field, so a huge paste would otherwise freeze the interface.
const maxFieldBytes = 4096

func insertWizardText(input []byte, cursor int, value string) ([]byte, int) {
	cursor = max(0, min(cursor, len(input)))
	if len(input)+len(value) > maxFieldBytes {
		return input, cursor
	}
	updated := make([]byte, 0, len(input)+len(value))
	updated = append(updated, input[:cursor]...)
	updated = append(updated, value...)
	updated = append(updated, input[cursor:]...)
	return updated, cursor + len(value)
}

func backspaceWizardText(input []byte, cursor int) ([]byte, int) {
	cursor = max(0, min(cursor, len(input)))
	if cursor == 0 {
		return input, cursor
	}
	prefix := popUTF8(input[:cursor])
	updated := append(prefix, input[cursor:]...)
	return updated, len(prefix)
}

func deleteWizardText(input []byte, cursor int) ([]byte, int) {
	cursor = max(0, min(cursor, len(input)))
	if cursor >= len(input) {
		return input, cursor
	}
	return append(input[:cursor], input[nextUTF8(input, cursor):]...), cursor
}

func deleteWizardWord(input []byte, cursor int) ([]byte, int) {
	cursor = max(0, min(cursor, len(input)))
	end := cursor
	for cursor > 0 && input[cursor-1] == ' ' {
		cursor = len(popUTF8(input[:cursor]))
	}
	for cursor > 0 && input[cursor-1] != ' ' {
		cursor = len(popUTF8(input[:cursor]))
	}
	updated := append(input[:cursor], input[end:]...)
	return updated, cursor
}

func commonCompletion(input string, matches []string) string {
	common := commonDirectoryPrefix(matches)
	if strings.HasPrefix(common, input) && len(common) > len(input) {
		return common
	}
	return input
}
