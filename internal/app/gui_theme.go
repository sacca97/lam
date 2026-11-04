//go:build gui

package app

import (
	"image/color"

	"gioui.org/unit"
)

// guiTheme holds every color and size of the GUI in one place.
//
// The look is a quiet graphite: surfaces step up in lightness instead of
// using shadows, text has three levels (primary, secondary, tertiary), and
// the accent is kept for what needs attention: an agent at work, the primary
// action and keyboard focus.
type guiTheme struct {
	Name                                                   string      // "dark" or "light"
	Scrim                                                  color.NRGBA // what dims the window behind a dialog
	Shadow                                                 float32     // strength of drop shadows, 1 on dark
	Bg, Sidebar, Surface, Selected, Hover, Divider, Border color.NRGBA
	Text, TextDim, TextFaint                               color.NRGBA
	Accent, AccentText, Idle, Unread, Danger               color.NRGBA
	TermFg, TermBg, Cursor, Selection                      color.NRGBA
	Palette16                                              [16]color.NRGBA

	SidebarW   unit.Dp
	ItemH      unit.Dp
	ItemRadius unit.Dp
	HeaderH    unit.Dp
	Pad        unit.Dp
	TermPad    unit.Dp
	TitleSize  unit.Sp
	UISize     unit.Sp
	SmallSize  unit.Sp
	TinySize   unit.Sp
}

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 255}
}

func defaultGUITheme() guiTheme { return themeNamed("dark") }

// themeNamed is the dark or the light theme; anything else is dark.
func themeNamed(name string) guiTheme {
	t := darkGUITheme()
	if name != "light" {
		name = "dark"
	} else {
		t.applyLight()
	}
	t.Name = name
	return t
}

// applyLight swaps the colors for the light palette; sizes stay as they are.
func (t *guiTheme) applyLight() {
	t.Scrim, t.Shadow = color.NRGBA{A: 90}, 0.3
	t.Bg, t.Sidebar, t.Surface = rgb(0xffffff), rgb(0xf3f4f6), rgb(0xffffff)
	t.Selected, t.Hover = rgb(0xe3e6ea), rgb(0xebedf0)
	t.Divider, t.Border = rgb(0xe1e3e7), rgb(0xcdd1d7)
	t.Text, t.TextDim, t.TextFaint = rgb(0x1c1e22), rgb(0x4d525a), rgb(0x80858e)
	t.Accent, t.AccentText = rgb(0x2f8a5b), rgb(0xffffff)
	t.Idle, t.Unread, t.Danger = rgb(0xaeb3bb), rgb(0xd0603f), rgb(0xd0372f)
	t.TermFg, t.TermBg, t.Cursor, t.Selection = rgb(0x24272d), rgb(0xffffff), rgb(0x2f8a5b), rgb(0xbad7f5)
	t.Palette16 = [16]color.NRGBA{
		rgb(0x24272d), rgb(0xc9302c), rgb(0x2e8b4a), rgb(0x9a6b00),
		rgb(0x2563c4), rgb(0x9a3fb5), rgb(0x1a8a96), rgb(0x6b717b),
		rgb(0x5c6370), rgb(0xe0453f), rgb(0x3aa35d), rgb(0xb8830a),
		rgb(0x3b7fe0), rgb(0xb257cf), rgb(0x2aa3b0), rgb(0x4a4f58),
	}
}

func darkGUITheme() guiTheme {
	return guiTheme{
		Shadow:     1,
		Scrim:      color.NRGBA{R: 13, G: 14, B: 16, A: 170},
		Bg:         rgb(0x0d0e10),
		Sidebar:    rgb(0x121316),
		Surface:    rgb(0x1a1c20),
		Selected:   rgb(0x23262b),
		Hover:      rgb(0x1b1d21),
		Divider:    rgb(0x1e2024),
		Border:     rgb(0x2b2e34),
		Text:       rgb(0xe7e8eb),
		TextDim:    rgb(0xa3a7ae),
		TextFaint:  rgb(0x6b7079),
		Accent:     rgb(0x7dc69a),
		AccentText: rgb(0x0b1610),
		Idle:       rgb(0x4a4f57),
		Unread:     rgb(0xe8866f),
		Danger:     rgb(0xe5534b),
		TermFg:     rgb(0xd8dbe1),
		TermBg:     rgb(0x0d0e10),
		Cursor:     rgb(0xcfe9d9),
		Selection:  rgb(0x264f78),
		Palette16: [16]color.NRGBA{
			rgb(0x22252b), rgb(0xe06c75), rgb(0x8cc88c), rgb(0xe5c07b),
			rgb(0x61afef), rgb(0xc678dd), rgb(0x56b6c2), rgb(0xc8ccd4),
			rgb(0x5c6370), rgb(0xff7b86), rgb(0xa9dfa9), rgb(0xf2d38a),
			rgb(0x82c4ff), rgb(0xd99cf0), rgb(0x7fd4de), rgb(0xf3f4f6),
		},
		SidebarW:   268,
		ItemH:      34,
		ItemRadius: 7,
		HeaderH:    46,
		Pad:        12,
		TermPad:    14,
		TitleSize:  16,
		UISize:     16,
		SmallSize:  14,
		TinySize:   13,
	}
}
