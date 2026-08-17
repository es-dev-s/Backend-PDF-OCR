package engine

import "testing"

func TestPrintedTitleRejectsFilenames(t *testing.T) {
	if got := PrintedTitle("3.Our.ME_Project_Study_2.pdf"); got != "" {
		t.Fatalf("filename leaked: %q", got)
	}
	if got := PrintedTitle("Untitled document"); got != "" {
		t.Fatalf("placeholder leaked: %q", got)
	}
	want := "Design and Fabrication of River Cleaning Machine"
	if got := PrintedTitle(want); got != want {
		t.Fatalf("printed title: %q", got)
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
