package httpapi

import "testing"

func TestSafeDownloadNameUsesPrintedTitle(t *testing.T) {
	got := safeDownloadName("Design and Fabrication of River Cleaning Machine", "application/pdf")
	if got != "Design and Fabrication of River Cleaning Machine.pdf" {
		t.Fatalf("got %q", got)
	}
}

func TestSafeDownloadNamePlaceholder(t *testing.T) {
	got := safeDownloadName("document", "application/pdf")
	if got != "document.pdf" {
		t.Fatalf("got %q", got)
	}
}

func TestSafeDownloadNameStripsPath(t *testing.T) {
	got := safeDownloadName("CFD Analysis / Pump Study", "application/pdf")
	if got != "CFD Analysis - Pump Study.pdf" {
		t.Fatalf("got %q", got)
	}
}
