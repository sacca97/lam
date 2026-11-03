package app

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The window's document viewer opens markdown and PDF files, on this machine
// or on the connected host. A file on the host comes over the connection in
// pieces, so no request is larger than a frame.

type docKind int

const (
	docNone docKind = iota
	docMarkdown
	docPDF
)

const (
	docChunk       = 4 << 20 // bytes asked for per request
	maxMarkdownLen = 2 << 20 // laid out in one go; a README is a few KB
	maxPDFLen      = 128 << 20
)

// documentKind says what a file name is, by its extension.
func documentKind(name string) docKind {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return docMarkdown
	case ".pdf":
		return docPDF
	}
	return docNone
}

func documentLimit(k docKind) int64 {
	if k == docPDF {
		return maxPDFLen
	}
	return maxMarkdownLen
}

// readFileChunk reads up to length bytes of a regular file from offset. size
// and modified describe the whole file.
func readFileChunk(path string, offset int64, length int) (data []byte, size, modified int64, err error) {
	if !filepath.IsAbs(path) {
		return nil, 0, 0, errors.New("the path must be absolute")
	}
	// The host reads documents only, whatever it is asked.
	if documentKind(path) == docNone {
		return nil, 0, 0, fmt.Errorf("%s is not a markdown or PDF file", filepath.Base(path))
	}
	if length <= 0 || length > docChunk {
		length = docChunk
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, 0, fmt.Errorf("%s is not a regular file", path)
	}
	if offset < 0 || offset > info.Size() {
		return nil, 0, 0, errors.New("offset outside the file")
	}
	data = make([]byte, min(int64(length), info.Size()-offset))
	if _, err := f.ReadAt(data, offset); err != nil && err != io.EOF {
		return nil, 0, 0, err
	}
	return data, info.Size(), info.ModTime().UnixNano(), nil
}

// documentSource is where a document is read from: this machine or the host.
type documentSource interface {
	ReadFileChunk(path string, offset int64, length int) (data []byte, size, modified int64, err error)
}

type documentCompleter interface {
	CompleteDocumentPath(input, base string) ([]string, error)
}

type localDocumentSource struct{}

func (localDocumentSource) ReadFileChunk(path string, offset int64, length int) ([]byte, int64, int64, error) {
	return readFileChunk(path, offset, length)
}

func (localDocumentSource) CompleteDocumentPath(input, base string) ([]string, error) {
	return completePaths(input, base, true)
}

// document is a file read whole.
type document struct {
	Path     string
	Kind     docKind
	Data     []byte
	Modified time.Time
}

// readDocument reads the file at an absolute path, refusing what is too big.
func readDocument(src documentSource, path string) (*document, error) {
	kind := documentKind(path)
	if kind == docNone {
		return nil, fmt.Errorf("%s is not a markdown or PDF file", filepath.Base(path))
	}
	limit := documentLimit(kind)
	var data []byte
	var modified int64
	for {
		piece, size, mod, err := src.ReadFileChunk(path, int64(len(data)), docChunk)
		if err != nil {
			return nil, err
		}
		if size > limit {
			return nil, fmt.Errorf("%s is %d MB; the viewer opens files up to %d MB", filepath.Base(path), size>>20, limit>>20)
		}
		if len(data) > 0 && mod != modified {
			return nil, errors.New("the file changed while it was being read")
		}
		data, modified = append(data, piece...), mod
		if int64(len(data)) >= size {
			break
		}
		if len(piece) == 0 {
			return nil, errors.New("the file ended early")
		}
	}
	return &document{Path: path, Kind: kind, Data: data, Modified: time.Unix(0, modified)}, nil
}
