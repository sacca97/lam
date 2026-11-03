//go:build gui

package app

import (
	"context"
	"image"
	"image/draw"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomono"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/font/gofont/gomonobolditalic"
	"golang.org/x/image/font/gofont/gomonoitalic"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

// The terminal is drawn the way terminal emulators draw it: every glyph is
// rasterized once on the CPU, hinted and aligned to the pixel grid, cached as
// a coverage mask, and stamped into row images. Vector text on the GPU is both
// slower for a full screen of glyphs and blurry at terminal sizes.

const (
	styleRegular = iota
	styleBold
	styleItalic
	styleBoldItalic
)

// Terminal families tried when neither the desktop nor fontconfig names one
// that is installed, best first.
var monoFamilies = []string{
	"JetBrains Mono", "Adwaita Mono", "Cascadia Mono", "Cascadia Code", "Fira Code",
	"Source Code Pro", "Hack", "Iosevka", "DejaVu Sans Mono", "Noto Sans Mono", "Ubuntu Mono",
}

// desktopFont is the font the desktop is set to use for monospace text (mono)
// or for the interface: GNOME's and KDE's settings, as their own terminals
// and applications read them. size is in points; zero when unknown.
func desktopFont(mono bool) (family string, size float64) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		return "", 0
	}
	key := "font-name"
	if mono {
		key = "monospace-font-name"
	}
	if out, err := fontTool("gsettings", "get", "org.gnome.desktop.interface", key); err == nil {
		// 'Noto Sans Mono 10': the size is the last word.
		v := strings.Trim(strings.TrimSpace(string(out)), "'")
		if i := strings.LastIndexByte(v, ' '); i > 0 {
			if n, err := strconv.ParseFloat(v[i+1:], 64); err == nil {
				return strings.TrimSpace(v[:i]), n
			}
		}
		if v != "" {
			return v, 0
		}
	}
	// KDE: ~/.config/kdeglobals, [General] fixed=Hack,10,... and font=...
	home, _ := os.UserHomeDir()
	data, err := os.ReadFile(filepath.Join(home, ".config", "kdeglobals"))
	if err != nil {
		return "", 0
	}
	want := "font="
	if mono {
		want = "fixed="
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, want) {
			parts := strings.Split(strings.TrimPrefix(line, want), ",")
			if len(parts) >= 2 {
				n, _ := strconv.ParseFloat(parts[1], 64)
				return parts[0], n
			}
		}
	}
	return "", 0
}

// macMonoFiles finds the Mac's terminal fonts: SF Mono, which comes with
// Terminal.app (or installed from Apple), else Menlo, which every macOS has.
func macMonoFiles() ([4]*fontFile, bool) {
	var files [4]*fontFile
	for _, dir := range []string{
		"/System/Applications/Utilities/Terminal.app/Contents/Resources/Fonts",
		"/Applications/Utilities/Terminal.app/Contents/Resources/Fonts",
	} {
		names := [4]string{"SFMono-Regular.otf", "SFMono-Bold.otf", "SFMono-RegularItalic.otf", "SFMono-BoldItalic.otf"}
		for s, n := range names {
			files[s] = loadFontFile(filepath.Join(dir, n), 0)
		}
		if files[styleRegular] != nil {
			return files, true
		}
	}
	home, _ := os.UserHomeDir()
	for _, dir := range []string{"/Library/Fonts", filepath.Join(home, "Library", "Fonts")} {
		names := [4]string{"SF-Mono-Regular.otf", "SF-Mono-Bold.otf", "SF-Mono-RegularItalic.otf", "SF-Mono-BoldItalic.otf"}
		for s, n := range names {
			files[s] = loadFontFile(filepath.Join(dir, n), 0)
		}
		if files[styleRegular] != nil {
			return files, true
		}
	}
	// Menlo.ttc holds regular, bold, italic and bold italic, in that order.
	for s := range files {
		files[s] = loadFontFile("/System/Library/Fonts/Menlo.ttc", s)
	}
	return files, files[styleRegular] != nil
}

// macFallbackFonts cover what the terminal fonts lack: CJK and symbols.
var macFallbackFonts = []string{
	"/System/Library/Fonts/Hiragino Sans GB.ttc",
	"/System/Library/Fonts/ヒラギノ角ゴシック W3.ttc",
	"/System/Library/Fonts/AppleSDGothicNeo.ttc",
	"/System/Library/Fonts/Apple Symbols.ttf",
	"/System/Library/Fonts/Supplemental/Arial Unicode.ttf",
	"/System/Library/Fonts/STHeiti Light.ttc",
}

// fontFile is a parsed font file, shared by every size.
type fontFile struct {
	f     *sfnt.Font
	path  string
	index int // within a collection
}

var (
	fontFilesMu sync.Mutex
	fontFiles   = map[string]*fontFile{}
)

func loadFontFile(path string, index int) *fontFile {
	key := path + "#" + strconv.Itoa(index)
	fontFilesMu.Lock()
	defer fontFilesMu.Unlock()
	if f, ok := fontFiles[key]; ok {
		return f
	}
	var ff *fontFile
	if data, err := os.ReadFile(path); err == nil {
		if c, err := opentype.ParseCollection(data); err == nil && index < c.NumFonts() {
			if f, err := c.Font(index); err == nil {
				ff = &fontFile{f: f, path: path, index: index}
			}
		} else if f, err := opentype.Parse(data); err == nil {
			ff = &fontFile{f: f, path: path, index: index}
		}
	}
	fontFiles[key] = ff // nil remembers a file that does not parse
	return ff
}

func builtinFont(data []byte) *fontFile {
	f, err := opentype.Parse(data)
	if err != nil {
		return nil
	}
	return &fontFile{f: f, path: "builtin"}
}

// fontToolTimeout bounds the helpers the window asks for fonts and settings: a
// hung dconf or fontconfig must not stall the window or pile up processes.
const fontToolTimeout = 5 * time.Second

// fontLookups limits the fc-match processes running at once.
var fontLookups = make(chan struct{}, 3)

func fontTool(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), fontToolTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).Output()
}

// fcMatch asks fontconfig for the file of a pattern.
func fcMatch(pattern string) (path string, index int, family string) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		return "", 0, ""
	}
	out, err := fontTool("fc-match", "-f", "%{file}\n%{index}\n%{family}", pattern)
	if err != nil {
		return "", 0, ""
	}
	parts := strings.SplitN(string(out), "\n", 3)
	if len(parts) < 3 {
		return "", 0, ""
	}
	index, _ = strconv.Atoi(parts[1])
	return parts[0], index, parts[2]
}

// monoFiles finds the regular, bold, italic and bold italic files of the
// terminal font. A missing style is nil and is synthesized from regular.
func monoFiles() [4]*fontFile {
	var files [4]*fontFile
	// LAM_GUI_FONT may also be the path of a font file.
	if p := strings.TrimSpace(os.Getenv("LAM_GUI_FONT")); p != "" {
		if f := loadFontFile(p, 0); f != nil {
			files[styleRegular] = f
			return files
		}
	}
	if runtime.GOOS == "darwin" {
		if f, ok := macMonoFiles(); ok {
			return f
		}
	}
	// LAM_GUI_FONT first, then the desktop's monospace font, then
	// fontconfig's monospace, then a list of good terminal fonts.
	var families []string
	if f := strings.TrimSpace(os.Getenv("LAM_GUI_FONT")); f != "" {
		families = append(families, f)
	}
	if f, _ := desktopFont(true); f != "" {
		families = append(families, f)
	}
	families = append(append(families, "monospace"), monoFamilies...)
	styles := [4]string{"", ":weight=bold", ":slant=italic", ":weight=bold:slant=italic"}
	for _, family := range families {
		path, index, got := fcMatch(family)
		if path == "" || (family != "monospace" && !strings.HasPrefix(strings.ToLower(got), strings.ToLower(family))) {
			continue
		}
		regular := loadFontFile(path, index)
		if regular == nil {
			continue
		}
		files[styleRegular] = regular
		for s := styleBold; s <= styleBoldItalic; s++ {
			if p, i, _ := fcMatch(family + styles[s]); p != "" && p != path {
				files[s] = loadFontFile(p, i)
			}
		}
		return files
	}
	if runtime.GOOS == "android" {
		for _, p := range []string{"/system/fonts/RobotoMono-Regular.ttf", "/system/fonts/DroidSansMono.ttf", "/system/fonts/CutiveMono.ttf"} {
			if f := loadFontFile(p, 0); f != nil {
				files[styleRegular] = f
				return files
			}
		}
	}
	return [4]*fontFile{builtinFont(gomono.TTF), builtinFont(gomonobold.TTF), builtinFont(gomonoitalic.TTF), builtinFont(gomonobolditalic.TTF)}
}

// uiFontFiles are the interface font's weights: the desktop's sans serif.
func uiFontFiles() [][]byte {
	if runtime.GOOS == "darwin" {
		// San Francisco, the system font.
		for _, p := range []string{"/System/Library/Fonts/SFNS.ttf", "/System/Library/Fonts/SFNSText.ttf"} {
			if data, err := os.ReadFile(p); err == nil {
				return [][]byte{data}
			}
		}
	}
	families := []string{"sans-serif"}
	if f, _ := desktopFont(false); f != "" {
		families = append([]string{f}, families...)
	}
	for _, family := range families {
		path, _, got := fcMatch(family)
		if path == "" || (family != "sans-serif" && !strings.HasPrefix(strings.ToLower(got), strings.ToLower(family))) {
			continue
		}
		var files [][]byte
		seen := map[string]bool{}
		for _, w := range []string{"", ":weight=medium", ":weight=semibold", ":weight=bold"} {
			p, _, _ := fcMatch(family + w)
			if p == "" || seen[p] {
				continue
			}
			seen[p] = true
			if data, err := os.ReadFile(p); err == nil {
				files = append(files, data)
			}
		}
		if len(files) > 0 {
			return files
		}
	}
	if runtime.GOOS == "android" {
		if data, err := os.ReadFile("/system/fonts/Roboto-Regular.ttf"); err == nil {
			return [][]byte{data}
		}
	}
	return nil
}

// glyphKey identifies a cached glyph mask.
type glyphKey struct {
	text  string
	style uint8
	span  uint8 // cells the glyph covers
}

// glyphMask is a glyph's coverage, positioned relative to its cell's top left.
type glyphMask struct {
	mask *image.Alpha
	off  image.Point
}

// termFont is the terminal font at one pixel size, with its glyph cache.
type termFont struct {
	px                     float64
	faces                  [4]font.Face
	synthBold, synthItalic [4]bool
	cellW, cellH, baseline int
	thin                   int // line thickness for underlines and box drawing

	glyphs   map[glyphKey]*glyphMask
	gamma    [256]uint8
	fallback *fallbackFonts

	// FreeType faces, when FreeType is available: hinted the way the
	// desktop hints its own text. Nil entries use the x/image faces above.
	ft         [4]*ftFace
	ftFallback map[faceKey]*ftFace
}

var termFontFiles struct {
	once  sync.Once
	files [4]*fontFile
}

func newTermFont(px float64, fallback *fallbackFonts) *termFont {
	termFontFiles.once.Do(func() { termFontFiles.files = monoFiles() })
	files := termFontFiles.files
	t := &termFont{px: px, glyphs: map[glyphKey]*glyphMask{}, fallback: fallback}
	for s := range files {
		f := files[s]
		switch {
		case f != nil:
		case s == styleBoldItalic && files[styleBold] != nil:
			f, t.synthItalic[s] = files[styleBold], true
		case s == styleBoldItalic && files[styleItalic] != nil:
			f, t.synthBold[s] = files[styleItalic], true
		default:
			f = files[styleRegular]
			t.synthBold[s] = s == styleBold || s == styleBoldItalic
			t.synthItalic[s] = s == styleItalic || s == styleBoldItalic
		}
		face, err := opentype.NewFace(f.f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
		if err != nil {
			face, _ = opentype.NewFace(builtinFont(gomono.TTF).f, &opentype.FaceOptions{Size: px, DPI: 72, Hinting: font.HintingFull})
		}
		t.faces[s] = face
		if haveFreeType {
			t.ft[s] = openFTFace(f, px, t.synthBold[s], t.synthItalic[s])
		}
	}
	for i := range t.gamma {
		t.gamma[i] = uint8(i)
	}
	t.thin = max(1, int(math.Round(px/14)))
	if r := t.ft[styleRegular]; r != nil {
		t.ftFallback = map[faceKey]*ftFace{}
		adv, ok := r.advance('M')
		if !ok {
			adv = px * 0.6
		}
		t.cellW = max(1, int(math.Round(adv)))
		t.cellH = max(r.ascent+r.descent, int(math.Ceil(px*1.2)))
		t.baseline = r.ascent + (t.cellH-r.ascent-r.descent)/2
		return t
	}
	m := t.faces[styleRegular].Metrics()
	adv, ok := t.faces[styleRegular].GlyphAdvance('M')
	if !ok {
		adv = fixed.I(int(px * 0.6))
	}
	t.cellW = max(1, adv.Round())
	asc, desc := m.Ascent.Ceil(), m.Descent.Ceil()
	t.cellH = max(asc+desc, int(math.Ceil(px*1.2)))
	t.baseline = asc + (t.cellH-asc-desc)/2
	// Unhinted text on a dark background looks thinner than the same font in
	// other terminals: lift its coverage slightly.
	for i := range t.gamma {
		t.gamma[i] = uint8(math.Round(255 * math.Pow(float64(i)/255, 0.8)))
	}
	return t
}

func (t *termFont) close() {
	for _, f := range t.faces {
		if f != nil {
			f.Close()
		}
	}
	for _, f := range t.ft {
		f.close()
	}
	for _, f := range t.ftFallback {
		f.close()
	}
}

// rasterizeFT draws a grapheme with FreeType, from the terminal font or the
// fallback font that has it.
func (t *termFont) rasterizeFT(runes []rune, style uint8, w, h int) *glyphMask {
	face := t.ft[style]
	if !face.has(runes[0]) {
		f := t.fallback.file(runes[0])
		if f == nil {
			return nil // looked up in the background; drawn once found
		}
		face = t.ftFallbackFace(f, t.px)
		if face == nil {
			return nil
		}
		// A glyph wider than its cells is drawn from a smaller size.
		if adv, ok := face.advance(runes[0]); ok && adv > float64(w)+0.5 {
			if small := t.ftFallbackFace(f, t.px*float64(w)/adv); small != nil {
				face = small
			}
		}
	}
	pad := t.cellW / 2
	dst := image.NewAlpha(image.Rect(0, 0, w+2*pad, h))
	drawn := false
	for i, r := range runes {
		if i > 0 && (r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f)) {
			break // joiners and variation selectors: the base glyph stands in
		}
		if face.draw(dst, r, pad, t.baseline) {
			drawn = true
		}
	}
	if !drawn {
		return nil
	}
	return &glyphMask{mask: dst, off: image.Pt(-pad, 0)}
}

func (t *termFont) ftFallbackFace(f *fontFile, px float64) *ftFace {
	k := faceKey{f, math.Round(px*4) / 4}
	if face, ok := t.ftFallback[k]; ok {
		return face
	}
	face := openFTFace(f, k.px, false, false)
	t.ftFallback[k] = face
	return face
}

// glyph returns the mask for a grapheme in a style, rasterizing it on first
// use. It returns nil for blank text.
func (t *termFont) glyph(text string, style uint8, span int) *glyphMask {
	k := glyphKey{text, style, uint8(span)}
	if g, ok := t.glyphs[k]; ok {
		return g
	}
	g := t.rasterize(text, style, span)
	t.glyphs[k] = g
	return g
}

func (t *termFont) rasterize(text string, style uint8, span int) *glyphMask {
	runes := []rune(text)
	if len(runes) == 0 || (len(runes) == 1 && runes[0] == ' ') {
		return nil
	}
	w, h := t.cellW*span, t.cellH
	if m := boxGlyph(runes[0], w, h, t.thin); m != nil {
		return &glyphMask{mask: m}
	}
	if t.ft[style] != nil {
		return t.rasterizeFT(runes, style, w, h)
	}
	face := t.faces[style]
	if _, ok := face.GlyphAdvance(runes[0]); !ok {
		if alt := t.fallback.face(runes[0], t.px); alt != nil {
			face = alt
		}
	}
	// Room for glyphs that overhang their cell, such as italics.
	pad := t.cellW / 2
	dst := image.NewAlpha(image.Rect(0, 0, w+2*pad, h))
	dot := fixed.P(pad, t.baseline)
	if adv, ok := face.GlyphAdvance(runes[0]); ok && adv.Round() > w {
		// A glyph wider than its cells (a symbol from a fallback font) is
		// scaled down by drawing it from a smaller face.
		if small := t.fallback.scaled(face, runes[0], t.px*float64(w)/float64(adv.Round())); small != nil {
			face = small
		}
	}
	drawn := false
	for i, r := range runes {
		if i > 0 && (r == 0x200d || (r >= 0xfe00 && r <= 0xfe0f)) {
			break // joiners and variation selectors: the base glyph stands in
		}
		dr, mask, mp, _, ok := face.Glyph(dot, r)
		if !ok {
			continue
		}
		draw.DrawMask(dst, dr, image.Opaque, image.Point{}, mask, mp, draw.Over)
		drawn = true
	}
	if !drawn {
		return nil
	}
	if t.synthBold[style] {
		bold := image.NewAlpha(dst.Rect)
		copy(bold.Pix, dst.Pix)
		shift := max(1, t.thin)
		for y := 0; y < h; y++ {
			row := dst.Pix[y*dst.Stride : y*dst.Stride+dst.Rect.Dx()]
			out := bold.Pix[y*bold.Stride : y*bold.Stride+bold.Rect.Dx()]
			for x := shift; x < len(row); x++ {
				out[x] = max(out[x], row[x-shift])
			}
		}
		dst = bold
	}
	if t.synthItalic[style] {
		dst = slant(dst, t.baseline)
	}
	for i, a := range dst.Pix {
		dst.Pix[i] = t.gamma[a]
	}
	return &glyphMask{mask: dst, off: image.Pt(-pad, 0)}
}

// slant shears a mask to fake an italic, pivoting on the baseline.
func slant(src *image.Alpha, baseline int) *image.Alpha {
	dst := image.NewAlpha(src.Rect)
	w := src.Rect.Dx()
	for y := 0; y < src.Rect.Dy(); y++ {
		shift := int(math.Round(float64(baseline-y) * 0.2))
		for x := 0; x < w; x++ {
			if sx := x - shift; sx >= 0 && sx < w {
				dst.Pix[y*dst.Stride+x] = src.Pix[y*src.Stride+sx]
			}
		}
	}
	return dst
}

// fallbackFonts finds a font for characters the terminal font lacks (CJK,
// symbols). Looking one up runs fontconfig, so it happens in the background;
// gen counts finished lookups so the view knows to draw those cells again.
type fallbackFonts struct {
	mu      sync.Mutex
	byRune  map[rune]*fontFile // nil: none found
	pending map[rune]bool
	faces   map[faceKey]font.Face
	gen     atomic.Uint64
	notify  func()
}

type faceKey struct {
	f  *fontFile
	px float64
}

func newFallbackFonts(notify func()) *fallbackFonts {
	return &fallbackFonts{byRune: map[rune]*fontFile{}, pending: map[rune]bool{}, faces: map[faceKey]font.Face{}, notify: notify}
}

// face returns a face that has r at size px, or nil while it is being looked
// up or when no installed font has it.
func (fb *fallbackFonts) face(r rune, px float64) font.Face {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	f, known := fb.byRune[r]
	if !known {
		if !fb.pending[r] {
			fb.pending[r] = true
			go fb.lookup(r)
		}
		return nil
	}
	if f == nil {
		return nil
	}
	return fb.faceLocked(f, px)
}

// file is the font file that has r, or nil while it is looked up or when no
// installed font has it.
func (fb *fallbackFonts) file(r rune) *fontFile {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	f, known := fb.byRune[r]
	if !known && !fb.pending[r] {
		fb.pending[r] = true
		go fb.lookup(r)
	}
	return f
}

func (fb *fallbackFonts) faceLocked(f *fontFile, px float64) font.Face {
	k := faceKey{f, math.Round(px*4) / 4}
	if face, ok := fb.faces[k]; ok {
		return face
	}
	face, err := opentype.NewFace(f.f, &opentype.FaceOptions{Size: k.px, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil
	}
	fb.faces[k] = face
	return face
}

// scaled is face's font at another size, for a glyph too wide for its cells.
func (fb *fallbackFonts) scaled(face font.Face, r rune, px float64) font.Face {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	for k, f := range fb.faces {
		if f == face {
			return fb.faceLocked(k.f, px)
		}
	}
	return nil
}

func (fb *fallbackFonts) lookup(r rune) {
	// A screen of unfamiliar characters asks for hundreds of fonts at once.
	fontLookups <- struct{}{}
	defer func() { <-fontLookups }()
	var found *fontFile
	candidates := []string{}
	if path, index, _ := fcMatch("monospace:charset=" + strconv.FormatInt(int64(r), 16)); path != "" {
		candidates = append(candidates, path+"#"+strconv.Itoa(index))
	}
	if path, index, _ := fcMatch(":charset=" + strconv.FormatInt(int64(r), 16)); path != "" {
		candidates = append(candidates, path+"#"+strconv.Itoa(index))
	}
	if runtime.GOOS == "darwin" {
		for _, p := range macFallbackFonts {
			candidates = append(candidates, p+"#0")
		}
	}
	if runtime.GOOS == "android" {
		candidates = append(candidates, "/system/fonts/NotoSansCJK-Regular.ttc#0", "/system/fonts/NotoSansSymbols-Regular-Subsetted.ttf#0", "/system/fonts/DroidSansFallback.ttf#0")
	}
	var buf sfnt.Buffer
	for _, c := range candidates {
		i := strings.LastIndexByte(c, '#')
		index, _ := strconv.Atoi(c[i+1:])
		f := loadFontFile(c[:i], index)
		if f == nil {
			continue
		}
		if g, err := f.f.GlyphIndex(&buf, r); err == nil && g != 0 {
			found = f
			break
		}
	}
	fb.mu.Lock()
	fb.byRune[r] = found
	delete(fb.pending, r)
	fb.mu.Unlock()
	if found != nil {
		fb.gen.Add(1)
		if fb.notify != nil {
			fb.notify()
		}
	}
}

// boxGlyph draws box drawing, block, braille and powerline characters from
// geometry, so lines meet exactly across cells whatever the font. It returns
// nil for any other character.
func boxGlyph(r rune, w, h, thin int) *image.Alpha {
	switch {
	case r >= 0x2500 && r <= 0x257f:
		return boxLines(r, w, h, thin)
	case r >= 0x2580 && r <= 0x259f:
		return blockElement(r, w, h)
	case r >= 0x2800 && r <= 0x28ff:
		return braille(r, w, h)
	case r >= 0xe0b0 && r <= 0xe0b3:
		return powerline(r, w, h, thin)
	}
	return nil
}

func fillRect(m *image.Alpha, x0, y0, x1, y1 int, a uint8) {
	b := m.Rect
	x0, y0, x1, y1 = max(x0, b.Min.X), max(y0, b.Min.Y), min(x1, b.Max.X), min(y1, b.Max.Y)
	for y := y0; y < y1; y++ {
		row := m.Pix[y*m.Stride:]
		for x := x0; x < x1; x++ {
			row[x] = max(row[x], a)
		}
	}
}

// boxArms lists, for U+2500–U+257F, the weight of the arm towards the top,
// right, bottom and left: 0 none, 1 light, 2 heavy, 3 double. A negative
// first value marks dashed lines; arcs and diagonals are handled separately.
var boxArms = map[rune][4]int8{}

func init() {
	table := []string{
		"0101", "0202", "1010", "2020", "0101", "0202", "1010", "2020", "0101", "0202", "1010", "2020",
		"0110", "0210", "0120", "0220", "0011", "0012", "0021", "0022", "1100", "1200", "2100", "2200",
		"1001", "1002", "2001", "2002", "1110", "1210", "2110", "1120", "2120", "2210", "1220", "2220",
		"1011", "1012", "2011", "1021", "2021", "2012", "1022", "2022", "0111", "0112", "0211", "0212",
		"0121", "0122", "0221", "0222", "1101", "1102", "1201", "1202", "2101", "2102", "2201", "2202",
		"1111", "1112", "1211", "1212", "2111", "1121", "2121", "2112", "2211", "1122", "1221", "2212",
		"1222", "2122", "2221", "2222", "0101", "0202", "1010", "2020", "0303", "3030", "0310", "0130",
		"0330", "0013", "0031", "0033", "1300", "3100", "3300", "1003", "3001", "3003", "1310", "3130",
		"3330", "1013", "3031", "3033", "0313", "0131", "0333", "1303", "3101", "3303", "1313", "3131",
		"3333",
	}
	for i, s := range table {
		var arms [4]int8
		for j := range arms {
			arms[j] = int8(s[j] - '0')
		}
		boxArms[rune(0x2500+i)] = arms
	}
	tail := map[rune]string{
		0x2574: "0001", 0x2575: "1000", 0x2576: "0100", 0x2577: "0010", 0x2578: "0002", 0x2579: "2000",
		0x257a: "0200", 0x257b: "0020", 0x257c: "0201", 0x257d: "1020", 0x257e: "0102", 0x257f: "2010",
	}
	for r, s := range tail {
		var arms [4]int8
		for j := range arms {
			arms[j] = int8(s[j] - '0')
		}
		boxArms[r] = arms
	}
}

func isDashed(r rune) int {
	switch r {
	case 0x2504, 0x2505, 0x2506, 0x2507:
		return 3
	case 0x2508, 0x2509, 0x250a, 0x250b:
		return 4
	case 0x254c, 0x254d, 0x254e, 0x254f:
		return 2
	}
	return 0
}

func boxLines(r rune, w, h, thin int) *image.Alpha {
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	if r >= 0x256d && r <= 0x2570 {
		arc(m, r, w, h, thin)
		return m
	}
	if r >= 0x2571 && r <= 0x2573 {
		if r != 0x2572 {
			diagonal(m, w, h, thin, false)
		}
		if r != 0x2571 {
			diagonal(m, w, h, thin, true)
		}
		return m
	}
	arms, ok := boxArms[r]
	if !ok {
		return nil
	}
	heavy := max(thin*2, thin+1)
	// extent is how wide an arm is across its direction; a double line is
	// two thin strokes with a thin gap.
	extent := func(a int8) int {
		switch a {
		case 0:
			return 0
		case 2:
			return heavy
		case 3:
			return 3 * thin
		}
		return thin
	}
	mx, my := w/2, h/2
	vw := max(extent(arms[0]), extent(arms[2])) // widest vertical arm
	hw := max(extent(arms[1]), extent(arms[3])) // widest horizontal arm
	// strokes returns where an arm's strokes start across it, centred on c.
	strokes := func(a int8, c int) (starts []int, lw int) {
		switch a {
		case 3:
			return []int{c - 3*thin/2, c - 3*thin/2 + 2*thin}, thin
		case 2:
			return []int{c - heavy/2}, heavy
		}
		return []int{c - thin/2}, thin
	}
	for i, a := range arms {
		if a == 0 {
			continue
		}
		switch i {
		case 0, 2: // up, down
			starts, lw := strokes(a, mx)
			// Reach the far side of the horizontal arms (or the centre).
			reach := max(hw, extent(a))
			for _, x0 := range starts {
				if i == 0 {
					fillRect(m, x0, 0, x0+lw, my-reach/2+reach, 255)
				} else {
					fillRect(m, x0, my-reach/2, x0+lw, h, 255)
				}
			}
		case 1, 3: // right, left
			starts, lw := strokes(a, my)
			reach := max(vw, extent(a))
			for _, y0 := range starts {
				if i == 1 {
					fillRect(m, mx-reach/2, y0, w, y0+lw, 255)
				} else {
					fillRect(m, 0, y0, mx-reach/2+reach, y0+lw, 255)
				}
			}
		}
	}
	if n := isDashed(r); n > 0 {
		// Cut gaps into the line: n dashes per cell.
		vertical := arms[0] > 0
		size := w
		if vertical {
			size = h
		}
		for k := 0; k < n; k++ {
			g0 := (k*size)/n + (size/n)*2/3
			g1 := min(((k+1)*size)/n, size)
			if vertical {
				for y := g0; y < g1; y++ {
					clear(m.Pix[y*m.Stride : y*m.Stride+w])
				}
			} else {
				for y := 0; y < h; y++ {
					clear(m.Pix[y*m.Stride+g0 : y*m.Stride+g1])
				}
			}
		}
	}
	return m
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// arc draws the rounded corners ╭ ╮ ╯ ╰ with antialiasing.
func arc(m *image.Alpha, r rune, w, h, thin int) {
	cx := float64(w)/2 - 0.5
	cy := float64(h)/2 - 0.5
	rad := math.Min(float64(w), float64(h)) / 2
	// Direction of the arc's centre from the cell centre.
	dx, dy := 1.0, 1.0
	switch r {
	case 0x256e: // ╮ down and left
		dx = -1
	case 0x256f: // ╯ up and left
		dx, dy = -1, -1
	case 0x2570: // ╰ up and right
		dy = -1
	}
	ox, oy := cx+dx*rad, cy+dy*rad
	half := float64(thin) / 2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px, py := float64(x), float64(y)
			var d float64
			inX := (px-ox)*dx <= 0
			inY := (py-oy)*dy <= 0
			switch {
			case inX && inY:
				d = math.Abs(math.Hypot(px-ox, py-oy) - rad)
			case !inY && inX: // straight part towards the vertical edge
				d = math.Abs(px - cx)
				if (py-cy)*dy < 0 {
					continue
				}
			case !inX && inY:
				d = math.Abs(py - cy)
				if (px-cx)*dx < 0 {
					continue
				}
			default:
				continue
			}
			if a := half + 0.5 - d; a > 0 {
				m.Pix[y*m.Stride+x] = max(m.Pix[y*m.Stride+x], uint8(255*math.Min(1, a)))
			}
		}
	}
}

func diagonal(m *image.Alpha, w, h, thin int, down bool) {
	half := float64(thin) / 2
	length := math.Hypot(float64(w), float64(h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			var d float64
			if down { // ╲ from top left to bottom right
				d = math.Abs(float64(h)*px-float64(w)*py) / length
			} else { // ╱ from bottom left to top right
				d = math.Abs(float64(h)*px+float64(w)*py-float64(w*h)) / length
			}
			if a := half + 0.5 - d; a > 0 {
				m.Pix[y*m.Stride+x] = max(m.Pix[y*m.Stride+x], uint8(255*math.Min(1, a)))
			}
		}
	}
}

func blockElement(r rune, w, h int) *image.Alpha {
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	eighthH := func(n int) int { return int(math.Round(float64(h*n) / 8)) }
	eighthW := func(n int) int { return int(math.Round(float64(w*n) / 8)) }
	hw, hh := w/2, h/2
	switch {
	case r == 0x2580:
		fillRect(m, 0, 0, w, hh, 255)
	case r >= 0x2581 && r <= 0x2588:
		fillRect(m, 0, h-eighthH(int(r-0x2580)), w, h, 255)
	case r >= 0x2589 && r <= 0x258f:
		fillRect(m, 0, 0, eighthW(int(0x2590-r)), h, 255)
	case r == 0x2590:
		fillRect(m, hw, 0, w, h, 255)
	case r >= 0x2591 && r <= 0x2593:
		fillRect(m, 0, 0, w, h, uint8(64*(r-0x2590)))
	case r == 0x2594:
		fillRect(m, 0, 0, w, eighthH(1), 255)
	case r == 0x2595:
		fillRect(m, w-eighthW(1), 0, w, h, 255)
	default: // quadrants
		quads := map[rune]string{
			0x2596: "0010", 0x2597: "0001", 0x2598: "1000", 0x2599: "1011", 0x259a: "1001",
			0x259b: "1110", 0x259c: "1101", 0x259d: "0100", 0x259e: "0110", 0x259f: "0111",
		}
		q := quads[r]
		if q == "" {
			return nil
		}
		boxes := [4][4]int{{0, 0, hw, hh}, {hw, 0, w, hh}, {0, hh, hw, h}, {hw, hh, w, h}}
		for i, b := range boxes {
			if q[i] == '1' {
				fillRect(m, b[0], b[1], b[2], b[3], 255)
			}
		}
	}
	return m
}

func braille(r rune, w, h int) *image.Alpha {
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	bits := int(r - 0x2800)
	// Dot positions: bit → (column, row).
	pos := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
	dot := max(1, int(math.Round(float64(w)/5)))
	for i, p := range pos {
		if bits&(1<<i) == 0 {
			continue
		}
		x := int(math.Round(float64(w)*(0.3+0.4*float64(p[0])))) - dot/2
		y := int(math.Round(float64(h)*(0.14+0.24*float64(p[1])))) - dot/2
		fillRect(m, x, y, x+dot, y+dot, 255)
	}
	return m
}

// powerline draws the separators  and their thin variants.
func powerline(r rune, w, h, thin int) *image.Alpha {
	m := image.NewAlpha(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		// Distance from the vertical centre, 0 in the middle, 1 at an edge.
		t := math.Abs(float64(y)+0.5-float64(h)/2) / (float64(h) / 2)
		reach := (1 - t) * float64(w)
		for x := 0; x < w; x++ {
			px := float64(x) + 0.5
			if r == 0xe0b2 || r == 0xe0b3 {
				px = float64(w) - px
			}
			var a float64
			if r == 0xe0b0 || r == 0xe0b2 {
				a = reach - px + 0.5
			} else {
				a = float64(thin)/2 + 0.5 - math.Abs(px-reach)*float64(h)/math.Hypot(float64(h), float64(2*w))
			}
			if a > 0 {
				m.Pix[y*m.Stride+x] = uint8(255 * math.Min(1, a))
			}
		}
	}
	return m
}
