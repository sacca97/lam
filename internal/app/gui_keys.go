//go:build gui

package app

import (
	"fmt"
	"strings"

	"gioui.org/io/key"
	gh "go.mitchellh.com/libghostty"
)

// keyBytes maps a Gio key event to the bytes a terminal program expects.
// Plain printable text is not handled here: it arrives as key.EditEvent.
func keyBytes(name key.Name, mods key.Modifiers) ([]byte, bool) {
	shift := mods.Contain(key.ModShift)
	ctrl := mods.Contain(key.ModCtrl)
	alt := mods.Contain(key.ModAlt)
	// xterm modifier parameter: 1 + shift(1) + alt(2) + ctrl(4)
	mod := 1
	if shift {
		mod += 1
	}
	if alt {
		mod += 2
	}
	if ctrl {
		mod += 4
	}
	csi := func(final string) []byte {
		if mod == 1 {
			return []byte("\x1b[" + final)
		}
		return []byte(fmt.Sprintf("\x1b[1;%d%s", mod, final))
	}
	tilde := func(n int) []byte {
		if mod == 1 {
			return []byte(fmt.Sprintf("\x1b[%d~", n))
		}
		return []byte(fmt.Sprintf("\x1b[%d;%d~", n, mod))
	}
	withAlt := func(b []byte) []byte {
		if alt {
			return append([]byte{0x1b}, b...)
		}
		return b
	}
	switch name {
	case key.NameReturn, key.NameEnter:
		return withAlt([]byte{'\r'}), true
	case key.NameDeleteBackward:
		if ctrl {
			return withAlt([]byte{0x08}), true
		}
		return withAlt([]byte{0x7f}), true
	case key.NameTab:
		if shift {
			return []byte("\x1b[Z"), true
		}
		return withAlt([]byte{'\t'}), true
	case key.NameEscape:
		return []byte{0x1b}, true
	case key.NameSpace:
		if ctrl {
			return withAlt([]byte{0}), true
		}
		if alt {
			return []byte{0x1b, ' '}, true
		}
		return nil, false // plain space arrives as text
	case key.NameUpArrow:
		return csi("A"), true
	case key.NameDownArrow:
		return csi("B"), true
	case key.NameRightArrow:
		return csi("C"), true
	case key.NameLeftArrow:
		return csi("D"), true
	case key.NameHome:
		return csi("H"), true
	case key.NameEnd:
		return csi("F"), true
	case key.NamePageUp:
		return tilde(5), true
	case key.NamePageDown:
		return tilde(6), true
	case key.NameDeleteForward:
		return tilde(3), true
	}
	if n := strings.TrimPrefix(string(name), "F"); n != string(name) && n != "" {
		codes := map[string]string{"1": "P", "2": "Q", "3": "R", "4": "S"}
		if c, ok := codes[n]; ok {
			if mod == 1 {
				return []byte("\x1bO" + c), true
			}
			return []byte(fmt.Sprintf("\x1b[1;%d%s", mod, c)), true
		}
		tildes := map[string]int{"5": 15, "6": 17, "7": 18, "8": 19, "9": 20, "10": 21, "11": 23, "12": 24}
		if c, ok := tildes[n]; ok {
			return tilde(c), true
		}
	}
	// Single-character keys: only Ctrl and Alt combinations are ours; bare and
	// shifted characters come through as text.
	r := []rune(string(name))
	if len(r) != 1 || !(ctrl || alt) {
		return nil, false
	}
	c := r[0]
	if c >= 'A' && c <= 'Z' && !shift {
		c += 'a' - 'A'
	}
	if ctrl {
		switch {
		case c >= 'a' && c <= 'z':
			c = c - 'a' + 1
		case c >= 'A' && c <= 'Z':
			c = c - 'A' + 1
		case c == '[':
			c = 0x1b
		case c == '\\':
			c = 0x1c
		case c == ']':
			c = 0x1d
		case c == '/':
			c = 0x1f
		case c == '2' || c == '@':
			c = 0
		default:
			return nil, false
		}
	}
	if c < 0x80 {
		return withAlt([]byte{byte(c)}), true
	}
	return withAlt([]byte(string(c))), true
}

// ghosttyKeys maps Gio key names to libghostty keys and the character the key
// types without Shift, which the encoder needs for Ctrl and Alt combinations
// and the kitty keyboard protocol.
var ghosttyKeys = func() map[key.Name]struct {
	k  gh.Key
	cp rune
} {
	m := map[key.Name]struct {
		k  gh.Key
		cp rune
	}{}
	letters := []gh.Key{gh.KeyA, gh.KeyB, gh.KeyC, gh.KeyD, gh.KeyE, gh.KeyF, gh.KeyG, gh.KeyH, gh.KeyI,
		gh.KeyJ, gh.KeyK, gh.KeyL, gh.KeyM, gh.KeyN, gh.KeyO, gh.KeyP, gh.KeyQ, gh.KeyR, gh.KeyS, gh.KeyT,
		gh.KeyU, gh.KeyV, gh.KeyW, gh.KeyX, gh.KeyY, gh.KeyZ}
	for i, k := range letters {
		m[key.Name(rune('A'+i))] = struct {
			k  gh.Key
			cp rune
		}{k, rune('a' + i)}
	}
	digits := []gh.Key{gh.KeyDigit0, gh.KeyDigit1, gh.KeyDigit2, gh.KeyDigit3, gh.KeyDigit4,
		gh.KeyDigit5, gh.KeyDigit6, gh.KeyDigit7, gh.KeyDigit8, gh.KeyDigit9}
	for i, k := range digits {
		m[key.Name(rune('0'+i))] = struct {
			k  gh.Key
			cp rune
		}{k, rune('0' + i)}
	}
	add := func(n key.Name, k gh.Key, cp rune) {
		m[n] = struct {
			k  gh.Key
			cp rune
		}{k, cp}
	}
	add("-", gh.KeyMinus, '-')
	add("=", gh.KeyEqual, '=')
	add("[", gh.KeyBracketLeft, '[')
	add("]", gh.KeyBracketRight, ']')
	add("\\", gh.KeyBackslash, '\\')
	add(";", gh.KeySemicolon, ';')
	add("'", gh.KeyQuote, '\'')
	add(",", gh.KeyComma, ',')
	add(".", gh.KeyPeriod, '.')
	add("/", gh.KeySlash, '/')
	add("`", gh.KeyBackquote, '`')
	add(key.NameSpace, gh.KeySpace, ' ')
	add(key.NameReturn, gh.KeyEnter, 0)
	add(key.NameEnter, gh.KeyNumpadEnter, 0)
	add(key.NameTab, gh.KeyTab, 0)
	add(key.NameEscape, gh.KeyEscape, 0)
	add(key.NameDeleteBackward, gh.KeyBackspace, 0)
	add(key.NameDeleteForward, gh.KeyDelete, 0)
	add(key.NameUpArrow, gh.KeyArrowUp, 0)
	add(key.NameDownArrow, gh.KeyArrowDown, 0)
	add(key.NameLeftArrow, gh.KeyArrowLeft, 0)
	add(key.NameRightArrow, gh.KeyArrowRight, 0)
	add(key.NameHome, gh.KeyHome, 0)
	add(key.NameEnd, gh.KeyEnd, 0)
	add(key.NamePageUp, gh.KeyPageUp, 0)
	add(key.NamePageDown, gh.KeyPageDown, 0)
	fkeys := []gh.Key{gh.KeyF1, gh.KeyF2, gh.KeyF3, gh.KeyF4, gh.KeyF5, gh.KeyF6, gh.KeyF7, gh.KeyF8,
		gh.KeyF9, gh.KeyF10, gh.KeyF11, gh.KeyF12}
	for i, k := range fkeys {
		add(key.Name(fmt.Sprintf("F%d", i+1)), k, 0)
	}
	return m
}()

// encodeKey turns a key press into the bytes the program expects, using
// libghostty's encoder with the terminal's current modes (application cursor
// keys, the kitty keyboard protocol, modifyOtherKeys). It returns false for
// keys it does not know, which then fall back to keyBytes.
func encodeKey(enc *gh.KeyEncoder, ev *gh.KeyEvent, t *gh.Terminal, name key.Name, mods key.Modifiers) ([]byte, bool) {
	gk, ok := ghosttyKeys[name]
	if !ok || enc == nil || ev == nil {
		return nil, false
	}
	enc.SetOptFromTerminal(t)
	var m gh.Mods
	if mods.Contain(key.ModShift) {
		m |= gh.ModShift
	}
	if mods.Contain(key.ModCtrl) {
		m |= gh.ModCtrl
	}
	if mods.Contain(key.ModAlt) {
		m |= gh.ModAlt
	}
	if mods.Contain(key.ModSuper) {
		m |= gh.ModSuper
	}
	ev.SetAction(gh.KeyActionPress)
	ev.SetKey(gk.k)
	ev.SetMods(m)
	ev.SetConsumedMods(0)
	ev.SetUnshiftedCodepoint(gk.cp)
	text := ""
	if gk.cp != 0 {
		text = string(gk.cp)
		if m&gh.ModShift != 0 && gk.cp >= 'a' && gk.cp <= 'z' {
			text = strings.ToUpper(text)
			ev.SetConsumedMods(gh.ModShift)
		}
	}
	ev.SetUTF8(text)
	out, err := enc.Encode(ev)
	if err != nil || len(out) == 0 {
		return nil, false
	}
	// Without the kitty protocol, a Ctrl combination the encoder can only
	// express as CSI u (Ctrl+[ and friends) has a classic control character.
	if flags, _ := t.KittyKeyboardFlags(); flags == 0 && len(out) > 2 && out[0] == 0x1b && out[1] == '[' && out[len(out)-1] == 'u' {
		if legacy, ok := keyBytes(name, mods); ok {
			return legacy, true
		}
	}
	return out, true
}
