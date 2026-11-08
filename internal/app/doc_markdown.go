//go:build gui

package app

import (
	"fmt"
	"image/color"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Markdown is laid out into positioned items in pixels, independent of the
// window: the caller measures text, the layout wraps and stacks it, and the
// window paints the items. That keeps the layout testable with a fake ruler.

// Colours of the reader: a white page; the window paints them through
// docPalette when its theme is dark.
var (
	mdPage    = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	mdText    = color.NRGBA{R: 31, G: 35, B: 40, A: 255}
	mdDim     = color.NRGBA{R: 89, G: 99, B: 110, A: 255}
	mdLink    = color.NRGBA{R: 9, G: 105, B: 218, A: 255}
	mdCodeBg  = color.NRGBA{R: 246, G: 248, B: 250, A: 255}
	mdRule    = color.NRGBA{R: 209, G: 217, B: 224, A: 255}
	mdInlineB = color.NRGBA{R: 239, G: 241, B: 243, A: 255} // rgba(129,139,152,.12) on white
)

// mdRuler measures a piece of text drawn in a style, in pixels.
type mdRuler func(s string, size float32, bold, italic, mono bool) float32

type mdKind int

const (
	mdText_ mdKind = iota // text with its baseline at (X, Y)
	mdBox                 // a filled rectangle
)

// mdItem is one thing to paint.
type mdItem struct {
	Kind          mdKind
	X, Y, W, H    float32 // a box's rectangle; a text's origin on its baseline
	Text          string
	Size          float32
	Bold, Italic  bool
	Mono          bool
	Strike, Under bool
	Color         color.NRGBA
	Link          string  // the destination, for text that is a link
	Radius        float32 // a box's corner radius
}

// mdLayout is a laid-out document.
type mdLayout struct {
	Items   []mdItem
	Height  float32
	Anchors map[string]float32 // heading slugs (#like-this) and where they start
}

// headingSlug is the id GitHub gives a heading: lower case, spaces as
// hyphens, punctuation dropped.
func headingSlug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return b.String()
}

// anchor records a heading's place; a second heading of the same name is
// name-1, as on GitHub.
func (b *mdBuilder) anchor(title string) {
	slug := headingSlug(title)
	if slug == "" {
		return
	}
	for n := 1; ; n++ {
		if _, taken := b.out.Anchors[slug]; !taken {
			b.out.Anchors[slug] = b.y
			return
		}
		slug = fmt.Sprintf("%s-%d", headingSlug(title), n)
	}
}

// layoutMarkdown lays source out at the given content width and body size.
func layoutMarkdown(source []byte, width, size float32, ruler mdRuler) *mdLayout {
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	root := md.Parser().Parse(text.NewReader(source))
	b := &mdBuilder{src: source, ruler: ruler, size: size, out: &mdLayout{Anchors: map[string]float32{}}}
	b.blocks(root, 0, width)
	b.out.Height = b.y + size
	return b.out
}

type mdBuilder struct {
	depth   int     // how many lists deep
	leading float32 // line height as a multiple of the size; 0 means the body's
	src     []byte
	ruler   mdRuler
	size    float32
	y       float32
	out     *mdLayout
}

func (b *mdBuilder) add(it mdItem) { b.out.Items = append(b.out.Items, it) }

func (b *mdBuilder) gap(h float32) { b.y += h }

// blocks lays out the children of a container between x and x+width.
func (b *mdBuilder) blocks(parent ast.Node, x, width float32) {
	first := true
	for n := parent.FirstChild(); n != nil; n = n.NextSibling() {
		if !first {
			b.gap(b.size)
			switch n.(type) {
			case *ast.Heading, *ast.ThematicBreak:
				b.gap(b.size * 0.5) // 24px above, as on GitHub
			}
		}
		first = false
		b.block(n, x, width)
	}
}

func (b *mdBuilder) block(n ast.Node, x, width float32) {
	switch n := n.(type) {
	case *ast.Heading:
		scale := [...]float32{2, 1.5, 1.25, 1.0, 0.875, 0.85}[min(max(n.Level, 1), 6)-1]
		sz := b.size * scale
		c := mdText
		if n.Level == 6 {
			c = mdDim
		}
		b.anchor(string(n.Text(b.src)))
		b.leading = 1.25
		b.paragraph(n, x, width, sz, c, true)
		b.leading = 0
		if n.Level <= 2 {
			b.gap(sz * 0.3)
			b.add(mdItem{Kind: mdBox, X: x, Y: b.y, W: width, H: 1, Color: mdRule})
			b.gap(1)
		}
	case *ast.Paragraph, *ast.TextBlock:
		b.paragraph(n, x, width, b.size, mdText, false)
	case *ast.List:
		b.list(n, x, width)
	case *ast.Blockquote:
		top := b.y
		b.quote(n, x+b.size*1.25, width-b.size*1.25)
		b.add(mdItem{Kind: mdBox, X: x, Y: top, W: b.size * 0.25, H: b.y - top, Color: mdRule})
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		b.code(n, x, width)
	case *ast.ThematicBreak:
		h := b.size * 0.25
		b.add(mdItem{Kind: mdBox, X: x, Y: b.y, W: width, H: h, Color: mdRule})
		b.gap(h)
	case *east.Table:
		b.table(n, x, width)
	case *ast.HTMLBlock:
		var lines []string
		for i := 0; i < n.Lines().Len(); i++ {
			seg := n.Lines().At(i)
			lines = append(lines, strings.TrimRight(string(seg.Value(b.src)), "\r\n"))
		}
		b.plain(strings.Join(lines, "\n"), x, width, mdDim, true)
	default:
		b.blocks(n, x, width)
	}
}

// quote lays out a block quote's content in the dim colour.
func (b *mdBuilder) quote(n ast.Node, x, width float32) {
	first := true
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if !first {
			b.gap(b.size)
		}
		first = false
		if p, ok := c.(*ast.Paragraph); ok {
			b.paragraph(p, x, width, b.size, mdDim, false)
			continue
		}
		b.block(c, x, width)
	}
}

func (b *mdBuilder) list(n *ast.List, x, width float32) {
	indent := b.size * 2
	num := n.Start
	if num == 0 {
		num = 1
	}
	first := true
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		if !first {
			b.gap(b.size * 0.25)
		}
		first = false
		marker := "•"
		if n.IsOrdered() {
			marker = strconv.Itoa(num) + "."
			num++
		}
		top := b.y
		if tb, ok := firstTaskBox(item); ok {
			b.checkbox(x+indent-b.size*1.4, top+b.size*0.3, tb.IsChecked)
		} else if n.IsOrdered() {
			b.add(mdItem{Kind: mdText_, X: x + indent - b.ruler(marker, b.size, false, false, false) - b.size*0.4, Y: top + (b.size*1.5-b.size*1.2)/2 + b.size*0.95, Text: marker, Size: b.size, Color: mdText})
		} else {
			b.bullet(x+indent-b.size*0.95, top+b.size*0.75)
		}
		b.depth++
		b.listItem(item, x+indent, width-indent)
		b.depth--
	}
}

// bullet draws a list marker: a disc, a ring one level down, then a square.
func (b *mdBuilder) bullet(x, cy float32) {
	d := b.size * 0.36
	switch b.depth {
	case 0:
		b.add(mdItem{Kind: mdBox, X: x, Y: cy - d/2, W: d, H: d, Color: mdText, Radius: d / 2})
	case 1:
		b.add(mdItem{Kind: mdBox, X: x, Y: cy - d/2, W: d, H: d, Color: mdText, Radius: d / 2})
		b.add(mdItem{Kind: mdBox, X: x + 1.3, Y: cy - d/2 + 1.3, W: d - 2.6, H: d - 2.6, Color: mdPage, Radius: d / 2})
	default:
		b.add(mdItem{Kind: mdBox, X: x, Y: cy - d/2, W: d, H: d, Color: mdText})
	}
}

// checkbox draws a task's box, filled when done, with boxes, since the fonts
// have no ballot-box glyphs.
func (b *mdBuilder) checkbox(x, y float32, checked bool) {
	side := b.size * 0.85
	line := float32(1.5)
	edge := mdDim
	if checked {
		edge = mdLink
		b.add(mdItem{Kind: mdBox, X: x, Y: y, W: side, H: side, Color: mdLink})
		// The tick: two strokes as small squares along the path.
		for i := float32(0); i < 4; i++ {
			b.add(mdItem{Kind: mdBox, X: x + side*0.2 + i*side*0.08, Y: y + side*0.5 + i*side*0.08, W: line * 1.4, H: line * 1.4, Color: mdPage})
		}
		for i := float32(0); i < 7; i++ {
			b.add(mdItem{Kind: mdBox, X: x + side*0.45 + i*side*0.07, Y: y + side*0.78 - i*side*0.09, W: line * 1.4, H: line * 1.4, Color: mdPage})
		}
		return
	}
	b.add(mdItem{Kind: mdBox, X: x, Y: y, W: side, H: line, Color: edge})
	b.add(mdItem{Kind: mdBox, X: x, Y: y + side - line, W: side, H: line, Color: edge})
	b.add(mdItem{Kind: mdBox, X: x, Y: y, W: line, H: side, Color: edge})
	b.add(mdItem{Kind: mdBox, X: x + side - line, Y: y, W: line, H: side, Color: edge})
}

func (b *mdBuilder) listItem(item ast.Node, x, width float32) {
	first := true
	for c := item.FirstChild(); c != nil; c = c.NextSibling() {
		if !first {
			b.gap(b.size * 0.25)
		}
		first = false
		b.block(c, x, width)
	}
}

func firstTaskBox(item ast.Node) (*east.TaskCheckBox, bool) {
	if c := item.FirstChild(); c != nil {
		if tb, ok := c.FirstChild().(*east.TaskCheckBox); ok {
			return tb, true
		}
	}
	return nil, false
}

func (b *mdBuilder) code(n ast.Node, x, width float32) {
	var sb strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		sb.Write(seg.Value(b.src))
	}
	pad := b.size // 16px
	sz := b.size * 0.85
	lines := wrapMono(strings.TrimRight(sb.String(), "\n"), width-2*pad, sz, b.ruler)
	lh := sz * 1.45
	h := float32(len(lines))*lh + 2*pad
	b.add(mdItem{Kind: mdBox, X: x, Y: b.y, W: width, H: h, Color: mdCodeBg, Radius: b.size * 0.375})
	for i, l := range lines {
		b.add(mdItem{Kind: mdText_, X: x + pad, Y: b.y + pad + lh*float32(i) + (lh-sz*1.2)/2 + sz*0.95, Text: l, Size: sz, Mono: true, Color: mdText})
	}
	b.y += h
}

// wrapMono breaks code at the width, cutting long lines between characters.
func wrapMono(s string, width, size float32, ruler mdRuler) []string {
	var out []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\t", "    "), "\n") {
		cur := ""
		for _, r := range line {
			if cur != "" && ruler(cur+string(r), size, false, false, true) > width {
				out = append(out, cur)
				cur = ""
			}
			cur += string(r)
		}
		out = append(out, cur)
	}
	return out
}

// plain lays out literal text, one source line per line, wrapped.
func (b *mdBuilder) plain(s string, x, width float32, c color.NRGBA, mono bool) {
	sz := b.size * 0.85
	lh := sz * 1.45
	for _, l := range wrapMono(s, width, sz, b.ruler) {
		b.add(mdItem{Kind: mdText_, X: x, Y: b.y + sz*1.05, Text: l, Size: sz, Mono: mono, Color: c})
		b.y += lh
	}
}

// mdWord is a piece of a paragraph that cannot be broken, in one style.
type mdWord struct {
	text         string
	bold, italic bool
	mono         bool
	strike       bool
	link         string
	space        bool // whitespace between words
	newline      bool // a hard break
	width        float32
	color        color.NRGBA
	background   bool
	size         float32
}

// paragraph flows the inline content of n into lines.
func (b *mdBuilder) paragraph(n ast.Node, x, width, size float32, c color.NRGBA, bold bool) {
	var words []mdWord
	b.inline(n, mdWord{bold: bold, color: c, size: size}, &words)
	b.flow(words, x, width, size)
}

func (b *mdBuilder) inline(n ast.Node, st mdWord, out *[]mdWord) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			b.splitWords(string(c.Segment.Value(b.src)), st, out)
			if c.HardLineBreak() {
				*out = append(*out, mdWord{newline: true})
			} else if c.SoftLineBreak() {
				*out = append(*out, mdWord{space: true, size: st.size, bold: st.bold, mono: st.mono})
			}
		case *ast.String:
			b.splitWords(string(c.Value), st, out)
		case *ast.CodeSpan:
			s := st
			s.mono, s.background = true, true
			var sb strings.Builder
			for t := c.FirstChild(); t != nil; t = t.NextSibling() {
				if tt, ok := t.(*ast.Text); ok {
					sb.Write(tt.Segment.Value(b.src))
				}
			}
			b.splitWords(strings.ReplaceAll(sb.String(), "\n", " "), s, out)
		case *ast.Emphasis:
			s := st
			if c.Level >= 2 {
				s.bold = true
			} else {
				s.italic = true
			}
			b.inline(c, s, out)
		case *east.Strikethrough:
			s := st
			s.strike = true
			b.inline(c, s, out)
		case *ast.Link:
			s := st
			s.link, s.color = string(c.Destination), mdLink
			b.inline(c, s, out)
		case *ast.AutoLink:
			s := st
			s.link, s.color = string(c.URL(b.src)), mdLink
			b.splitWords(string(c.Label(b.src)), s, out)
		case *ast.Image:
			s := st
			s.link, s.color, s.italic = string(c.Destination), mdLink, true
			*out = append(*out, mdWord{text: "[image:", italic: true, color: mdDim, size: st.size})
			b.inline(c, s, out)
			*out = append(*out, mdWord{text: "]", italic: true, color: mdDim, size: st.size})
		case *east.TaskCheckBox:
			// Shown as the list marker.
		case *ast.RawHTML:
			var sb strings.Builder
			for i := 0; i < c.Segments.Len(); i++ {
				seg := c.Segments.At(i)
				sb.Write(seg.Value(b.src))
			}
			s := st
			s.color = mdDim
			b.splitWords(sb.String(), s, out)
		default:
			b.inline(c, st, out)
		}
	}
}

func (b *mdBuilder) splitWords(s string, st mdWord, out *[]mdWord) {
	start := -1 // where the word being read began
	flush := func(end int) {
		if start >= 0 {
			w := st
			w.text = s[start:end]
			*out = append(*out, w)
			start = -1
		}
	}
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			flush(i)
			w := st
			w.space, w.text = true, ""
			*out = append(*out, w)
			continue
		}
		if start < 0 {
			start = i
		}
	}
	flush(len(s))
}

// flow wraps words into lines and emits them.
func (b *mdBuilder) flow(words []mdWord, x, width, size float32) {
	leading := b.leading
	if leading == 0 {
		leading = 1.5
	}
	lh := size * leading
	type placed struct {
		w mdWord
		x float32
	}
	var line []placed
	cx := float32(0)
	lineTop := b.y
	flush := func() {
		// Trailing spaces do not count.
		for len(line) > 0 && line[len(line)-1].w.space {
			line = line[:len(line)-1]
		}
		// Neighbours in one style are drawn as one run, so the font's own
		// spacing and kerning apply across the words.
		baseline := lineTop + (lh-size*1.2)/2 + size*0.95
		// Each run starts where the one before it measured out, not at the sum
		// of its words' rounded widths, which drifts by a pixel or so a word.
		cur := float32(0)
		if len(line) > 0 {
			cur = x + line[0].x
		}
		for i := 0; i < len(line); {
			j := i
			text := ""
			for j < len(line) && sameRun(line[i].w, line[j].w) {
				if line[j].w.space {
					text += " "
				} else {
					text += line[j].w.text
				}
				j++
			}
			first := line[i].w
			ws := first.size
			if ws == 0 {
				ws = size
			}
			if first.mono {
				ws *= 0.85
			}
			rx := cur
			rw := b.ruler(text, ws, first.bold, first.italic, first.mono)
			cur += rw
			if first.background && !first.space {
				rx += size * 0.34 // inside the box's padding
				cur += size * 0.68
				b.add(mdItem{Kind: mdBox, X: rx - size*0.34, Y: baseline - size*0.92, W: b.ruler(strings.TrimRight(text, " "), ws, first.bold, first.italic, first.mono) + size*0.68, H: size * 1.25, Color: mdInlineB, Radius: size * 0.375})
			}
			if strings.TrimSpace(text) != "" {
				b.add(mdItem{Kind: mdText_, X: rx, Y: baseline, Text: text, Size: ws,
					Bold: first.bold, Italic: first.italic, Mono: first.mono, Strike: first.strike,
					Color: first.color, Link: first.link})
			}
			i = j
		}
		line = line[:0]
		cx = 0
		lineTop += lh
	}
	for _, w := range words {
		ws := w.size
		if ws == 0 {
			ws = size
		}
		if w.mono {
			ws *= 0.85
		}
		switch {
		case w.newline:
			flush()
			continue
		case w.space:
			if cx == 0 {
				continue
			}
			w.width = b.ruler(" ", ws, w.bold, w.italic, w.mono)
		default:
			w.width = b.ruler(w.text, ws, w.bold, w.italic, w.mono)
			if cx > 0 && cx+w.width > width {
				flush()
			}
			// A word wider than the line is cut between characters.
			rest := []rune(w.text)
			for w.width > width && len(rest) > 1 {
				// Bisect for the longest head that fits. Only a window of the word
				// is measured, so a line of megabytes without a space is cut in a
				// moment, not in time that grows with its square.
				lo, hi := 1, min(len(rest)-1, 2048)
				for lo < hi {
					mid := (lo + hi + 1) / 2
					if b.ruler(string(rest[:mid]), ws, w.bold, w.italic, w.mono) <= width {
						lo = mid
					} else {
						hi = mid - 1
					}
				}
				head := w
				head.text, head.width = string(rest[:lo]), b.ruler(string(rest[:lo]), ws, w.bold, w.italic, w.mono)
				line = append(line, placed{head, cx})
				flush()
				rest = rest[lo:]
				if len(rest) > 2048 {
					w.width = width + 1 // still longer than a line; measured when it is short
				} else {
					w.width = b.ruler(string(rest), ws, w.bold, w.italic, w.mono)
				}
			}
			w.text = string(rest)
			w.width = b.ruler(w.text, ws, w.bold, w.italic, w.mono)
		}
		line = append(line, placed{w, cx})
		cx += w.width
	}
	if len(line) > 0 {
		flush()
	}
	b.y = lineTop
}

// table lays out a GFM table as a grid, columns sharing the width by content.
func (b *mdBuilder) table(t *east.Table, x, width float32) {
	var rows [][]ast.Node
	var heads []bool
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		var cells []ast.Node
		for c := r.FirstChild(); c != nil; c = c.NextSibling() {
			cells = append(cells, c)
		}
		rows = append(rows, cells)
		_, isHead := r.(*east.TableHeader)
		heads = append(heads, isHead)
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return
	}
	pad := b.size * 0.8    // 13px on each side of a cell
	vpad := b.size * 0.375 // 6px above and below
	// Natural width of each column: its widest cell on one line.
	natural := make([]float32, cols)
	for _, r := range rows {
		for i, c := range r {
			var words []mdWord
			b.inline(c, mdWord{color: mdText, size: b.size}, &words)
			var w float32
			for _, wd := range words {
				if wd.space {
					w += b.ruler(" ", b.size, false, false, false)
				} else {
					ws := b.size
					if wd.mono {
						ws *= 0.85
					}
					w += b.ruler(wd.text, ws, wd.bold, wd.italic, wd.mono)
				}
			}
			natural[i] = max(natural[i], w+2*pad)
		}
	}
	var total float32
	for _, w := range natural {
		total += w
	}
	colW := make([]float32, cols)
	for i := range colW {
		if total <= width {
			colW[i] = natural[i]
		} else {
			colW[i] = max(b.size*4, natural[i]*width/total)
		}
	}
	// Squeezed columns can still add up to more than the page: shrink them all.
	if sum := sumF(colW); sum > width {
		for i := range colW {
			colW[i] *= width / sum
		}
	}
	top := b.y
	for ri, r := range rows {
		rowTop := b.y
		rowBg := len(b.out.Items)
		var h float32
		cx := x
		for i, c := range r {
			sub := &mdBuilder{src: b.src, ruler: b.ruler, size: b.size, y: rowTop + vpad, out: &mdLayout{}}
			bold := heads[ri]
			var words []mdWord
			sub.inline(c, mdWord{color: mdText, size: b.size, bold: bold}, &words)
			sub.flow(words, cx+pad, colW[i]-2*pad+1, b.size) // a pixel of slack: widths are sums of rounded parts
			for _, it := range sub.out.Items {
				b.add(it)
			}
			h = max(h, sub.y-rowTop+vpad)
			cx += colW[i]
		}
		// Every second body row is grey, as on GitHub; the header stays white.
		if !heads[ri] && ri%2 == 0 {
			// The background goes under what was just added.
			bg := mdItem{Kind: mdBox, X: x, Y: rowTop, W: sumF(colW), H: h, Color: mdCodeBg}
			b.out.Items = slices.Insert(b.out.Items, rowBg, bg)
		}
		b.y = rowTop + h
		b.add(mdItem{Kind: mdBox, X: x, Y: b.y, W: sumF(colW), H: 1, Color: mdRule})
		b.y++
	}
	// Vertical lines over the whole table.
	cx := x
	for i := 0; i <= cols; i++ {
		b.add(mdItem{Kind: mdBox, X: cx, Y: top, W: 1, H: b.y - top, Color: mdRule})
		if i < cols {
			cx += colW[i]
		}
	}
	b.add(mdItem{Kind: mdBox, X: x, Y: top, W: sumF(colW), H: 1, Color: mdRule})
}

func sumF(v []float32) (s float32) {
	for _, f := range v {
		s += f
	}
	return
}

// sameRun says whether two words are drawn in one style, so a run can carry both.
func sameRun(a, b mdWord) bool {
	if a.space != b.space && (a.link != "" || a.background || b.link != "" || b.background) {
		// A space between two linked or coded words belongs to the run; one
		// beside a plain word does not.
		return a.link == b.link && a.background == b.background && a.mono == b.mono
	}
	return a.bold == b.bold && a.italic == b.italic && a.mono == b.mono && a.strike == b.strike &&
		a.link == b.link && a.color == b.color && a.background == b.background && a.size == b.size
}

// docPalette is how a colour of the light page looks in the dark theme: the
// same roles on a dark page. Colours it does not know pass through.
func docPalette(c color.NRGBA, dark bool) color.NRGBA {
	if !dark {
		return c
	}
	switch c {
	case mdPage:
		return color.NRGBA{R: 21, G: 24, B: 29, A: 255}
	case mdText:
		return color.NRGBA{R: 230, G: 237, B: 243, A: 255}
	case mdDim:
		return color.NRGBA{R: 145, G: 152, B: 161, A: 255}
	case mdLink:
		return color.NRGBA{R: 88, G: 166, B: 255, A: 255}
	case mdCodeBg:
		return color.NRGBA{R: 30, G: 35, B: 42, A: 255}
	case mdRule:
		return color.NRGBA{R: 48, G: 54, B: 61, A: 255}
	case mdInlineB:
		return color.NRGBA{R: 43, G: 49, B: 57, A: 255}
	}
	return c
}
