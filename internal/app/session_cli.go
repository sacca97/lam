package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type cliSessionRequest struct {
	Provider string
	Path     string
	Name     string
	Command  []string
}

func parseSessionStartArgs(args []string) (cliSessionRequest, error) {
	var request cliSessionRequest
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--path", "--name":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return request, fmt.Errorf("%s requires a value", arg)
			}
			i++
			value := args[i]
			switch arg {
			case "--path":
				if request.Path != "" {
					return request, fmt.Errorf("path specified more than once")
				}
				request.Path = value
			case "--name":
				if request.Name != "" {
					return request, fmt.Errorf("name specified more than once")
				}
				request.Name = strings.TrimSpace(value)
			}
		case "--cmd":
			if i+1 >= len(args) {
				return request, fmt.Errorf("--cmd requires a command")
			}
			request.Command = append(request.Command, args[i+1:]...)
			return request, nil
		default:
			if strings.HasPrefix(arg, "-") {
				return request, fmt.Errorf("unknown option %q", arg)
			}
			if isSessionProvider(arg) {
				if request.Provider != "" {
					return request, fmt.Errorf("provider specified more than once")
				}
				request.Provider = arg
				continue
			}
			if request.Path != "" {
				return request, fmt.Errorf("unexpected positional argument %q", arg)
			}
			request.Path = arg
		}
	}
	return request, nil
}

func isSessionProvider(value string) bool {
	for _, provider := range supportedLaunchers {
		if value == provider {
			return true
		}
	}
	return false
}

func startSessionCmd(args []string) {
	request, err := parseSessionStartArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lam start: %v\n%s\n", err, sessionStartUsage())
		os.Exit(2)
	}
	if err := checkInteractiveTerminal(); err != nil {
		fmt.Fprintf(os.Stderr, "lam start: %v\n", err)
		os.Exit(1)
	}
	created, err := createCLISession(request)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lam start: %v\n", err)
		os.Exit(1)
	}
	if err := runIDEWithInitialSession(false, created.CWD, created.Name); err != nil {
		if cleanupErr := killSession(created.Name); cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "lam start: %v (could not remove session %q: %v)\n", err, created.Name, cleanupErr)
		} else {
			fmt.Fprintf(os.Stderr, "lam start: %v\n", err)
		}
		os.Exit(1)
	}
}

func newSessionCmd(args []string) { startSessionCmd(args) }

func sessionStartUsage() string {
	return "Usage: lam start [PROVIDER] [PATH] [--name NAME] [--cmd ARG...]\n       lam new   (alias for lam start)\nCreates a persistent session and opens LAM focused on it. PATH defaults to the current directory. PROVIDER is optional; use --cmd to pass arguments to the provider or to run a generic command. --cmd consumes all remaining arguments."
}

func createCLISession(request cliSessionRequest) (sessionInfo, error) {
	path := request.Path
	if path == "" {
		path = "."
	}
	cwd, err := resolveDir(path)
	if err != nil {
		return sessionInfo{}, err
	}

	commandArgs := make([]string, 0, len(request.Command)+1)
	if request.Provider != "" {
		if !isSessionProvider(request.Provider) {
			return sessionInfo{}, fmt.Errorf("unsupported provider %q", request.Provider)
		}
		commandArgs = append(commandArgs, request.Provider)
	}
	commandArgs = append(commandArgs, request.Command...)
	shell := request.Provider == "" && len(request.Command) == 0
	command := sessionCommand(commandArgs)
	nameKind := "session"
	if shell {
		nameKind = "shell"
	} else if request.Provider != "" {
		nameKind = request.Provider
	}

	name := strings.TrimSpace(request.Name)
	if name == "" {
		sessions, err := listSessionsWithError()
		if err != nil {
			return sessionInfo{}, fmt.Errorf("list sessions: %w", err)
		}
		name = generatedSessionName(nameKind, cwd, sessions)
	}
	if err := validSessionName(name); err != nil {
		return sessionInfo{}, err
	}
	if err := newSession(name, cwd, command); err != nil {
		return sessionInfo{}, fmt.Errorf("create session: %w", err)
	}
	if request.Provider != "" {
		setLauncher(request.Provider)
	}
	rememberSelectedSession(name)
	return sessionInfo{Name: name, CWD: cwd, Command: command, Shell: shell}, nil
}

func sessionCommand(args []string) string {
	if len(args) == 0 {
		return fallbackShellCommand
	}
	if len(args) == 1 {
		return args[0]
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

// launcherSessionName names a session started with a launcher from the tool
// and folder, such as claude-os-findmy. A plain terminal is called "shell".
func launcherSessionName(launcher, cwd string, sessions []sessionInfo) string {
	if launcher == "terminal" {
		launcher = "shell"
	}
	return generatedSessionName(launcher, cwd, sessions)
}

// generatedSessionName is kind plus the last folder of cwd, with -1, -2, ...
// added when a session of that name exists.
func generatedSessionName(kind, cwd string, sessions []sessionInfo) string {
	if kind == "" {
		kind = "session"
	}
	base := filepath.Base(filepath.Clean(cwd))
	if base == string(os.PathSeparator) {
		base = "root"
	}
	base = sessionNameSlug(base)
	if base == "" {
		base = "project"
	}
	if len(base) > 48 {
		base = strings.Trim(base[:48], "-")
	}
	prefix := kind + "-" + base
	used := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		used[session.Name] = struct{}{}
	}
	for suffix := 0; ; suffix++ {
		name := prefix
		if suffix > 0 {
			name = fmt.Sprintf("%s-%d", prefix, suffix)
		}
		if _, exists := used[name]; !exists {
			return name
		}
	}
}

func sessionNameSlug(value string) string {
	var b strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
