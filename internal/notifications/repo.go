package notifications

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"ocr-backend/internal/documents"
	"ocr-backend/internal/realtime"
)

type Item struct {
	ID      uuid.UUID `json:"id"`
	Title   string    `json:"title"`
	Detail  string    `json:"detail"`
	Read    bool      `json:"read"`
	Created time.Time `json:"created_at"`
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
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT id, title, detail, "read", created_at
		FROM notifications
		ORDER BY created_at DESC
		LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Item, 0, 16)
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Title, &item.Detail, &item.Read, &item.Created); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repo) Create(ctx context.Context, title, detail string) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	item := Item{
		ID:      uuid.New(),
		Title:   title,
		Detail:  detail,
		Created: time.Now().UTC(),
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO notifications (id, title, detail, "read", created_at)
		VALUES ($1,$2,$3,false,$4)`, item.ID, item.Title, item.Detail, item.Created); err != nil {
		return err
	}
	if r.hub != nil {
		r.hub.Publish(ctx, "notification.created", item)
	}
	return nil
}

func (r *Repo) MarkRead(ctx context.Context, id uuid.UUID) (Item, error) {
	db, err := r.db()
	if err != nil {
		return Item{}, err
	}
	var item Item
	err = db.QueryRow(ctx, `
		UPDATE notifications SET "read"=true
		WHERE id=$1
		RETURNING id, title, detail, "read", created_at`, id).
		Scan(&item.ID, &item.Title, &item.Detail, &item.Read, &item.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, documents.ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	if r.hub != nil {
		r.hub.Publish(ctx, "notification.updated", item)
	}
	return item, nil
}

func (r *Repo) MarkAllRead(ctx context.Context) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	if _, err := db.Exec(ctx, `UPDATE notifications SET "read"=true WHERE "read"=false`); err != nil {
		return err
	}
	if r.hub != nil {
		r.hub.Publish(ctx, "notification.cleared", json.RawMessage(`{}`))
	}
	return nil
}
