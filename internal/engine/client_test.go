package engine

import "testing"

func TestPrintedTitleRejectsFilenames(t *testing.T) {
	if got := PrintedTitle("3.Our.ME_Project_Study_2.pdf"); got != "" {
		t.Fatalf("filename leaked: %q", got)
	}
	if got := PrintedTitle("notes.pdf"); got != "" {
		t.Fatalf("short filename leaked: %q", got)
	}
	if got := PrintedTitle("Untitled document"); got != "" {
		t.Fatalf("placeholder leaked: %q", got)
	}
	want := "Design and Fabrication of River Cleaning Machine"
	if got := PrintedTitle(want); got != want {
		t.Fatalf("printed title: %q", got)
	}
}

func TestPrintedTitleAcceptsHeadingWithPDFSuffix(t *testing.T) {
	raw := "Egyptian Journal of Chemistry.pdf"
	want := "Egyptian Journal of Chemistry"
	if got := PrintedTitle(raw); got != want {
		t.Fatalf("heading with .pdf: %q", got)
	}
	if !TitleSettled(raw) {
		t.Fatal("heading with .pdf must settle so the form stops Generating")
	}
	if got := PublicTitle(raw); got != want {
		t.Fatalf("public heading: %q", got)
	}
}

func TestDisplayNameUsesEngineTitleNotFilename(t *testing.T) {
	got := DisplayName(Result{
		OK:     true,
		Method: "ocr",
		Title:  "Design and Fabrication of River Cleaning Machine",
	})
	if got != "Design and Fabrication of River Cleaning Machine" {
		t.Fatalf("ocr success: %q", got)
	}
}

func TestDisplayNameUnreadableScan(t *testing.T) {
	got := DisplayName(Result{OK: false, Method: "ocr", Message: "No OCR", Filename: "scan.pdf"})
	if got != UnreadableTitle {
		t.Fatalf("unreadable: %q", got)
	}
}

func TestNewInteractiveGate(t *testing.T) {
	c := New("http://127.0.0.1:9", nil, 1, 3)
	if c == nil || cap(c.fast) != 3 || cap(c.sem) != 1 {
		t.Fatalf("gates sem=%d fast=%d", cap(c.sem), cap(c.fast))
	}
}

func TestParseEngineDetail(t *testing.T) {
	if got := parseEngineDetail([]byte(`{"detail":"PDF exceeds 80 page limit"}`), 400); got != "PDF exceeds 80 page limit" {
		t.Fatalf("string detail: %q", got)
	}
	if got := parseEngineDetail([]byte(`{"detail":[{"msg":"Field required"}]}`), 422); got != "Field required" {
		t.Fatalf("array detail: %q", got)
	}
}

func TestPublicTitleNeverFilename(t *testing.T) {
	if got := PublicTitle("3.Our.ME_Project_Study_2.pdf"); got != UntitledDocument {
		t.Fatalf("filename: %q", got)
	}
	if got := PublicTitle(UnreadableTitle); got != UnreadableTitle {
		t.Fatalf("unreadable: %q", got)
	}
	want := "River Cleaning Machine"
	if got := PublicTitle(want); got != want {
		t.Fatalf("printed: %q", got)
	}
}

func TestTitleSettled(t *testing.T) {
	if TitleSettled(UntitledDocument) {
		t.Fatal("untitled must stay in the retry queue")
	}
	if TitleSettled("notes.pdf") {
		t.Fatal("filename must stay in the retry queue")
	}
	if !TitleSettled("River Cleaning Machine") {
		t.Fatal("printed title is settled")
	}
	if !TitleSettled(UnreadableTitle) {
		t.Fatal("unreadable scan is settled")
	}
}
