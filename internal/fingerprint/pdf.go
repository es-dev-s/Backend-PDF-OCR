package fingerprint

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/ledongthuc/pdf"
)

func ExtractPDFText(path string) (text string, pages int, err error) {
	if text, pages, err = extractPDFTextPoppler(path); err == nil {
		return capText(text), pages, nil
	}
	info, statErr := os.Stat(path)
	if statErr == nil && info.Size() > 8<<20 {
		return "", 0, nil
	}
	text, pages, err = extractPDFTextGo(path)
	if err != nil {
		return "", pages, err
	}
	return capText(text), pages, nil
}

func extractPDFTextPoppler(path string) (string, int, error) {
	bin, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", 0, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-f", "1", "-l", strconv.Itoa(MaxTextPages), "-enc", "UTF-8", "-q", path, "-")
	out, err := cmd.Output()
	if err != nil {
		return "", 0, err
	}
	return string(out), pdfPageCount(path), nil
}

func pdfPageCount(path string) int {
	bin, err := exec.LookPath("pdfinfo")
	if err != nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-q", path)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "Pages:") {
			continue
		}
		n, convErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Pages:")))
		if convErr == nil && n > 0 {
			return n
		}
	}
	return 0
}

func extractPDFTextGo(path string) (text string, pages int, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("pdf parse panic: %v", rec)
		}
	}()
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
		if b.Len() >= MaxTextBytes {
			break
		}
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

func capText(s string) string {
	if len(s) <= MaxTextBytes {
		return s
	}
	return s[:MaxTextBytes]
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
