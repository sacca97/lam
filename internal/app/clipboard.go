package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os/exec"
	"strings"
	"time"
)

type clipboardCommand struct {
	name string
	args []string
}

var systemClipboardCommands = []clipboardCommand{
	{name: "wl-copy"},
	{name: "xclip", args: []string{"-selection", "clipboard"}},
	{name: "xsel", args: []string{"--clipboard", "--input"}},
	{name: "pbcopy"},
	{name: "clip.exe"},
}

// copyToSystemClipboard targets the machine running the LAM UI, including
// when the selected terminal session is remote.
func copyToSystemClipboard(text string) bool {
	for _, candidate := range systemClipboardCommands {
		path, err := exec.LookPath(candidate.name)
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		command := exec.CommandContext(ctx, path, candidate.args...)
		command.Stdin = strings.NewReader(text)
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		// wl-copy and xclip leave a child serving the selection that keeps the
		// output pipes open; without this Run would wait for it indefinitely.
		command.WaitDelay = 200 * time.Millisecond
		err = command.Run()
		cancel()
		if err == nil {
			return true
		}
	}
	return false
}

// osc52Text extracts the text of an OSC 52 clipboard write ("52;c;<base64>").
// Clipboard queries ("?") and malformed payloads are ignored.
func osc52Text(data []byte) (string, bool) {
	parts := bytes.SplitN(data, []byte{';'}, 3)
	if len(parts) != 3 || string(parts[2]) == "?" {
		return "", false
	}
	text, err := base64.StdEncoding.DecodeString(string(parts[2]))
	if err != nil || len(text) == 0 {
		return "", false
	}
	return string(text), true
}
