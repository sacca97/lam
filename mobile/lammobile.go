// Package lammobile is lam's client for phones, shaped for gomobile bind:
// simple types, byte slices and callback interfaces. It connects to a host
// over the built-in SSH client, lists sessions and attaches to one, delivering
// raw terminal output and taking raw input.
package lammobile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sacca97/lam/internal/app"
	"golang.org/x/crypto/ssh"
)

var (
	dirMu   sync.Mutex
	dataDir string
)

// SetDataDir sets the app's private directory, where the device key and
// known_hosts live. Call it before anything else.
func SetDataDir(dir string) error {
	dirMu.Lock()
	defer dirMu.Unlock()
	if err := os.MkdirAll(filepath.Join(dir, "ssh"), 0o700); err != nil {
		return err
	}
	dataDir = dir
	_ = os.Setenv("HOME", dir)
	if os.Getenv("TMPDIR") == "" {
		tmp := filepath.Join(dir, "tmp")
		if os.MkdirAll(tmp, 0o700) == nil {
			_ = os.Setenv("TMPDIR", tmp)
		}
	}
	return nil
}

func keyPath() (string, error) {
	dirMu.Lock()
	defer dirMu.Unlock()
	if dataDir == "" {
		return "", errors.New("lammobile: SetDataDir was not called")
	}
	return filepath.Join(dataDir, "ssh", "id_ed25519"), nil
}

// PublicKey returns the device key as an authorized_keys line, creating the
// key on first use.
func PublicKey() (string, error) {
	path, err := keyPath()
	if err != nil {
		return "", err
	}
	if data, err := os.ReadFile(path + ".pub"); err == nil {
		return strings.TrimSpace(string(data)), nil
	}
	return app.GenerateSSHKey(path, "lam-android")
}

// HostPrompt is answered by the app when a host is not in known_hosts.
type HostPrompt interface {
	// ConfirmHost returns true to trust the key (fingerprint is SHA256:...).
	ConfirmHost(host, keyType, fingerprint string) bool
	// RequestPassword is called when the device key was not accepted. Return
	// the account password, or an empty string to cancel.
	RequestPassword(user, host string) string
}

// Session is one session on the host.
type Session struct {
	Name     string
	Folder   string
	Command  string
	Active   bool // producing output now
	Unread   bool
	Attached int // clients attached
}

// SessionList is a list of sessions (gomobile cannot return slices of structs).
type SessionList struct{ items []*Session }

// Len is the number of sessions.
func (l *SessionList) Len() int { return len(l.items) }

// Get returns session i, or nil.
func (l *SessionList) Get(i int) *Session {
	if i < 0 || i >= len(l.items) {
		return nil
	}
	return l.items[i]
}

// Client is a connection to one host.
type Client struct {
	mu     sync.Mutex
	link   *app.MobileLink
	target string
	// password is kept in memory only, so Reconnect needs no prompt.
	password string
}

func (c *Client) cur() *app.MobileLink {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.link
}

func sshOptions(prompt HostPrompt, password *string) (app.SSHOptions, error) {
	key, err := keyPath()
	if err != nil {
		return app.SSHOptions{}, err
	}
	if _, err := PublicKey(); err != nil {
		return app.SSHOptions{}, err
	}
	opts := app.SSHOptions{
		Paths: app.SSHPaths{
			KnownHostsPath: filepath.Join(filepath.Dir(key), "known_hosts"),
			IdentityFiles:  []string{key},
			DisableConfig:  true,
		},
		DisableAgent:   true,
		ConnectTimeout: 2 * time.Minute, // covers answering the questions
	}
	if prompt != nil {
		opts.ConfirmHost = func(host string, k ssh.PublicKey) (bool, error) {
			return prompt.ConfirmHost(host, k.Type(), ssh.FingerprintSHA256(k)), nil
		}
		opts.Password = func(user, host string) (string, error) {
			p := prompt.RequestPassword(user, host)
			if p == "" {
				return "", errors.New("password entry cancelled")
			}
			*password = p
			return p, nil
		}
	} else if *password != "" {
		// Unattended reconnect: reuse the password given at login.
		opts.Password = func(string, string) (string, error) { return *password, nil }
	}
	return opts, nil
}

// Connect logs in to target (user@host, user@host:port) with the device key
// and runs lam on the host. It blocks, including while prompt is asked.
func Connect(target string, prompt HostPrompt) (*Client, error) {
	c := &Client{target: target}
	opts, err := sshOptions(prompt, &c.password)
	if err != nil {
		return nil, err
	}
	link, err := app.MobileConnectSSH(target, opts)
	if err != nil {
		return nil, err
	}
	c.link = link
	return c, nil
}

// Reconnect dials the host again without asking anything (known host, device
// key, or the password given at login) and replaces the connection. Sessions
// attached on the old connection have to be attached again.
func (c *Client) Reconnect() error {
	if c.target == "" {
		return errors.New("not an ssh connection")
	}
	pw := c.password
	opts, err := sshOptions(nil, &pw)
	if err != nil {
		return err
	}
	link, err := app.MobileConnectSSH(c.target, opts)
	if err != nil {
		return err
	}
	c.mu.Lock()
	old := c.link
	c.link = link
	c.mu.Unlock()
	old.Close()
	return nil
}

// Sessions lists the host's sessions.
func (c *Client) Sessions() (*SessionList, error) {
	list, err := c.cur().Sessions()
	if err != nil {
		return nil, err
	}
	out := &SessionList{}
	for _, s := range list {
		out.items = append(out.items, &Session{Name: s.Name, Folder: s.CWD, Command: s.Command,
			Active: s.Activity == "active", Unread: s.Unread, Attached: s.Clients})
	}
	return out, nil
}

// Launchers lists the agents installed on the host, one per line.
func (c *Client) Launchers() (string, error) {
	list, err := c.cur().Launchers()
	return strings.Join(list, "\n"), err
}

// Home is the host's home directory.
func (c *Client) Home() (string, error) { return c.cur().Home() }

// CompleteDir lists host directories that continue input, one per line.
func (c *Client) CompleteDir(input, base string) (string, error) {
	list, err := c.cur().CompleteDir(input, base)
	return strings.Join(list, "\n"), err
}

// CreateSession starts a session on the host and returns its name; launcher
// "terminal" starts a shell, and an empty dir means the host's home.
func (c *Client) CreateSession(launcher, dir string) (string, error) {
	return c.cur().CreateSession(launcher, dir)
}

// Alive reports whether the connection is still up.
func (c *Client) Alive() bool {
	select {
	case <-c.cur().Done():
		return false
	default:
		return true
	}
}

// Close ends the connection.
func (c *Client) Close() { c.cur().Close() }

// Output receives what an attached session produces.
type Output interface {
	// OnOutput gets raw terminal bytes (the first call redraws the screen).
	OnOutput(data []byte)
	// OnClosed is called once when the session or connection ends.
	OnClosed(reason string)
}

// Terminal is an attached session.
type Terminal struct {
	a *app.MobileAttach
}

// Attach attaches to a session at the given size and starts delivering output.
func (c *Client) Attach(name string, cols, rows int, out Output) (*Terminal, error) {
	a, err := c.cur().Attach(name, cols, rows)
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			data, err := a.ReadOutput()
			if err != nil {
				out.OnClosed(err.Error())
				return
			}
			out.OnOutput(data)
		}
	}()
	return &Terminal{a: a}, nil
}

// Write sends raw input (typed bytes) to the session.
func (t *Terminal) Write(data []byte) error { return t.a.WriteInput(data) }

// Resize tells the session the terminal size.
func (t *Terminal) Resize(cols, rows int) error { return t.a.Resize(cols, rows) }

// Close detaches from the session.
func (t *Terminal) Close() { t.a.Close() }
