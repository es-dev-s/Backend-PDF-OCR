package blob

import "testing"

func TestCleanKey(t *testing.T) {
	got, err := cleanKey(`folder\doc.pdf`)
	if err != nil || got != "folder/doc.pdf" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := cleanKey("../secret"); err == nil {
		t.Fatal("expected invalid key")
	}
	if joinPrefix("ocr/v1", "a/b.pdf") != "ocr/v1/a/b.pdf" {
		t.Fatal("prefix join")
	}
}
