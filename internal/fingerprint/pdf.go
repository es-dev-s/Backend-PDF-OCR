package fingerprint

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func ExtractPDFText(path string) (text string, pages int, err error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	pages = r.NumPage()
	var b strings.Builder
	limit := pages
	if limit > MaxTextPages {
		limit = MaxTextPages
	}
	for i := 1; i <= limit; i++ {
		p := r.Page(i)
		if p.V.IsNull() {
			continue
		}
		plain, perr := p.GetPlainText(nil)
		if perr != nil {
			continue
		}
		b.WriteString(plain)
		b.WriteByte('\n')
	}
	return b.String(), pages, nil
}

func ExtractPDFPageImages(path string) (files []string, cleanup func(), err error) {
	if _, lookErr := exec.LookPath("pdftoppm"); lookErr == nil {
		files, cleanup, err = renderPDFPages(path)
		if err == nil {
			return files, cleanup, nil
		}
	}
	return extractEmbeddedImages(path)
}

func renderPDFPages(path string) (files []string, cleanup func(), err error) {
	bin, err := exec.LookPath("pdftoppm")
	if err != nil {
		return nil, func() {}, err
	}
	dir, err := os.MkdirTemp("", "ocr-fp-render-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	prefix := filepath.Join(dir, "page")
	cmd := exec.CommandContext(ctx, bin, "-png", "-f", "1", "-l", strconv.Itoa(MaxVisualPages), "-r", "72", path, prefix)
	if err := cmd.Run(); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	files, err = listImages(dir)
	if err != nil || len(files) == 0 {
		cleanup()
		return nil, func() {}, err
	}
	return files, cleanup, nil
}

func extractEmbeddedImages(path string) (files []string, cleanup func(), err error) {
	dir, err := os.MkdirTemp("", "ocr-fp-*")
	if err != nil {
		return nil, func() {}, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	pages := []string{"1-" + strconv.Itoa(MaxVisualPages)}
	if err := api.ExtractImagesFile(path, dir, pages, conf); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	files, err = listImages(dir)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return files, cleanup, nil
}

func listImages(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !IsImageName(e.Name()) {
			continue
		}
		files = append(files, filepath.Join(dir, e.Name()))
	}
	sort.Strings(files)
	return files, nil
}

func ValidPDF(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 8)
	n, _ := f.Read(head)
	return bytes.HasPrefix(head[:n], []byte("%PDF-"))
}
