package documents

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestFoldTitleSimilarUniquesByDocument(t *testing.T) {
	docID := uuid.New()
	otherA := uuid.New()
	otherB := uuid.New()
	now := time.Now().UTC()
	d := Document{
		ID: docID,
		Sources: []Source{
			{
				TitleSimilar: []TitleSimilarMatch{
					{ID: uuid.New(), DocumentID: otherA, Title: "The jorney of man", Score: 0.94, Uploaded: now},
					{ID: uuid.New(), DocumentID: otherA, Title: "The jorney of man", Score: 0.91, Uploaded: now},
					{ID: uuid.New(), DocumentID: docID, Title: "self", Score: 1, Uploaded: now},
				},
			},
			{
				TitleSimilar: []TitleSimilarMatch{
					{ID: uuid.New(), DocumentID: otherB, Title: "The journey of man", Score: 0.99, Uploaded: now},
				},
			},
		},
	}
	foldTitleSimilar(&d)
	if d.TitleSimilarCount != 2 {
		t.Fatalf("count=%d", d.TitleSimilarCount)
	}
	if d.TitleSimilar[0].DocumentID != otherB || d.TitleSimilar[0].Score != 0.99 {
		t.Fatalf("expected highest first: %+v", d.TitleSimilar[0])
	}
	if d.TitleSimilar[1].DocumentID != otherA || d.TitleSimilar[1].Score != 0.94 {
		t.Fatalf("expected best A: %+v", d.TitleSimilar[1])
	}
	if len(d.Sources[0].TitleSimilar) != 2 {
		t.Fatalf("self match must be dropped from source, got %d", len(d.Sources[0].TitleSimilar))
	}
}
