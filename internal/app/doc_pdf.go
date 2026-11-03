//go:build gui

package app

import (
	"errors"
	"fmt"
	"image"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// PDFs are drawn by PDFium compiled to WebAssembly and run by a pure-Go
// runtime: no system library, no cgo. The runtime takes a couple of seconds to
// start the first time, so it starts when the first PDF is opened; a page then
// renders in a few milliseconds.

var (
	pdfPoolMu sync.Mutex
	pdfPool   pdfium.Pool
)

// pdfRuntime starts the renderer on first use. A failure is not remembered: the
// next PDF opened tries again.
func pdfRuntime() (pdfium.Pool, error) {
	pdfPoolMu.Lock()
	defer pdfPoolMu.Unlock()
	if pdfPool != nil {
		return pdfPool, nil
	}
	pool, err := webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 2, MaxTotal: 12})
	if err != nil {
		return nil, err
	}
	pdfPool = pool
	return pool, nil
}

// Each open PDF holds one instance for as long as it is open, and a reload
// briefly needs a second, so the pool allows a dozen.

// pdfDoc is an open PDF. Its methods may be called from any goroutine; they run
// one at a time.
type pdfDoc struct {
	mu    sync.Mutex
	inst  pdfium.Pdfium
	doc   references.FPDF_DOCUMENT
	sizes []image.Point // page size in points, rounded
	done  bool
}

// openPDF parses data and reads every page's size.
func openPDF(data []byte) (*pdfDoc, error) {
	pool, err := pdfRuntime()
	if err != nil {
		return nil, fmt.Errorf("start the PDF renderer: %w", err)
	}
	inst, err := pool.GetInstance(15 * time.Second)
	if err != nil {
		return nil, err
	}
	opened, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		inst.Close()
		return nil, fmt.Errorf("open the PDF: %w", err)
	}
	d := &pdfDoc{inst: inst, doc: opened.Document}
	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: d.doc})
	if err != nil || count.PageCount == 0 {
		d.Close()
		return nil, errors.New("the PDF has no pages")
	}
	for i := 0; i < count.PageCount; i++ {
		size, err := inst.GetPageSize(&requests.GetPageSize{Page: d.page(i)})
		if err != nil {
			d.sizes = append(d.sizes, image.Pt(612, 792))
			continue
		}
		d.sizes = append(d.sizes, image.Pt(max(1, int(size.Width+0.5)), max(1, int(size.Height+0.5))))
	}
	return d, nil
}

func (d *pdfDoc) page(i int) requests.Page {
	return requests.Page{ByIndex: &requests.PageByIndex{Document: d.doc, Index: i}}
}

func (d *pdfDoc) Pages() int { return len(d.sizes) }

// render draws page i (from 0) width pixels wide.
func (d *pdfDoc) render(i, width int) (*image.RGBA, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return nil, errors.New("the PDF is closed")
	}
	if i < 0 || i >= len(d.sizes) {
		return nil, errors.New("no such page")
	}
	size := d.sizes[i]
	height := max(1, width*size.Y/size.X)
	// A very tall page, or a big zoom, would ask for gigabytes: the picture is
	// made smaller and drawn stretched.
	const maxHeight, maxPixels = 12000, 40 << 20
	if height > maxHeight {
		width, height = max(1, width*maxHeight/height), maxHeight
	}
	for width*height > maxPixels {
		width, height = max(1, width*9/10), max(1, height*9/10)
	}
	r, err := d.inst.RenderPageInPixels(&requests.RenderPageInPixels{Page: d.page(i), Width: width, Height: height, RenderForm: true, Document: &d.doc})
	if err != nil {
		return nil, err
	}
	return r.Result.Image, nil
}

func (d *pdfDoc) Close() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done {
		return
	}
	d.done = true
	d.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: d.doc})
	d.inst.Close()
}
