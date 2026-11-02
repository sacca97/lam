//go:build tools

package lammobile

// gomobile bind generates code that imports golang.org/x/mobile/bind; this
// import keeps it in go.mod, where go mod tidy would otherwise drop it.
import _ "golang.org/x/mobile/bind"
