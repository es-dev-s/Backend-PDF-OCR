package documents

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ocr-backend/internal/engine"
)

const maxSimilarKept = 100

type titleCandidate struct {
	ID          uuid.UUID
	DocumentID  uuid.UUID
	Title       string
	TitleNorm   string
	ERP         string
	Client      string
	Member      string
	Uploaded    time.Time
	ContentType string
}

type similarHit struct {
	MatchedSourceID uuid.UUID
	DocumentID      uuid.UUID
	Score           float64
}

func (r *Repo) attachTitleSimilar(ctx context.Context, db *pgxpool.Pool, sourceIDs []uuid.UUID, sourceByID map[uuid.UUID]*Source) error {
	rows, err := db.Query(ctx, `
		SELECT ts.id, ts.source_id, ts.matched_source_id, ts.score,
		       COALESCE(ms.title, ''), COALESCE(md.erp, ''), COALESCE(md.client, ''),
		       COALESCE(md.member, ''), ms.created_at, md.id, COALESCE(ms.content_type, '')
		FROM title_similarities ts
		JOIN sources ms ON ms.id = ts.matched_source_id
		JOIN documents md ON md.id = ms.document_id
		WHERE ts.source_id = ANY($1)
		ORDER BY ts.score DESC, ms.created_at ASC`, sourceIDs)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var m TitleSimilarMatch
		var sourceID uuid.UUID
		var matchedID uuid.UUID
		var matchedDoc uuid.UUID
		if err := rows.Scan(
			&m.ID, &sourceID, &matchedID, &m.Score,
			&m.Title, &m.ERP, &m.Client, &m.Member, &m.Uploaded, &matchedDoc, &m.ContentType,
		); err != nil {
			return err
		}
		m.SourceID = matchedID
		m.DocumentID = matchedDoc
		m.Title = engine.PublicTitle(m.Title)
		m.FileURL = fileURL(matchedDoc, matchedID)
		if src := sourceByID[sourceID]; src != nil {
			src.TitleSimilar = append(src.TitleSimilar, m)
		}
	}
	return rows.Err()
}

func foldTitleSimilar(d *Document) {
	if d == nil {
		return
	}
	best := make(map[string]TitleSimilarMatch)
	for i := range d.Sources {
		src := &d.Sources[i]
		kept := src.TitleSimilar[:0]
		for _, m := range src.TitleSimilar {
			if m.DocumentID == uuid.Nil {
				continue
			}
			if src.ID != uuid.Nil && m.SourceID == src.ID {
				continue
			}
			if m.SourceID == uuid.Nil && m.DocumentID == d.ID {
				continue
			}
			kept = append(kept, m)
			key := "d:" + m.DocumentID.String()
			if m.DocumentID == d.ID {
				key = "s:" + m.SourceID.String()
			}
			prev, ok := best[key]
			if !ok || m.Score > prev.Score {
				best[key] = m
			}
		}
		src.TitleSimilar = kept
		if len(src.TitleSimilar) == 0 {
			src.TitleSimilar = nil
		}
	}
	if len(best) == 0 {
		d.TitleSimilar = nil
		d.TitleSimilarCount = 0
		return
	}
	out := make([]TitleSimilarMatch, 0, len(best))
	for _, m := range best {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].Uploaded.Before(out[j].Uploaded)
		}
		return out[i].Score > out[j].Score
	})
	if len(out) > 50 {
		out = out[:50]
	}
	d.TitleSimilar = out
	d.TitleSimilarCount = len(out)
}

func (r *Repo) ListNeedingSimilar(ctx context.Context, limit int) ([]Source, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	if limit < 1 {
		limit = 50
	}
	rows, err := db.Query(ctx, `
		SELECT id, document_id, title, storage_key, content_type, size_bytes,
		       content_sha256, uniqueness, score, created_at
		FROM sources
		WHERE title_similar_at IS NULL
		  AND needs_title = FALSE
		ORDER BY created_at ASC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Source, 0, limit)
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.DocumentID, &s.Title, &s.StorageKey, &s.ContentType, &s.SizeBytes, &s.SHA256, &s.Uniqueness, &s.Score, &s.Uploaded); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *Repo) SetTitleNorm(ctx context.Context, id uuid.UUID, titleNorm string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `UPDATE sources SET title_norm=$2 WHERE id=$1`, id, titleNorm)
	return err
}

func (r *Repo) ListTitleCandidates(ctx context.Context, sourceID uuid.UUID) ([]titleCandidate, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT s.id, s.document_id, s.title, s.title_norm, s.content_type, s.created_at,
		       COALESCE(d.erp, ''), COALESCE(d.client, ''), COALESCE(d.member, '')
		FROM sources s
		JOIN documents d ON d.id = s.document_id
		WHERE s.title_norm <> ''
		  AND s.id <> $1`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]titleCandidate, 0, 64)
	for rows.Next() {
		var c titleCandidate
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.Title, &c.TitleNorm, &c.ContentType, &c.Uploaded, &c.ERP, &c.Client, &c.Member); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *Repo) HasUnreadyTitlePeers(ctx context.Context, documentID, sourceID uuid.UUID) (bool, error) {
	db, err := r.db()
	if err != nil {
		return false, err
	}
	var n int
	// Wait only while a sibling PDF is still extracting a printed title.
	// Settled sources with an empty title_norm (images, unreadable scans)
	// must not block, or mixed uploads never stamp similarities.
	err = db.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM sources
		WHERE document_id = $1
		  AND id <> $2
		  AND needs_title = TRUE`, documentID, sourceID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *Repo) ReplaceSimilar(ctx context.Context, sourceID uuid.UUID, titleNorm string, hits []similarHit) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		locked := map[uuid.UUID]struct{}{sourceID: {}}
		for _, hit := range hits {
			if hit.MatchedSourceID != uuid.Nil {
				locked[hit.MatchedSourceID] = struct{}{}
			}
		}
		for _, id := range sortedUUIDs(locked) {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1::text, 0))`, "tsim:"+id.String()); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `
			UPDATE sources
			SET title_norm=$2, title_similar_at=now()
			WHERE id=$1`, sourceID, titleNorm); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			DELETE FROM title_similarities
			WHERE source_id=$1 OR matched_source_id=$1`, sourceID); err != nil {
			return err
		}
		if len(hits) > maxSimilarKept {
			hits = hits[:maxSimilarKept]
		}
		for _, hit := range hits {
			if hit.MatchedSourceID == uuid.Nil || hit.MatchedSourceID == sourceID {
				continue
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO title_similarities (id, source_id, matched_source_id, score)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT (source_id, matched_source_id) DO UPDATE SET score = EXCLUDED.score`,
				uuid.New(), sourceID, hit.MatchedSourceID, hit.Score); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO title_similarities (id, source_id, matched_source_id, score)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT (source_id, matched_source_id) DO UPDATE SET score = EXCLUDED.score`,
				uuid.New(), hit.MatchedSourceID, sourceID, hit.Score); err != nil {
				return err
			}
		}
		return nil
	})
}
