package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"strings"
)

type Result struct {
	SHA256      string
	TextNormSHA string
	SimHash     uint64
	PHash       uint64
	HasText     bool
	HasVisual   bool
	PageCount   int
	Kind        string
}

func Analyze(path, filename, contentType string) (Result, error) {
	var out Result
	sum, err := hashFile(path)
	if err != nil {
		return out, err
	}
	out.SHA256 = sum

	head, _ := readHead(path, 16)
	kind := Sniff(head, filename)
	if kind == "" {
		ct := strings.ToLower(contentType)
		if strings.Contains(ct, "pdf") {
			kind = "pdf"
		} else if strings.HasPrefix(ct, "image/") {
			kind = "image"
		}
	}
	out.Kind = kind

	switch kind {
	case "pdf":
		analyzePDF(path, &out)
	case "image":
		analyzeImage(path, &out)
	}
	return out, nil
}

func analyzePDF(path string, out *Result) {
	if !ValidPDF(path) {
		return
	}
	text, pages, err := ExtractPDFText(path)
	if err == nil {
		out.PageCount = pages
		norm := Normalize(text)
		runes := []rune(norm)
		tokens := Tokens(norm)
		if textIsSubstantial(len(runes), len(tokens), pages) {
			out.HasText = true
			sum := sha256.Sum256([]byte(norm))
			out.TextNormSHA = hex.EncodeToString(sum[:])
			out.SimHash = SimHash64(tokens)
			return
		}
	}
	files, cleanup, err := ExtractPDFPageImages(path)
	if err != nil {
		return
	}
	defer cleanup()
	if len(files) == 0 {
		return
	}
	h, err := PerceptionHashFile(files[0])
	if err != nil {
		return
	}
	out.PHash = h
	out.HasVisual = true
}

func textIsSubstantial(runes, tokens, pages int) bool {
	if runes < MinTextRunes || tokens < MinTokens {
		return false
	}
	if pages <= 1 {
		return true
	}
	return runes >= pages*MinRunesPerPage
}

func analyzeImage(path string, out *Result) {
	out.PageCount = 1
	h, err := PerceptionHashFile(path)
	if err != nil {
		return
	}
	out.PHash = h
	out.HasVisual = true
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	got, err := io.ReadFull(f, buf)
	if got > 0 {
		return buf[:got], nil
	}
	return nil, err
}
