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
			var err error
			listenConfigured := false
			listenerDisabled := false
			listenerAddress := ""
			switch {
			case len(os.Args) == 2 || len(os.Args) == 3 && os.Args[2] == "--stdio":
				err = serveRemote(os.Stdin, os.Stdout)
			case len(os.Args) == 3 && os.Args[2] == "--listen":
				listenerAddress, _, err = enableRemoteListener("")
				listenConfigured = err == nil
			case len(os.Args) == 4 && os.Args[2] == "--listen":
				listenerAddress, _, err = enableRemoteListener(os.Args[3])
				listenConfigured = err == nil
			case len(os.Args) == 3 && os.Args[2] == "--disable":
				_, _, err = disableRemoteListener()
				listenerDisabled = err == nil
			case len(os.Args) == 3:
				if err := checkInteractiveTerminal(); err != nil {
					fmt.Fprintf(os.Stderr, "lam remote: %v\n", err)
					os.Exit(1)
				}
				err = runTCPRemote(os.Args[2], false)
			default:
				fmt.Fprintf(os.Stderr, "usage: lam remote <ssh-host[:port]> | lam remote --listen [bind-address[:port]] | lam remote --disable (default port %s)\n", defaultRemoteTCPPort)
				os.Exit(2)
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "remote: %v\n", err)
				os.Exit(1)
			}
			if listenConfigured {
				fmt.Fprintf(os.Stdout, "Remote TCP listener configured on %s.\n", listenerAddress)
				if port, allInterfaces := remoteListenPort(listenerAddress); allInterfaces {
					fmt.Fprintf(os.Stdout, "It accepts connections on all interfaces; restrict port %s to your VPN in the firewall.\n", port)
				}
				if !daemonServiceInstalled() {
					fmt.Fprintln(os.Stdout, "Install the background daemon with `lam install-service` to keep the listener active.")
				}
			}
			if listenerDisabled {
				fmt.Fprintln(os.Stdout, "Remote TCP listener disabled.")
			}
			return
		case "remote-ticket":
			if len(os.Args) != 2 {
				fmt.Fprintln(os.Stderr, "usage: lam remote-ticket")
				os.Exit(2)
			}
			if err := printRemoteTCPAuthTicket(); err != nil {
				fmt.Fprintf(os.Stderr, "remote ticket: %v\n", err)
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
	fmt.Printf("  remote <host[:port]>  Open this UI over VPN TCP after SSH authentication (default port %s)\n", defaultRemoteTCPPort)
	fmt.Printf("  remote --listen [IP[:port]]  Enable the daemon listener (all interfaces by default, port %s)\n", defaultRemoteTCPPort)
	fmt.Println("  remote --disable    Disable the daemon's TCP listener")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --prompt-me         Ask for a starting prompt when creating sessions")
	fmt.Println("  --version           Print version and exit")
}
