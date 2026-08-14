package documents

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"ocr-backend/internal/fingerprint"
)

type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (r *Repo) FinalizeFingerprint(ctx context.Context, src Source, fp fingerprint.Result) (FinalizeResult, error) {
	var out FinalizeResult
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		if fp.SHA256 == "" {
			return nil
		}
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "sha:"+fp.SHA256); err != nil {
			return err
		}
		if fp.SimHash != 0 {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "sim:"+u64(fp.SimHash)); err != nil {
				return err
			}
		}
		if fp.PHash != 0 {
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, "ph:"+u64(fp.PHash)); err != nil {
				return err
			}
		}

		var docID uuid.UUID
		err := tx.QueryRow(ctx, `SELECT document_id FROM sources WHERE id=$1`, src.ID).Scan(&docID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}

		matches := make([]HashMatch, 0, 8)
		exact, err := queryExact(ctx, tx, src.ID, fp.SHA256)
		if err != nil {
			return err
		}
		matches = mergeMatches(matches, exact)

		if fp.HasText && fp.TextNormSHA != "" {
			textExact, err := queryTextExact(ctx, tx, src.ID, fp.TextNormSHA)
			if err != nil {
				return err
			}
			matches = mergeMatches(matches, textExact)
			near, err := queryLSH(ctx, tx, src.ID, "simhash", fp.SimHash, fingerprint.SimHashMaxDist)
			if err != nil {
				return err
			}
			matches = mergeMatches(matches, near)
		} else if fp.HasVisual && fp.PHash != 0 {
			vis, err := queryLSH(ctx, tx, src.ID, "phash", fp.PHash, fingerprint.PHashMaxDist)
			if err != nil {
				return err
			}
			matches = mergeMatches(matches, vis)
		}

		touched := map[uuid.UUID]struct{}{docID: {}}
		for _, m := range matches {
			touched[m.DocumentID] = struct{}{}
		}
		ids := sortedUUIDs(touched)
		if len(ids) > 0 {
			lockRows, err := tx.Query(ctx, `
				SELECT id FROM documents
				WHERE id = ANY($1)
				ORDER BY id
				FOR UPDATE`, ids)
			if err != nil {
				return err
			}
			lockRows.Close()
		}

		if err := tx.QueryRow(ctx, `SELECT document_id FROM sources WHERE id=$1 FOR UPDATE`, src.ID).Scan(&docID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}

		matches, err = livingMatches(ctx, tx, matches)
		if err != nil {
			return err
		}
		matches = confidentMatches(fp, matches)

		kind := "unique"
		if fp.HasText {
			kind = "text"
		} else if fp.HasVisual {
			kind = "visual"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE sources SET
				content_sha256=$2,
				text_norm_sha256=$3,
				simhash=$4,
				phash=$5,
				has_text_layer=$6,
				page_count=$7,
				fingerprint_kind=$8
			WHERE id=$1`,
			src.ID, fp.SHA256, nullStr(fp.TextNormSHA), nullI64(fp.SimHash),
			nullI64(fp.PHash), fp.HasText, fp.PageCount, kind); err != nil {
			return err
		}
		if err := replaceLSH(ctx, tx, src.ID, fp); err != nil {
			return err
		}

		if len(matches) == 0 {
			out.Uniqueness = Unique
			if _, err := tx.Exec(ctx, `
				UPDATE sources SET uniqueness='unique', score=NULL WHERE id=$1`, src.ID); err != nil {
				return err
			}
		} else {
			best := matches[0].Score
			for _, m := range matches {
				if m.Score > best {
					best = m.Score
				}
			}
			srcRole := Original
			for _, m := range matches {
				if !uploadedFirst(src.ID, src.Uploaded, m.SourceID, m.Uploaded) {
					srcRole = Duplicate
					break
				}
			}
			out.Uniqueness = srcRole
			if _, err := tx.Exec(ctx, `
				UPDATE sources SET uniqueness=$2, score=$3 WHERE id=$1`, src.ID, srcRole, best); err != nil {
				return err
			}
			var erp string
			_ = tx.QueryRow(ctx, `SELECT erp FROM documents WHERE id=$1`, docID).Scan(&erp)
			for _, m := range matches {
				matchRole := Duplicate
				if uploadedFirst(m.SourceID, m.Uploaded, src.ID, src.Uploaded) {
					matchRole = Original
				}
				if _, err := tx.Exec(ctx, `
					UPDATE sources SET
						uniqueness = CASE
							WHEN $2 = 'original' AND uniqueness = 'duplicate' THEN uniqueness
							ELSE $2
						END,
						score = GREATEST(COALESCE(score, 0), $3)
					WHERE id = $1`, m.SourceID, matchRole, m.Score); err != nil {
					return err
				}
				if err := insertDup(ctx, tx, src.ID, m.SourceID, m.Title, m.ERP, m.Score, m.Uploaded, m.Kind); err != nil {
					return err
				}
				if err := insertDup(ctx, tx, m.SourceID, src.ID, src.Title, erp, m.Score, src.Uploaded, m.Kind); err != nil {
					return err
				}
			}
		}

		if _, err := tx.Exec(ctx, `
			UPDATE documents d SET
				status = CASE
					WHEN EXISTS (SELECT 1 FROM sources s WHERE s.document_id = d.id AND s.content_sha256 IS NULL)
						THEN 'processing'
					WHEN EXISTS (
						SELECT 1 FROM sources s
						WHERE s.document_id = d.id
						  AND s.uniqueness IN ('duplicate', 'original')
					)
						THEN 'duplicate'
					ELSE 'completed'
				END,
				updated_at = now()
			WHERE d.id = ANY($1)`, ids); err != nil {
			return err
		}

		nrows, err := tx.Query(ctx, `
			UPDATE documents SET notified_at = now()
			WHERE id = ANY($1)
			  AND notified_at IS NULL
			  AND status <> 'processing'
			RETURNING id`, ids)
		if err != nil {
			return err
		}
		notified := make([]uuid.UUID, 0)
		for nrows.Next() {
			var id uuid.UUID
			if err := nrows.Scan(&id); err != nil {
				nrows.Close()
				return err
			}
			notified = append(notified, id)
		}
		nrows.Close()
		if err := nrows.Err(); err != nil {
			return err
		}

		out.Touched = ids
		out.Notified = notified
		return nil
	})
	return out, err
}

func queryExact(ctx context.Context, q querier, except uuid.UUID, sha string) ([]HashMatch, error) {
	rows, err := q.Query(ctx, `
		SELECT s.id, s.document_id, s.title, d.erp, d.client, d.member, s.uniqueness, s.created_at
		FROM sources s
		JOIN documents d ON d.id = s.document_id
		WHERE s.content_sha256 = $1 AND s.id <> $2
		ORDER BY s.id`, sha, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMatches(rows, fingerprint.KindExact, 100)
}

func queryTextExact(ctx context.Context, q querier, except uuid.UUID, norm string) ([]HashMatch, error) {
	rows, err := q.Query(ctx, `
		SELECT s.id, s.document_id, s.title, d.erp, d.client, d.member, s.uniqueness, s.created_at, s.page_count
		FROM sources s
		JOIN documents d ON d.id = s.document_id
		WHERE s.text_norm_sha256 = $1 AND s.id <> $2
		ORDER BY s.id`, norm, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]HashMatch, 0)
	for rows.Next() {
		var m HashMatch
		if err := rows.Scan(&m.SourceID, &m.DocumentID, &m.Title, &m.ERP, &m.Client, &m.Member, &m.Uniqueness, &m.Uploaded, &m.PageCount); err != nil {
			return nil, err
		}
		m.Kind = fingerprint.KindText
		m.Score = 99.5
		out = append(out, m)
	}
	return out, rows.Err()
}

func queryLSH(ctx context.Context, q querier, except uuid.UUID, kind string, hash uint64, maxDist int) ([]HashMatch, error) {
	if hash == 0 {
		return nil, nil
	}
	var sql string
	matchKind := fingerprint.KindText
	switch kind {
	case "simhash":
		sql = lshQuery("s.simhash")
	case "phash":
		sql = lshQuery("s.phash")
		matchKind = fingerprint.KindVisual
	default:
		return nil, nil
	}
	bands := fingerprint.Bands(hash)
	rows, err := q.Query(ctx, sql, kind, except, int(bands[0]), int(bands[1]), int(bands[2]), int(bands[3]))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]HashMatch, 0)
	for rows.Next() {
		var m HashMatch
		var raw *int64
		var pages *int
		if err := rows.Scan(&m.SourceID, &m.DocumentID, &m.Title, &m.ERP, &m.Client, &m.Member, &m.Uniqueness, &m.Uploaded, &pages, &raw); err != nil {
			return nil, err
		}
		if raw == nil {
			continue
		}
		dist := fingerprint.Hamming(hash, uint64(*raw))
		if dist > maxDist {
			continue
		}
		if pages != nil {
			m.PageCount = *pages
		}
		m.Kind = matchKind
		m.Score = fingerprint.ScoreFromDistance(dist, 64)
		out = append(out, m)
	}
	return out, rows.Err()
}

func lshQuery(hashCol string) string {
	return `
		SELECT DISTINCT s.id, s.document_id, s.title, d.erp, d.client, d.member, s.uniqueness, s.created_at, s.page_count, ` + hashCol + `
		FROM fingerprint_lsh l
		JOIN sources s ON s.id = l.source_id
		JOIN documents d ON d.id = s.document_id
		WHERE l.kind = $1
		  AND l.source_id <> $2
		  AND s.content_sha256 IS NOT NULL
		  AND (
		    (l.band = 0 AND l.bucket = $3) OR
		    (l.band = 1 AND l.bucket = $4) OR
		    (l.band = 2 AND l.bucket = $5) OR
		    (l.band = 3 AND l.bucket = $6)
		  )`
}

func scanMatches(rows pgx.Rows, kind string, score float64) ([]HashMatch, error) {
	out := make([]HashMatch, 0)
	for rows.Next() {
		var m HashMatch
		if err := rows.Scan(&m.SourceID, &m.DocumentID, &m.Title, &m.ERP, &m.Client, &m.Member, &m.Uniqueness, &m.Uploaded); err != nil {
			return nil, err
		}
		m.Kind = kind
		m.Score = score
		out = append(out, m)
	}
	return out, rows.Err()
}

func mergeMatches(dst, src []HashMatch) []HashMatch {
	if len(src) == 0 {
		return dst
	}
	idx := make(map[uuid.UUID]int, len(dst))
	for i, m := range dst {
		idx[m.SourceID] = i
	}
	for _, m := range src {
		if i, ok := idx[m.SourceID]; ok {
			if m.Score > dst[i].Score {
				dst[i] = m
			}
			continue
		}
		idx[m.SourceID] = len(dst)
		dst = append(dst, m)
	}
	return dst
}

func livingMatches(ctx context.Context, tx pgx.Tx, matches []HashMatch) ([]HashMatch, error) {
	if len(matches) == 0 {
		return matches, nil
	}
	ids := make(map[uuid.UUID]struct{}, len(matches))
	for _, m := range matches {
		ids[m.SourceID] = struct{}{}
	}
	rows, err := tx.Query(ctx, `
		SELECT id FROM sources
		WHERE id = ANY($1)
		ORDER BY id
		FOR UPDATE`, sortedUUIDs(ids))
	if err != nil {
		return nil, err
	}
	alive := make(map[uuid.UUID]struct{}, len(matches))
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		alive[id] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]HashMatch, 0, len(matches))
	for _, m := range matches {
		if _, ok := alive[m.SourceID]; ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func replaceLSH(ctx context.Context, tx pgx.Tx, id uuid.UUID, fp fingerprint.Result) error {
	if _, err := tx.Exec(ctx, `DELETE FROM fingerprint_lsh WHERE source_id=$1`, id); err != nil {
		return err
	}
	add := func(kind string, h uint64) error {
		if h == 0 {
			return nil
		}
		bands := fingerprint.Bands(h)
		for i, b := range bands {
			if _, err := tx.Exec(ctx, `
				INSERT INTO fingerprint_lsh (source_id, kind, band, bucket)
				VALUES ($1,$2,$3,$4)
				ON CONFLICT DO NOTHING`, id, kind, i, int(b)); err != nil {
				return err
			}
		}
		return nil
	}
	if fp.HasText {
		return add("simhash", fp.SimHash)
	}
	if fp.HasVisual {
		return add("phash", fp.PHash)
	}
	return nil
}

func insertDup(ctx context.Context, tx pgx.Tx, from, to uuid.UUID, title, erp string, score float64, uploaded time.Time, kind string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO duplicate_matches (id, source_id, matched_source_id, title, erp, score, uploaded_at, kind)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (source_id, matched_source_id) DO UPDATE
		SET score = GREATEST(duplicate_matches.score, EXCLUDED.score),
		    kind = CASE
		      WHEN EXCLUDED.score >= duplicate_matches.score THEN EXCLUDED.kind
		      ELSE duplicate_matches.kind
		    END`,
		uuid.New(), from, to, title, erp, score, uploaded, kind)
	return err
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullI64(v uint64) any {
	if v == 0 {
		return nil
	}
	return int64(v)
}

func u64(v uint64) string {
	return strconv.FormatUint(v, 10)
}

func uploadedFirst(aID uuid.UUID, aTime time.Time, bID uuid.UUID, bTime time.Time) bool {
	if aTime.Equal(bTime) {
		return aID.String() < bID.String()
	}
	return aTime.Before(bTime)
}

func confidentMatches(fp fingerprint.Result, matches []HashMatch) []HashMatch {
	if len(matches) == 0 {
		return matches
	}
	out := make([]HashMatch, 0, len(matches))
	for _, m := range matches {
		if fingerprint.IsDuplicate(m.Kind, m.Score, fp.PageCount, m.PageCount) {
			out = append(out, m)
		}
	}
	return out
}

func (r *Repo) PreviewFingerprint(ctx context.Context, fp fingerprint.Result) ([]HashMatch, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	matches := make([]HashMatch, 0, 8)
	if fp.SHA256 != "" {
		exact, err := queryExact(ctx, db, uuid.Nil, fp.SHA256)
		if err != nil {
			return nil, err
		}
		matches = mergeMatches(matches, exact)
	}
	if fp.HasText && fp.TextNormSHA != "" {
		textExact, err := queryTextExact(ctx, db, uuid.Nil, fp.TextNormSHA)
		if err != nil {
			return nil, err
		}
		matches = mergeMatches(matches, textExact)
		near, err := queryLSH(ctx, db, uuid.Nil, "simhash", fp.SimHash, fingerprint.SimHashMaxDist)
		if err != nil {
			return nil, err
		}
		matches = mergeMatches(matches, near)
	} else if fp.HasVisual && fp.PHash != 0 {
		vis, err := queryLSH(ctx, db, uuid.Nil, "phash", fp.PHash, fingerprint.PHashMaxDist)
		if err != nil {
			return nil, err
		}
		matches = mergeMatches(matches, vis)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		oi := matches[i].Uniqueness == Original
		oj := matches[j].Uniqueness == Original
		if oi != oj {
			return oi
		}
		if matches[i].Score != matches[j].Score {
			return matches[i].Score > matches[j].Score
		}
		return matches[i].Uploaded.Before(matches[j].Uploaded)
	})
	return confidentMatches(fp, matches), nil
}
