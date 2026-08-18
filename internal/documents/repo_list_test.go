package documents

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
)

func TestIndexDocumentsPointersStayLive(t *testing.T) {
	docs := make([]Document, 0, 2)
	for i := 0; i < 80; i++ {
		docs = append(docs, Document{ID: uuid.New(), Client: fmt.Sprintf("c-%d", i)})
	}
	ids, byID := indexDocuments(docs)
	if len(ids) != len(docs) || len(byID) != len(docs) {
		t.Fatalf("index size ids=%d byID=%d docs=%d", len(ids), len(byID), len(docs))
	}
	for i := range docs {
		ptr := byID[docs[i].ID]
		if ptr == nil {
			t.Fatalf("missing %s", docs[i].ID)
		}
		ptr.Sources = []Source{{Title: "kept"}}
	}
	if len(docs[0].Sources) != 1 || docs[0].Sources[0].Title != "kept" {
		t.Fatal("first document lost attached sources")
	}
	if len(docs[len(docs)-1].Sources) != 1 || docs[len(docs)-1].Sources[0].Title != "kept" {
		t.Fatal("last document lost attached sources")
	}
}
