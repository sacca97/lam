package app

import (
	"fmt"
	"os"
	"strings"
)

// version is set at build time via -ldflags.
var version = "dev"

// Run executes the LAM command line interface.
func Run() {
	// Subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "daemon":
			runDaemon()
			return
		case "install-service":
			if err := installService(); err != nil {
				fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "uninstall-service":
			if err := uninstallService(); err != nil {
				fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "update":
			if err := updateCmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "lam update: %v\n", err)
				os.Exit(1)
			}
			return
		case "host":
			// Internal: runs one session's host process (see host.go).
			if len(os.Args) != 5 {
				fmt.Fprintln(os.Stderr, "usage: lam host <name> <cwd> <command>")
				os.Exit(2)
			}
			if err := runHost(os.Args[2], os.Args[3], os.Args[4]); err != nil {
				fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "attach":
			if len(os.Args) != 2 && len(os.Args) != 3 {
				fmt.Fprintln(os.Stderr, "usage: lam attach [SESSION]")
				os.Exit(2)
			}
			if err := attachCmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
				os.Exit(1)
			}
			return
		case "ls", "ps", "list", "tail", "send":
			verbs := map[string]func([]string) error{"ls": lsCmd, "ps": psCmd, "list": psCmd, "tail": tailCmd, "send": sendCmd}
			if err := verbs[os.Args[1]](os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "lam %s: %v\n", os.Args[1], err)
				os.Exit(1)
			}
			return
		case "restore":
			if err := restoreCmd(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "lam restore: %v\n", err)
				os.Exit(1)
			}
			return
		case "start":
			startSessionCmd(os.Args[2:])
			return
		case "new":
			newSessionCmd(os.Args[2:])
			return
		case "ssh":
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: lam ssh <host> [--prompt-me]")
				os.Exit(2)
			}
			promptMe := false
			for _, arg := range os.Args[3:] {
				if arg == "--prompt-me" {
					promptMe = true
				} else {
					fmt.Fprintf(os.Stderr, "unknown lam ssh option %q\n", arg)
					os.Exit(2)
				}
			}
			if err := runSSH(os.Args[2], promptMe); err != nil {
				fmt.Fprintf(os.Stderr, "  SSH: %v\n", err)
				os.Exit(1)
			}
			return
		case "remote":
			// The other end of `lam ssh`: speaks the protocol on stdin and stdout.
			if len(os.Args) != 2 && !(len(os.Args) == 3 && os.Args[2] == "--stdio") {
				fmt.Fprintln(os.Stderr, "usage: lam remote [--stdio] (run by `lam ssh` on the remote host)")
				os.Exit(2)
			}
			if err := serveRemote(os.Stdin, os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "remote: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	// Flags
	promptMe := false
	startDir := ""
	for _, arg := range os.Args[1:] {
		if !strings.HasPrefix(arg, "-") && startDir == "" {
			dir, err := resolveDir(arg)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  Error: %v\n", err)
				os.Exit(1)
			}
			startDir = dir
			continue
		}
		if arg == "--prompt-me" {
			promptMe = true
		}
		if arg == "--version" || arg == "-v" {
			fmt.Println("lam", version)
			return
		}
		if arg == "--help" || arg == "-h" {
			printHelp()
			return
		}
	}

	runIDE(promptMe, startDir)
}

func printHelp() {
	fmt.Println("Usage: lam [dir] [flags]")
	fmt.Println()
	fmt.Println("Arguments:")
	fmt.Println("  dir                 Folder new sessions open in (default: current directory)")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  daemon              Run background unread detection service")
	fmt.Println("  install-service     Install and start as system service")
	fmt.Println("  uninstall-service   Stop and remove system service")
	fmt.Println("  update [--check] [VERSION]  Install the latest GitHub release (checksum verified)")
	fmt.Println("  attach [SESSION]    Attach to a session (Ctrl+Space or Ctrl+\\ detaches); with no argument, the only or last-selected session; '.' means the session in this directory")
	fmt.Println("  start [PROVIDER] [PATH] [--name NAME] [--cmd ARG...]")
	fmt.Println("                      Create a session and open LAM focused on it; provider defaults to shell")
	fmt.Println("  new                 Alias for start; accepts the same arguments")
	fmt.Println("  ls [--json]         List sessions (name and directory, or JSON)")
	fmt.Println("  ps [--json]         List sessions with state, PID, age and directory (list is an alias)")
	fmt.Println("  tail <session> [-f] Print session scrollback and screen as plain text")
	fmt.Println("  send <session> <text>  Type text into a session and press Enter")
	fmt.Println("  restore [--list]    Reopen sessions that ended with a reboot or crash, resuming their agents")
	fmt.Println("  ssh <host>          Open this UI for sessions on an SSH host")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --prompt-me         Ask for a starting prompt when creating sessions")
	fmt.Println("  --version           Print version and exit")
}
