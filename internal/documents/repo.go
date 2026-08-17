package documents

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"ocr-backend/internal/retry"
)

var (
	ErrUnavailable = retry.ErrUnavailable
	ErrNotFound    = errors.New("not found")
	ErrERPTaken    = errors.New("erp taken")
	ErrNoFiles     = errors.New("no files")
	ErrTooMany     = errors.New("too many sources")
	ErrInvalid     = errors.New("invalid")
	ErrForbidden   = errors.New("forbidden")
)

type ListFilter struct {
	OwnerID uuid.UUID
	Admin   bool
	Pending bool
}

type Repo struct {
	pool func() *pgxpool.Pool
}

func NewRepo(pool func() *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) db() (*pgxpool.Pool, error) {
	p := r.pool()
	if p == nil {
		return nil, ErrUnavailable
	}
	return p, nil
}

func (r *Repo) List(ctx context.Context, filter ListFilter) ([]Document, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	query := `
		SELECT id, client, erp, anzsco, team, member, status, created_at, owner_id,
		       review_note, review_requested_at
		FROM documents`
	args := []any{}
	switch {
	case filter.Admin && filter.Pending:
		query += ` WHERE status = 'pending_review'`
	case filter.Admin:
	default:
		query += ` WHERE owner_id = $1`
		args = append(args, filter.OwnerID)
	}
	query += ` ORDER BY created_at DESC`
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	docs := make([]Document, 0, 64)
	ids := make([]uuid.UUID, 0, 64)
	byID := make(map[uuid.UUID]*Document, 64)
	for rows.Next() {
		var d Document
		if err := scanDocument(rows, &d); err != nil {
			return nil, err
		}
		d.Sources = []Source{}
		docs = append(docs, d)
		ids = append(ids, d.ID)
		byID[d.ID] = &docs[len(docs)-1]
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return docs, nil
	}
	if err := r.attachSources(ctx, db, ids, byID); err != nil {
		return nil, err
	}
	for i := range docs {
		decorate(&docs[i])
	}
	return docs, nil
}

func (r *Repo) Get(ctx context.Context, id uuid.UUID) (Document, error) {
	db, err := r.db()
	if err != nil {
		return Document{}, err
	}
	var d Document
	err = scanDocument(db.QueryRow(ctx, `
		SELECT id, client, erp, anzsco, team, member, status, created_at, owner_id,
		       review_note, review_requested_at
		FROM documents WHERE id=$1`, id), &d)
	if errors.Is(err, pgx.ErrNoRows) {
		return Document{}, ErrNotFound
	}
	if err != nil {
		return Document{}, err
	}
	d.Sources = []Source{}
	byID := map[uuid.UUID]*Document{d.ID: &d}
	if err := r.attachSources(ctx, db, []uuid.UUID{d.ID}, byID); err != nil {
		return Document{}, err
	}
	decorate(&d)
	return d, nil
}

func (r *Repo) attachSources(ctx context.Context, db *pgxpool.Pool, ids []uuid.UUID, byID map[uuid.UUID]*Document) error {
	rows, err := db.Query(ctx, `
		SELECT id, document_id, title, storage_key, content_type, size_bytes,
		       content_sha256, uniqueness, score, created_at
		FROM sources
		WHERE document_id = ANY($1)
		ORDER BY created_at ASC`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()

	type rowSrc struct {
		docID uuid.UUID
		src   Source
	}
	collected := make([]rowSrc, 0, 16)
	for rows.Next() {
		var s Source
		if err := rows.Scan(&s.ID, &s.DocumentID, &s.Title, &s.StorageKey, &s.ContentType, &s.SizeBytes, &s.SHA256, &s.Uniqueness, &s.Score, &s.Uploaded); err != nil {
			return err
		}
		s.Duplicates = []DuplicateMatch{}
		collected = append(collected, rowSrc{docID: s.DocumentID, src: s})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range collected {
		doc := byID[row.docID]
		if doc == nil {
			continue
		}
		doc.Sources = append(doc.Sources, row.src)
	}

	sourceByID := make(map[uuid.UUID]*Source)
	sourceIDs := make([]uuid.UUID, 0)
	for _, doc := range byID {
		for i := range doc.Sources {
			src := &doc.Sources[i]
			sourceByID[src.ID] = src
			sourceIDs = append(sourceIDs, src.ID)
		}
	}
	if len(sourceIDs) == 0 {
		return nil
	}
	mrows, err := db.Query(ctx, `
		SELECT dm.id, dm.source_id, dm.matched_source_id, dm.title, dm.erp, dm.score, dm.uploaded_at, dm.kind,
		       COALESCE(ms.uniqueness, 'unique'), COALESCE(md.client, ''), COALESCE(md.member, ''),
		       md.id, COALESCE(ms.content_type, '')
		FROM duplicate_matches dm
		JOIN sources ms ON ms.id = dm.matched_source_id
		JOIN documents md ON md.id = ms.document_id
		WHERE dm.source_id = ANY($1)
		ORDER BY CASE ms.uniqueness WHEN 'original' THEN 0 ELSE 1 END, dm.score DESC, ms.created_at ASC`, sourceIDs)
	if err != nil {
		return err
	}
	defer mrows.Close()
	for mrows.Next() {
		var m DuplicateMatch
		var sourceID uuid.UUID
		var matchedID uuid.UUID
		var matchedDoc uuid.UUID
		if err := mrows.Scan(&m.ID, &sourceID, &matchedID, &m.Title, &m.ERP, &m.Score, &m.Uploaded, &m.Kind, &m.Uniqueness, &m.Client, &m.Member, &matchedDoc, &m.ContentType); err != nil {
			return err
		}
		m.SourceID = matchedID
		m.DocumentID = matchedDoc
		m.FileURL = fileURL(matchedDoc, matchedID)
		if src := sourceByID[sourceID]; src != nil {
			src.Duplicates = append(src.Duplicates, m)
		}
	}
	return mrows.Err()
}

func (r *Repo) InsertDocument(ctx context.Context, d Document) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO documents (id, client, erp, anzsco, team, member, status, created_at, updated_at, owner_id, review_note)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$8,$9,$10)`,
			d.ID, d.Client, d.ERP, d.ANZSCO, d.Team, d.Member, d.Status, d.Uploaded, d.OwnerID, d.ReviewNote)
		if isUniqueViolation(err) {
			return ErrERPTaken
		}
		if err != nil {
			return err
		}
		for _, s := range d.Sources {
			if _, err := tx.Exec(ctx, `
				INSERT INTO sources (id, document_id, title, storage_key, content_type, size_bytes, uniqueness, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				s.ID, d.ID, s.Title, s.StorageKey, s.ContentType, s.SizeBytes, s.Uniqueness, s.Uploaded); err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *Repo) InsertSources(ctx context.Context, documentID uuid.UUID, sources []Source, maxSources int, note string) error {
	if maxSources < 1 {
		maxSources = 4
	}
	note = ClampNote(note)
	return r.withTx(ctx, func(tx pgx.Tx) error {
		var id uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM documents WHERE id=$1 FOR UPDATE`, documentID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM sources WHERE document_id=$1`, documentID).Scan(&n); err != nil {
			return err
		}
		if n+len(sources) > maxSources {
			return ErrTooMany
		}
		for _, s := range sources {
			if _, err := tx.Exec(ctx, `
				INSERT INTO sources (id, document_id, title, storage_key, content_type, size_bytes, uniqueness, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				s.ID, documentID, s.Title, s.StorageKey, s.ContentType, s.SizeBytes, s.Uniqueness, s.Uploaded); err != nil {
				return err
			}
		}
		if note != "" {
			if _, err := tx.Exec(ctx, `
				UPDATE documents
				SET status='processing', notified_at=NULL, review_note=$2, updated_at=now()
				WHERE id=$1`, documentID, note); err != nil {
				return err
			}
			return nil
		}
		_, err = tx.Exec(ctx, `UPDATE documents SET status='processing', notified_at=NULL, updated_at=now() WHERE id=$1`, documentID)
		return err
	})
}

func (r *Repo) Delete(ctx context.Context, id uuid.UUID) ([]string, error) {
	var keys []string
	err := r.withTx(ctx, func(tx pgx.Tx) error {
		var exists uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM documents WHERE id=$1 FOR UPDATE`, id).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT storage_key FROM sources WHERE document_id=$1`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		keys = keys[:0]
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `DELETE FROM documents WHERE id=$1`, id)
		return err
	})
	return keys, err
}

func (r *Repo) SourceMeta(ctx context.Context, docID, sourceID uuid.UUID) (Source, error) {
	db, err := r.db()
	if err != nil {
		return Source{}, err
	}
	var s Source
	err = db.QueryRow(ctx, `
		SELECT id, document_id, title, storage_key, content_type, size_bytes,
		       content_sha256, uniqueness, score, created_at
		FROM sources WHERE id=$1 AND document_id=$2`, sourceID, docID).
		Scan(&s.ID, &s.DocumentID, &s.Title, &s.StorageKey, &s.ContentType, &s.SizeBytes, &s.SHA256, &s.Uniqueness, &s.Score, &s.Uploaded)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	return s, err
}

func (r *Repo) CountSources(ctx context.Context, documentID uuid.UUID) (int, error) {
	db, err := r.db()
	if err != nil {
		return 0, err
	}
	var n int
	err = db.QueryRow(ctx, `SELECT COUNT(*) FROM sources WHERE document_id=$1`, documentID).Scan(&n)
	return n, err
}

func (r *Repo) ExistsERP(ctx context.Context, erp string) (bool, error) {
	db, err := r.db()
	if err != nil {
		return false, err
	}
	var exists bool
	err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM documents WHERE lower(erp)=lower($1))`, erp).Scan(&exists)
	return exists, err
}

func (r *Repo) ListERPs(ctx context.Context) ([]string, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `SELECT erp FROM documents`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 64)
	for rows.Next() {
		var erp string
		if err := rows.Scan(&erp); err != nil {
			return nil, err
		}
		out = append(out, erp)
	}
	return out, rows.Err()
}

func (r *Repo) withTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return retry.Do(ctx, 5, func(ctx context.Context) error {
		db, err := r.db()
		if err != nil {
			return err
		}
		tx, err := db.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := fn(tx); err != nil {
			return err
		}
		return tx.Commit(ctx)
	})
}

type FinalizeResult struct {
	Uniqueness Uniqueness
	Touched    []uuid.UUID
	Notified   []uuid.UUID
}

func (r *Repo) SetSourceTitle(ctx context.Context, id uuid.UUID, title string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE sources SET title=$2 WHERE id=$1`, id, title)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (r *Repo) Approve(ctx context.Context, id uuid.UUID) error {
	return r.withTx(ctx, func(tx pgx.Tx) error {
		var exists uuid.UUID
		err := tx.QueryRow(ctx, `SELECT id FROM documents WHERE id=$1 FOR UPDATE`, id).Scan(&exists)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE sources SET released=TRUE WHERE document_id=$1`, id); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
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
				notified_at = NULL,
				updated_at = now()
			WHERE d.id = $1`, id)
		return err
	})
}

func (r *Repo) RejectPending(ctx context.Context, id uuid.UUID) (keys []string, deleted bool, err error) {
	err = r.withTx(ctx, func(tx pgx.Tx) error {
		var exists uuid.UUID
		scanErr := tx.QueryRow(ctx, `SELECT id FROM documents WHERE id=$1 FOR UPDATE`, id).Scan(&exists)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if scanErr != nil {
			return scanErr
		}
		rows, qerr := tx.Query(ctx, `SELECT storage_key FROM sources WHERE document_id=$1 AND released=FALSE`, id)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		keys = keys[:0]
		for rows.Next() {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			keys = append(keys, key)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM sources WHERE document_id=$1 AND released=FALSE`, id); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM sources WHERE document_id=$1`, id).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM documents WHERE id=$1`, id); err != nil {
				return err
			}
			deleted = true
			return nil
		}
		_, err := tx.Exec(ctx, `
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
			WHERE d.id = $1`, id)
		return err
	})
	return keys, deleted, err
}

func (r *Repo) ListUnhashed(ctx context.Context, limit int) ([]Source, error) {
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
		WHERE content_sha256 IS NULL
		  AND created_at < now() - interval '15 seconds'
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

func sortedUUIDs(set map[uuid.UUID]struct{}) []uuid.UUID {
	ids := make([]uuid.UUID, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	return ids
}

func decorate(d *Document) {
	d.Uploader = d.Member
	d.URL = fmt.Sprintf("/backend/v1/documents/%s", d.ID)
	if len(d.Sources) > 0 {
		d.Title = d.Sources[0].Title
		d.FileURL = fileURL(d.ID, d.Sources[0].ID)
	} else {
		d.Title = d.ERP
		d.FileURL = d.URL
	}
	for i := range d.Sources {
		d.Sources[i].FileURL = fileURL(d.ID, d.Sources[i].ID)
	}
}

type documentScanner interface {
	Scan(dest ...any) error
}

func scanDocument(row documentScanner, d *Document) error {
	var owner uuid.NullUUID
	if err := row.Scan(
		&d.ID, &d.Client, &d.ERP, &d.ANZSCO, &d.Team, &d.Member, &d.Status, &d.Uploaded, &owner,
		&d.ReviewNote, &d.ReviewRequestedAt,
	); err != nil {
		return err
	}
	d.Member = strings.TrimSpace(d.Member)
	d.Uploader = d.Member
	d.OwnerID = nullUUID(owner)
	d.ReviewNote = strings.TrimSpace(d.ReviewNote)
	return nil
}

func fileURL(docID, sourceID uuid.UUID) string {
	return fmt.Sprintf("/backend/v1/documents/%s/sources/%s/file", docID, sourceID)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func nullUUID(v uuid.NullUUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := v.UUID
	return &id
}

func Owns(owner *uuid.UUID, userID uuid.UUID) bool {
	return owner != nil && *owner == userID
}
