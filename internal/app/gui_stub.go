//go:build !gui

package app

import (
	"fmt"
	"os"
)

// guiBuilt says whether this binary can open the window.
const guiBuilt = false

func guiCmd(args []string) {
	fmt.Fprintln(os.Stderr, "lam gui: built without GUI support; build with -tags gui (make gui)")
	os.Exit(1)
}
