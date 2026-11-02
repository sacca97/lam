//go:build gui && linux && !android

package app

/*
#cgo pkg-config: freetype2
#include <stdlib.h>
#include <ft2build.h>
#include FT_FREETYPE_H
#include FT_MULTIPLE_MASTERS_H
#include FT_SYNTHESIS_H

// lam_ft_set_weight moves a variable font's wght axis to weight. It returns
// 0 when the font has no such axis.
static int lam_ft_set_weight(FT_Library lib, FT_Face face, FT_Fixed weight) {
	if (!(face->face_flags & FT_FACE_FLAG_MULTIPLE_MASTERS)) return 0;
	FT_MM_Var *mm;
	if (FT_Get_MM_Var(face, &mm)) return 0;
	int found = 0;
	FT_Fixed coords[16];
	FT_UInt n = mm->num_axis < 16 ? mm->num_axis : 16;
	for (FT_UInt i = 0; i < n; i++) {
		coords[i] = mm->axis[i].def;
		if (mm->axis[i].tag == FT_MAKE_TAG('w','g','h','t')) {
			FT_Fixed w = weight;
			if (w > mm->axis[i].maximum) w = mm->axis[i].maximum;
			if (w < mm->axis[i].minimum) w = mm->axis[i].minimum;
			coords[i] = w;
			found = 1;
		}
	}
	if (found && FT_Set_Var_Design_Coordinates(face, n, coords)) found = 0;
	FT_Done_MM_Var(lib, mm);
	return found;
}

static FT_GlyphSlot lam_ft_load(FT_Face face, FT_ULong cp, int bold, int oblique, FT_Pos *advance) {
	FT_UInt idx = FT_Get_Char_Index(face, cp);
	if (idx == 0) return NULL;
	// Light hinting is what the desktop uses (hintslight): vertical
	// positions snap to the pixel grid, glyph shapes stay as designed.
	if (FT_Load_Glyph(face, idx, FT_LOAD_DEFAULT | FT_LOAD_TARGET_LIGHT)) return NULL;
	FT_GlyphSlot slot = face->glyph;
	*advance = slot->advance.x;
	if (bold) FT_GlyphSlot_Embolden(slot);
	if (oblique) FT_GlyphSlot_Oblique(slot);
	if (FT_Render_Glyph(slot, FT_RENDER_MODE_NORMAL)) return NULL;
	return slot;
}
*/
import "C"

import (
	"image"
	"image/draw"
	"math"
	"unsafe"
)

// ftLib is the FreeType library of the UI goroutine.
var ftLib C.FT_Library

func ftInit() bool {
	if ftLib != nil {
		return true
	}
	return C.FT_Init_FreeType(&ftLib) == 0
}

// ftFace is one font file at one size, in one weight.
type ftFace struct {
	face           C.FT_Face
	bold, oblique  bool // synthesized
	ascent, height int
	descent        int
}

// openFTFace opens a font at px pixels. bold asks for a bold weight: a
// variable font moves its weight axis, any other is emboldened.
func openFTFace(f *fontFile, px float64, bold, oblique bool) *ftFace {
	if f == nil || f.path == "" || f.path == "builtin" || !ftInit() {
		return nil
	}
	cpath := C.CString(f.path)
	defer C.free(unsafe.Pointer(cpath))
	var face C.FT_Face
	if C.FT_New_Face(ftLib, cpath, C.FT_Long(f.index), &face) != 0 {
		return nil
	}
	ff := &ftFace{face: face, oblique: oblique}
	if bold && C.lam_ft_set_weight(ftLib, face, 700<<16) == 0 {
		ff.bold = true
	}
	if C.FT_Set_Char_Size(face, 0, C.FT_F26Dot6(math.Round(px*64)), 72, 72) != 0 {
		C.FT_Done_Face(face)
		return nil
	}
	m := face.size.metrics
	ff.ascent = int((m.ascender + 63) >> 6)
	ff.descent = int((-m.descender + 63) >> 6)
	ff.height = int((m.height + 63) >> 6)
	return ff
}

func (f *ftFace) close() {
	if f != nil && f.face != nil {
		C.FT_Done_Face(f.face)
		f.face = nil
	}
}

func (f *ftFace) has(r rune) bool { return C.FT_Get_Char_Index(f.face, C.FT_ULong(r)) != 0 }

// advance is a character's advance in pixels.
func (f *ftFace) advance(r rune) (float64, bool) {
	idx := C.FT_Get_Char_Index(f.face, C.FT_ULong(r))
	if idx == 0 || C.FT_Load_Glyph(f.face, idx, C.FT_LOAD_DEFAULT|C.FT_LOAD_TARGET_LIGHT) != 0 {
		return 0, false
	}
	return float64(f.face.glyph.advance.x) / 64, true
}

// draw renders r with its origin at (x, baseline) into dst, combining with
// what is there, and reports whether the font has it.
func (f *ftFace) draw(dst *image.Alpha, r rune, x, baseline int) bool {
	var adv C.FT_Pos
	slot := C.lam_ft_load(f.face, C.FT_ULong(r), boolC(f.bold), boolC(f.oblique), &adv)
	if slot == nil {
		return false
	}
	bm := slot.bitmap
	w, h := int(bm.width), int(bm.rows)
	if w == 0 || h == 0 || bm.buffer == nil {
		return true // a blank glyph
	}
	pitch := int(bm.pitch)
	if pitch < 0 {
		pitch = -pitch
	}
	src := unsafe.Slice((*byte)(unsafe.Pointer(bm.buffer)), pitch*h)
	mask := &image.Alpha{Pix: src, Stride: pitch, Rect: image.Rect(0, 0, w, h)}
	at := image.Pt(x+int(slot.bitmap_left), baseline-int(slot.bitmap_top))
	rect := mask.Rect.Add(at).Intersect(dst.Rect)
	if !rect.Empty() {
		draw.DrawMask(dst, rect, image.Opaque, image.Point{}, mask, rect.Min.Sub(at), draw.Over)
	}
	return true
}

func boolC(b bool) C.int {
	if b {
		return 1
	}
	return 0
}

const haveFreeType = true
