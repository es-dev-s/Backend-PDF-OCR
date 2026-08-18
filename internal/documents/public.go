package documents

import "github.com/google/uuid"

func publiclyShareable(status Status) bool {
	return status == StatusCompleted || status == StatusDuplicate || status == StatusOriginal
}

// PublicView strips private fields and rewrites file links for the unlisted
// share page. Only finished documents are shareable — processing and pending
// review stay hidden so a duplicate cannot be downloaded before an admin acts.
func PublicView(doc *Document) error {
	if doc == nil || !publiclyShareable(doc.Status) {
		return ErrNotFound
	}
	doc.OwnerID = nil
	doc.ReviewNote = ""
	doc.ReviewRequestedAt = nil
	doc.URL = publicPagePath(doc.ID)
	if len(doc.Sources) > 0 {
		doc.FileURL = publicFileURL(doc.ID, doc.Sources[0].ID)
	} else {
		doc.FileURL = doc.URL
	}
	for i := range doc.Sources {
		doc.Sources[i].FileURL = publicFileURL(doc.ID, doc.Sources[i].ID)
		for j := range doc.Sources[i].Duplicates {
			match := &doc.Sources[i].Duplicates[j]
			// Keep match labels on the shared row, but do not hand out other
			// documents' IDs or public file URLs.
			match.FileURL = ""
			match.DocumentID = uuid.Nil
			match.SourceID = uuid.Nil
		}
		doc.Sources[i].TitleSimilar = nil
	}
	doc.TitleSimilar = nil
	doc.TitleSimilarCount = 0
	return nil
}
