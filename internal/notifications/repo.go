package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"hash/fnv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ocr-backend/internal/auth"
	"ocr-backend/internal/documents"
	"ocr-backend/internal/realtime"
)

type Item struct {
	ID         uuid.UUID  `json:"id"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	Read       bool       `json:"read"`
	Created    time.Time  `json:"created_at"`
	UserID     *uuid.UUID `json:"user_id,omitempty"`
	Audience   string     `json:"audience,omitempty"`
	Kind       string     `json:"kind,omitempty"`
	DocumentID *uuid.UUID `json:"document_id,omitempty"`
}

type Repo struct {
	pool func() *pgxpool.Pool
	hub  *realtime.Hub
}

func NewRepo(pool func() *pgxpool.Pool, hub *realtime.Hub) *Repo {
	return &Repo{pool: pool, hub: hub}
}

func (r *Repo) db() (*pgxpool.Pool, error) {
	p := r.pool()
	if p == nil {
		return nil, documents.ErrUnavailable
	}
	return p, nil
}

func (r *Repo) List(ctx context.Context) ([]Item, error) {
	user, err := auth.Must(ctx)
	if err != nil {
		return nil, err
	}
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	query := `
		SELECT id, title, detail, "read", created_at, user_id, audience, kind, document_id
		FROM notifications
		WHERE user_id = $1`
	args := []any{user.ID}
	if user.Admin() {
		query = `
			SELECT id, title, detail, "read", created_at, user_id, audience, kind, document_id
			FROM notifications
			WHERE audience = 'admin' OR user_id = $1`
	}
	query += ` ORDER BY created_at DESC LIMIT 100`
	rows, err := db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Item, 0, 16)
	for rows.Next() {
		var item Item
		var owner uuid.NullUUID
		var docID uuid.NullUUID
		if err := rows.Scan(&item.ID, &item.Title, &item.Detail, &item.Read, &item.Created, &owner, &item.Audience, &item.Kind, &docID); err != nil {
			return nil, err
		}
		item.UserID = asUUID(owner)
		item.DocumentID = asUUID(docID)
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repo) Create(ctx context.Context, title, detail string) error {
	return r.NotifyAdmins(ctx, title, detail, "", nil)
}

func (r *Repo) NotifyAdmins(ctx context.Context, title, detail, kind string, docID *uuid.UUID) error {
	return r.insert(ctx, nil, "admin", title, detail, kind, docID)
}

func (r *Repo) NotifyAdminsOnce(ctx context.Context, title, detail, kind string, docID uuid.UUID) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, kindLock(docID, kind)); err != nil {
		return err
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM notifications
			WHERE document_id = $1 AND kind = $2 AND "read" = false
		)`, docID, kind).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	item := Item{
		ID:         uuid.New(),
		Title:      title,
		Detail:     detail,
		Created:    time.Now().UTC(),
		Audience:   "admin",
		Kind:       kind,
		DocumentID: &docID,
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO notifications (id, title, detail, "read", created_at, user_id, audience, kind, document_id)
		VALUES ($1,$2,$3,false,$4,NULL,$5,$6,$7)`, item.ID, item.Title, item.Detail, item.Created, item.Audience, item.Kind, docID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if r.hub != nil {
		r.hub.Publish(ctx, "notification.created", item)
	}
	return nil
}

func (r *Repo) NotifyUser(ctx context.Context, userID uuid.UUID, title, detail, kind string, docID *uuid.UUID) error {
	return r.insert(ctx, &userID, "user", title, detail, kind, docID)
}

func (r *Repo) HasUnreadKind(ctx context.Context, docID uuid.UUID, kind string) bool {
	db, err := r.db()
	if err != nil {
		return false
	}
	var exists bool
	if err := db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM notifications
			WHERE document_id = $1 AND kind = $2 AND "read" = false
		)`, docID, kind).Scan(&exists); err != nil {
		return false
	}
	return exists
}

func (r *Repo) insert(ctx context.Context, userID *uuid.UUID, audience, title, detail, kind string, docID *uuid.UUID) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	item := Item{
		ID:         uuid.New(),
		Title:      title,
		Detail:     detail,
		Created:    time.Now().UTC(),
		UserID:     userID,
		Audience:   audience,
		Kind:       kind,
		DocumentID: docID,
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO notifications (id, title, detail, "read", created_at, user_id, audience, kind, document_id)
		VALUES ($1,$2,$3,false,$4,$5,$6,$7,$8)`, item.ID, item.Title, item.Detail, item.Created, userID, audience, kind, docID); err != nil {
		return err
	}
	if r.hub != nil {
		r.hub.Publish(ctx, "notification.created", item)
	}
	return nil
}

func (r *Repo) MarkRead(ctx context.Context, id uuid.UUID) (Item, error) {
	user, err := auth.Must(ctx)
	if err != nil {
		return Item{}, err
	}
	db, err := r.db()
	if err != nil {
		return Item{}, err
	}
	var item Item
	var owner uuid.NullUUID
	var docID uuid.NullUUID
	err = db.QueryRow(ctx, `
		UPDATE notifications SET "read"=true
		WHERE id=$1 AND (user_id=$2 OR ($3 AND audience='admin'))
		RETURNING id, title, detail, "read", created_at, user_id, audience, kind, document_id`, id, user.ID, user.Admin()).
		Scan(&item.ID, &item.Title, &item.Detail, &item.Read, &item.Created, &owner, &item.Audience, &item.Kind, &docID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, documents.ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	item.UserID = asUUID(owner)
	item.DocumentID = asUUID(docID)
	if r.hub != nil {
		r.hub.Publish(ctx, "notification.updated", item)
	}
	return item, nil
}

func (r *Repo) MarkAllRead(ctx context.Context) error {
	user, err := auth.Must(ctx)
	if err != nil {
		return err
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	if user.Admin() {
		_, err = db.Exec(ctx, `UPDATE notifications SET "read"=true WHERE "read"=false AND (audience='admin' OR user_id=$1)`, user.ID)
	} else {
		_, err = db.Exec(ctx, `UPDATE notifications SET "read"=true WHERE "read"=false AND user_id=$1`, user.ID)
	}
	if err != nil {
		return err
	}
	if r.hub != nil {
		r.hub.Publish(ctx, "notification.cleared", json.RawMessage(`{}`))
	}
	return nil
}

func asUUID(v uuid.NullUUID) *uuid.UUID {
	if !v.Valid {
		return nil
	}
	id := v.UUID
	return &id
}

func kindLock(docID uuid.UUID, kind string) int64 {
	h := fnv.New64a()
	_, _ = h.Write(docID[:])
	_, _ = h.Write([]byte(kind))
	return int64(h.Sum64())
}
