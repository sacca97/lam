package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type sessionDirectorySource interface {
	WorkingDirectory() (string, error)
	HomeDirectory() (string, error)
	ResolveSessionDir(input, base string) (string, error)
}

type sessionDirectoryCompleter interface {
	CompleteSessionDir(input, base string) ([]string, error)
}

type localDirectorySource struct{}

func (localDirectorySource) WorkingDirectory() (string, error) { return os.Getwd() }
func (localDirectorySource) HomeDirectory() (string, error)    { return os.UserHomeDir() }
func (localDirectorySource) ResolveSessionDir(input, base string) (string, error) {
	return resolveSessionDir(input, base)
}
func (localDirectorySource) CompleteSessionDir(input, base string) ([]string, error) {
	return completeSessionDirs(input, base)
}

func completeSessionDirs(input, base string) ([]string, error) {
	if input == "~" {
		if _, err := resolveSessionDir(input, base); err == nil {
			return []string{"~/"}, nil
		}
	}
	separator := string(filepath.Separator)
	separatorAt := strings.LastIndex(input, separator)
	dirPart, partial := "", input
	if separatorAt >= 0 {
		dirPart, partial = input[:separatorAt+1], input[separatorAt+1:]
	}
	parentInput := strings.TrimSuffix(dirPart, separator)
	if parentInput == "" && strings.HasPrefix(dirPart, separator) {
		parentInput = separator
	}
	parent := base
	if parentInput != "" {
		var err error
		parent, err = resolveSessionDir(parentInput, base)
		if err != nil {
			return nil, err
		}
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return nil, err
	}
	prefix := dirPart
	matches := make([]string, 0)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), partial) {
			continue
		}
		if partial == "" && !strings.HasPrefix(input, ".") && strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		info, err := os.Stat(filepath.Join(parent, entry.Name()))
		if err != nil || !info.IsDir() {
			continue
		}
		matches = append(matches, prefix+entry.Name()+separator)
	}
	return matches, nil
}

func formatDirectoryCompletions(input string, matches []string) string {
	return strings.Join(directoryCompletionLabels(input, matches), "  ")
}

func directoryCompletionLabels(input string, matches []string) []string {
	parentPrefix := ""
	if separatorAt := strings.LastIndex(input, string(filepath.Separator)); separatorAt >= 0 {
		parentPrefix = input[:separatorAt+1]
	}
	options := make([]string, 0, len(matches))
	for _, match := range matches {
		options = append(options, strings.TrimPrefix(match, parentPrefix))
	}
	return options
}

func commonDirectoryPrefix(matches []string) string {
	if len(matches) == 0 {
		return ""
	}
	prefix := []rune(matches[0])
	for _, match := range matches[1:] {
		runes := []rune(match)
		i := 0
		for i < len(prefix) && i < len(runes) && prefix[i] == runes[i] {
			i++
		}
		prefix = prefix[:i]
	}
	return string(prefix)
}

func nextUTF8(input []byte, at int) int {
	for at < len(input) && input[at]&0xc0 == 0x80 {
		at++
	}
	if at < len(input) {
		at++
		for at < len(input) && input[at]&0xc0 == 0x80 {
			at++
		}
	}
	return at
}

func shortenHomePath(path string, source sessionDirectorySource) string {
	home, err := source.HomeDirectory()
	if err != nil || home == "" {
		return path
	}
	home = filepath.Clean(home)
	path = filepath.Clean(path)
	if path == home {
		return "~"
	}
	rel, err := filepath.Rel(home, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return path
	}
	return filepath.Join("~", rel)
}

func normalizedSessionDir(start string, source sessionDirectorySource) string {
	if dir, err := source.ResolveSessionDir(start, ""); err == nil {
		return dir
	}
	if cwd, err := source.WorkingDirectory(); err == nil {
		return cwd
	}
	if home, err := source.HomeDirectory(); err == nil {
		return home
	}
	return string(filepath.Separator)
}

// resolveSessionDir expands ~/ and resolves relative paths from the selected
// starting directory rather than from wherever LAM happens to be running.
func resolveSessionDir(input, base string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		input = base
	}
	if input == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		input = home
	} else if strings.HasPrefix(input, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		input = filepath.Join(home, strings.TrimPrefix(input, "~/"))
	}
	if !filepath.IsAbs(input) {
		if base == "" {
			var err error
			base, err = os.Getwd()
			if err != nil {
				return "", err
			}
		}
		input = filepath.Join(base, input)
	}
	abs, err := filepath.Abs(input)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%q is not an existing directory", input)
	}
	return abs, nil
}
