//go:build gui && !darwin

package app

import "gioui.org/io/event"

// Elsewhere lam draws its own title bar, which already follows the theme.

func (g *guiApp) platformEvent(event.Event) {}
func (g *guiApp) styleWindow()              {}
