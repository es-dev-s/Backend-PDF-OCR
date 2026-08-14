package fingerprint

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func ExtractPDFText(path string) (text string, pages int, err error) {
	return extractPDFTextPoppler(path)
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
	return capText(string(out)), pdfPageCount(path), nil
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
