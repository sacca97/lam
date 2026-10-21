package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const remoteTicketKeyBytes = 32

func remoteConfigDir() string {
	config, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		config = filepath.Join(home, ".config")
	}
	return filepath.Join(config, "lam")
}

func remoteListenConfigPath() string { return filepath.Join(remoteConfigDir(), "remote-listen") }
func remoteTicketKeyPath() string    { return filepath.Join(remoteConfigDir(), "remote-ticket-key") }

func loadRemoteListenConfig() (string, error) {
	data, err := os.ReadFile(remoteListenConfigPath())
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return normalizeRemoteListenAddress(strings.TrimSpace(string(data)))
}

func saveRemoteListenConfig(address string) (string, error) {
	address, err := normalizeRemoteListenAddress(address)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(remoteConfigDir(), 0o700); err != nil {
		return "", fmt.Errorf("create remote config directory: %w", err)
	}
	if err := os.WriteFile(remoteListenConfigPath(), []byte(address+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("save remote listener address: %w", err)
	}
	return address, nil
}

func clearRemoteListenConfig() (bool, error) {
	err := os.Remove(remoteListenConfigPath())
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("remove remote listener address: %w", err)
	}
	return true, nil
}

func enableRemoteListener(address string) (string, bool, error) {
	address, err := normalizeRemoteListenAddress(address)
	if err != nil {
		return "", false, err
	}
	if _, err := ensureRemoteTicketKey(); err != nil {
		return "", false, err
	}
	address, err = saveRemoteListenConfig(address)
	if err != nil {
		return "", false, err
	}
	if !daemonServiceInstalled() {
		return address, false, nil
	}
	if err := restartDaemonService(); err != nil {
		return address, true, err
	}
	return address, true, nil
}

func disableRemoteListener() (bool, bool, error) {
	changed, err := clearRemoteListenConfig()
	if err != nil || !changed || !daemonServiceInstalled() {
		return changed, false, err
	}
	if err := restartDaemonService(); err != nil {
		return changed, true, err
	}
	return changed, true, nil
}

func ensureRemoteTicketKey() ([]byte, error) {
	if key, err := loadRemoteTicketKey(); err == nil {
		return key, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(remoteConfigDir(), 0o700); err != nil {
		return nil, fmt.Errorf("create remote config directory: %w", err)
	}
	key := make([]byte, remoteTicketKeyBytes)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate remote ticket signing key: %w", err)
	}
	file, err := os.OpenFile(remoteTicketKeyPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return loadRemoteTicketKey()
	}
	if err != nil {
		return nil, fmt.Errorf("create remote ticket signing key: %w", err)
	}
	if _, err := file.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		file.Close()
		os.Remove(remoteTicketKeyPath())
		return nil, fmt.Errorf("write remote ticket signing key: %w", err)
	}
	if err := file.Close(); err != nil {
		os.Remove(remoteTicketKeyPath())
		return nil, fmt.Errorf("close remote ticket signing key: %w", err)
	}
	return key, nil
}

func loadRemoteTicketKey() ([]byte, error) {
	path := remoteTicketKeyPath()
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("remote ticket signing key %s must be a regular file with mode 0600", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != remoteTicketKeyBytes {
		return nil, fmt.Errorf("invalid remote ticket signing key in %s", path)
	}
	return key, nil
}
