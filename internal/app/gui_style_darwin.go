//go:build gui && darwin

package app

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>

// lamStyleWindow makes the native window, title bar included, light or dark
// with the app's theme.
static void lamStyleWindow(uintptr_t viewRef, int dark) {
	dispatch_async(dispatch_get_main_queue(), ^{
		NSView *view = (__bridge NSView *)(void *)viewRef;
		NSWindow *w = view.window;
		if (w == nil) return;
		w.appearance = [NSAppearance appearanceNamed:dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua];
	});
}
*/
import "C"

import (
	"gioui.org/app"
	"gioui.org/io/event"
)

func (g *guiApp) platformEvent(e event.Event) {
	if v, ok := e.(app.AppKitViewEvent); ok {
		g.nsView = v.View
		g.styleWindow()
		startNativeNotifications()
	}
}

// styleWindow makes the native title bar follow the theme.
func (g *guiApp) styleWindow() {
	if g.nsView == 0 {
		return
	}
	dark := C.int(1)
	if g.th.Name == "light" {
		dark = 0
	}
	C.lamStyleWindow(C.uintptr_t(g.nsView), dark)
}
