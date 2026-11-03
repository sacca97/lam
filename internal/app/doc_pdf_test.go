//go:build gui

package app

import "testing"

const tinyPDF = "%PDF-1.4\n" +
	"1 0 obj<</Type/Catalog/Pages 2 0 R>>endobj\n" +
	"2 0 obj<</Type/Pages/Kids[3 0 R 6 0 R]/Count 2>>endobj\n" +
	"3 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 300 200]/Contents 4 0 R/Resources<</Font<</F1 5 0 R>>>>>>endobj\n" +
	"4 0 obj<</Length 44>>stream\nBT /F1 36 Tf 20 100 Td (Hello) Tj ET\nendstream endobj\n" +
	"5 0 obj<</Type/Font/Subtype/Type1/BaseFont/Helvetica>>endobj\n" +
	"6 0 obj<</Type/Page/Parent 2 0 R/MediaBox[0 0 200 300]>>endobj\n" +
	"trailer<</Root 1 0 R/Size 7>>\n%%EOF\n"

func TestPDFRendersPages(t *testing.T) {
	d, err := openPDF([]byte(tinyPDF))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Pages() != 2 {
		t.Fatalf("pages = %d", d.Pages())
	}
	img, err := d.render(0, 600)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 600 || b.Dy() != 400 {
		t.Fatalf("page 1 is %v, want 600x400", b)
	}
	dark := 0
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] < 100 {
			dark++
		}
	}
	if dark < 200 {
		t.Fatalf("the text did not render (%d dark pixels)", dark)
	}
	if img, err := d.render(1, 200); err != nil || img.Bounds().Dy() != 300 {
		t.Fatalf("page 2: %v %v", img, err)
	}
	if _, err := openPDF([]byte("not a pdf")); err == nil {
		t.Fatal("garbage was accepted")
	}
}
