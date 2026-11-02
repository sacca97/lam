//go:build gui && (!linux || android)

package app

import "image"

// Without FreeType (Android, macOS for now) glyphs come from x/image, which
// draws them unhinted.

type ftFace struct{ ascent, height, descent int }

func openFTFace(*fontFile, float64, bool, bool) *ftFace { return nil }
func (*ftFace) close()                                  {}
func (*ftFace) has(rune) bool                           { return false }
func (*ftFace) advance(rune) (float64, bool)            { return 0, false }
func (*ftFace) draw(*image.Alpha, rune, int, int) bool  { return false }

const haveFreeType = false
