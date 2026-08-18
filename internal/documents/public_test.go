package documents

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPublicViewHidesPendingReview(t *testing.T) {
	doc := Document{ID: uuid.New(), Status: StatusPendingReview, ReviewNote: "secret"}
	if err := PublicView(&doc); err != ErrNotFound {
		t.Fatalf("pending review must be hidden, got %v", err)
	}
}

func TestPublicViewHidesProcessing(t *testing.T) {
	doc := Document{ID: uuid.New(), Status: StatusProcessing}
	if err := PublicView(&doc); err != ErrNotFound {
		t.Fatalf("processing must be hidden, got %v", err)
	}
}

func TestPublicViewStripsPrivateFields(t *testing.T) {
	id := uuid.New()
	owner := uuid.New()
	srcID := uuid.New()
	matchDoc := uuid.New()
	matchSrc := uuid.New()
	requested := time.Now().UTC()
	doc := Document{
		ID:                id,
		Status:            StatusCompleted,
		OwnerID:           &owner,
		ReviewNote:        "keep this private",
		ReviewRequestedAt: &requested,
		Sources: []Source{{
			ID: srcID,
			Duplicates: []DuplicateMatch{{
				ID:         uuid.New(),
				DocumentID: matchDoc,
				SourceID:   matchSrc,
				FileURL:    fileURL(matchDoc, matchSrc),
				Title:      "Original",
				Client:     "Acme",
			}},
			TitleSimilar: []TitleSimilarMatch{{
				ID:         uuid.New(),
				DocumentID: matchDoc,
				SourceID:   matchSrc,
				FileURL:    fileURL(matchDoc, matchSrc),
				Title:      "Near title",
				ERP:        "ERP-10002",
				Score:      0.94,
			}},
		}},
		TitleSimilar: []TitleSimilarMatch{{
			Title: "Near title",
			ERP:   "ERP-10002",
			Score: 0.94,
		}},
		TitleSimilarCount: 1,
	}
	decorate(&doc)
	if err := PublicView(&doc); err != nil {
		t.Fatal(err)
	}
	if doc.OwnerID != nil {
		t.Fatal("owner must be omitted")
	}
	if doc.ReviewNote != "" || doc.ReviewRequestedAt != nil {
		t.Fatal("review fields must be omitted")
	}
	if doc.URL != "/d/"+id.String() {
		t.Fatalf("public page=%s", doc.URL)
	}
	if !strings.Contains(doc.FileURL, "/v1/public/documents/"+id.String()) {
		t.Fatalf("file url=%s", doc.FileURL)
	}
	match := doc.Sources[0].Duplicates[0]
	if match.FileURL != "" || match.DocumentID != uuid.Nil || match.SourceID != uuid.Nil {
		t.Fatalf("match must not leak other documents: %+v", match)
	}
	if match.Title != "Original" || match.Client != "Acme" {
		t.Fatalf("match labels=%+v", match)
	}
	if len(doc.TitleSimilar) != 0 || doc.TitleSimilarCount != 0 || len(doc.Sources[0].TitleSimilar) != 0 {
		t.Fatal("public view must not leak similar titles")
	}
}

func TestDecorateKeepsPrintedTitleOutOfPending(t *testing.T) {
	d := Document{
		ERP: "ERP-1",
		Sources: []Source{
			{Title: "Kinetic Study of Esterification", NeedsTitle: true},
			{Title: "Untitled document", NeedsTitle: true},
		},
	}
	decorate(&d)
	if d.TitlePending {
		t.Fatal("a document with a printed title must not sit in Titles in progress")
	}
	if d.Title != "Kinetic Study of Esterification" {
		t.Fatalf("title=%q", d.Title)
	}
}

func TestDecoratePendingWhenEveryTitleIsPlaceholder(t *testing.T) {
	d := Document{
		ERP: "ERP-2",
		Sources: []Source{
			{Title: "Untitled document", NeedsTitle: true},
			{Title: "scan.pdf", NeedsTitle: true},
		},
	}
	decorate(&d)
	if !d.TitlePending {
		t.Fatal("placeholder titles must stay in progress")
	}
}

func TestDecorateURLIsPublicPage(t *testing.T) {
	id := uuid.New()
	doc := Document{ID: id, Member: "Ada"}
	decorate(&doc)
	if doc.URL != "/d/"+id.String() {
		t.Fatalf("url=%s", doc.URL)
	}
	if doc.Uploader != "Ada" {
		t.Fatalf("uploader=%s", doc.Uploader)
	}
}
