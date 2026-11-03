//go:build gui

package app

import (
	"strings"
	"testing"
	"time"
)

func fakeRuler(s string, size float32, bold, italic, mono bool) float32 {
	return float32(len([]rune(s))) * size * 0.5
}

func textsOf(l *mdLayout) []string {
	var out []string
	for _, it := range l.Items {
		if it.Kind == mdText_ {
			out = append(out, it.Text)
		}
	}
	return out
}

func TestMarkdownLayoutBlocks(t *testing.T) {
	src := "# Title\n\nSome *emphasis*, **strong** and `code` with a [link](https://example.com).\n\n- one\n- [x] done\n\n1. first\n\n> quoted\n\n```\nfn main() {}\n```\n\n| a | b |\n|---|---|\n| 1 | 2 |\n"
	l := layoutMarkdown([]byte(src), 400, 16, fakeRuler)
	all := strings.Join(textsOf(l), " ")
	for _, want := range []string{"Title", "emphasis", "strong", "code", "link", "1.", "quoted", "fn main() {}", "a", "2"} {
		if !strings.Contains(all, want) {
			t.Errorf("%q missing from %q", want, all)
		}
	}
	var link, heading bool
	for _, it := range l.Items {
		if it.Link == "https://example.com" {
			link = true
		}
		if it.Text == "Title" && it.Size > 16*1.9 && it.Bold {
			heading = true
		}
		if it.Kind == mdText_ && it.X < 0 {
			t.Errorf("%q starts left of the page", it.Text)
		}
	}
	if !link || !heading {
		t.Fatalf("link=%v heading=%v", link, heading)
	}
	if l.Height < 100 {
		t.Fatalf("height = %v", l.Height)
	}
}

func TestMarkdownWrapsToWidth(t *testing.T) {
	src := strings.Repeat("word ", 60)
	l := layoutMarkdown([]byte(src), 200, 16, fakeRuler)
	ys := map[float32]bool{}
	for _, it := range l.Items {
		if it.Kind == mdText_ {
			ys[it.Y] = true
			if it.X+fakeRuler(it.Text, it.Size, false, false, false) > 200.5 {
				t.Fatalf("%q runs past the width", it.Text)
			}
		}
	}
	if len(ys) < 5 {
		t.Fatalf("only %d lines", len(ys))
	}
	// An unbreakable word wider than the page is cut instead of overflowing.
	l = layoutMarkdown([]byte(strings.Repeat("x", 200)), 100, 16, fakeRuler)
	for _, it := range l.Items {
		if it.Kind == mdText_ && it.X+fakeRuler(it.Text, it.Size, false, false, false) > 100.5 {
			t.Fatalf("%q overflows", it.Text)
		}
	}
}

func TestMarkdownTolerantOfOddInput(t *testing.T) {
	for _, src := range []string{"", "\n\n", "|a|\n|-|\n", "<div>raw</div>\n", "- \n- \n", "![alt](x.png)", "```", "# \n"} {
		layoutMarkdown([]byte(src), 300, 16, fakeRuler)
	}
}

func TestMarkdownLongUnbrokenLineIsFast(t *testing.T) {
	start := time.Now()
	l := layoutMarkdown([]byte(strings.Repeat("x", 300000)), 400, 16, fakeRuler)
	if time.Since(start) > 3*time.Second {
		t.Fatalf("took %v", time.Since(start))
	}
	for _, it := range l.Items {
		if it.Kind == mdText_ && it.X+fakeRuler(it.Text, it.Size, false, false, false) > 400.5 {
			t.Fatalf("%q overflows", it.Text[:10])
		}
	}
}

func TestMarkdownTableFitsThePage(t *testing.T) {
	src := "| a | b | c | d |\\n|---|---|---|---|\\n" + strings.Repeat("| `"+strings.Repeat("w", 40)+"` | "+strings.Repeat("v", 60)+" | z | y |\\n", 2)
	l := layoutMarkdown([]byte(strings.ReplaceAll(src, "\\\\n", "\\n")), 300, 16, fakeRuler)
	for _, it := range l.Items {
		if it.Kind == mdBox && it.X+it.W > 300.5 {
			t.Fatalf("a box runs past the page: x=%v w=%v", it.X, it.W)
		}
	}
}
