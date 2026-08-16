package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repo struct {
	pool func() *pgxpool.Pool
}

func NewRepo(pool func() *pgxpool.Pool) *Repo {
	return &Repo{pool: pool}
}

func (r *Repo) db() (*pgxpool.Pool, error) {
	p := r.pool()
	if p == nil {
		return nil, errors.New("postgres unavailable")
	}
	return p, nil
}

func (r *Repo) CreateUser(ctx context.Context, email, name, password, role string) (User, error) {
	email = NormalizeEmail(email)
	name = strings.TrimSpace(name)
	role = strings.TrimSpace(role)
	if email == "" || name == "" || !ValidRole(role) || !ValidPassword(password) {
		return User{}, ErrInvalid
	}
	hash, err := HashPassword(password)
	if err != nil {
		return User{}, err
	}
	db, err := r.db()
	if err != nil {
		return User{}, err
	}
	user := User{
		ID:      uuid.New(),
		Email:   email,
		Name:    name,
		Role:    role,
		Created: time.Now().UTC(),
	}
	_, err = db.Exec(ctx, `
		INSERT INTO users (id, email, name, password_hash, role, disabled, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,false,$6,$6)`,
		user.ID, user.Email, user.Name, hash, user.Role, user.Created)
	if isUnique(err) {
		return User{}, ErrEmailTaken
	}
	return user, err
}

func (r *Repo) EnsureAdmin(ctx context.Context, email, name, password string) error {
	email = NormalizeEmail(email)
	name = strings.TrimSpace(name)
	if email == "" || !ValidPassword(password) {
		return nil
	}
	if name == "" {
		name = "Admin"
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	var exists bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=lower($1))`, email).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = r.CreateUser(ctx, email, name, password, RoleAdmin)
	if errors.Is(err, ErrEmailTaken) {
		return nil
	}
	return err
}

func (r *Repo) Authenticate(ctx context.Context, email, password string) (User, error) {
	email = NormalizeEmail(email)
	db, err := r.db()
	if err != nil {
		return User{}, err
	}
	var user User
	var hash string
	err = db.QueryRow(ctx, `
		SELECT id, email, name, password_hash, role, disabled, created_at
		FROM users WHERE lower(email)=lower($1)`, email).
		Scan(&user.ID, &user.Email, &user.Name, &hash, &user.Role, &user.Disabled, &user.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		_ = CheckPassword("", password)
		return User{}, ErrInvalidCreds
	}
	if err != nil {
		return User{}, err
	}
	if !CheckPassword(hash, password) {
		return User{}, ErrInvalidCreds
	}
	if user.Disabled {
		return User{}, ErrDisabled
	}
	return user, nil
}

func (r *Repo) CreateSession(ctx context.Context, userID uuid.UUID) (string, time.Time, error) {
	token, err := NewToken()
	if err != nil {
		return "", time.Time{}, err
	}
	db, err := r.db()
	if err != nil {
		return "", time.Time{}, err
	}
	expires := time.Now().UTC().Add(SessionTTL)
	_, err = db.Exec(ctx, `
		INSERT INTO sessions (id, user_id, token_hash, expires_at, created_at)
		VALUES ($1,$2,$3,$4,$4)`,
		uuid.New(), userID, HashToken(token), expires)
	if err != nil {
		return "", time.Time{}, err
	}
	return token, expires, nil
}

func (r *Repo) UserByToken(ctx context.Context, token string) (User, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return User{}, ErrUnauthorized
	}
	db, err := r.db()
	if err != nil {
		return User{}, err
	}
	var user User
	err = db.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, u.role, u.disabled, u.created_at
		FROM sessions s
		JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, HashToken(token)).
		Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.Disabled, &user.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	if err != nil {
		return User{}, err
	}
	if user.Disabled {
		return User{}, ErrDisabled
	}
	return user, nil
}

func (r *Repo) DeleteSession(ctx context.Context, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	db, err := r.db()
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `DELETE FROM sessions WHERE token_hash=$1`, HashToken(token))
	return err
}

func (r *Repo) DeleteUserSessions(ctx context.Context, userID uuid.UUID) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	_, err = db.Exec(ctx, `DELETE FROM sessions WHERE user_id=$1`, userID)
	return err
}

func (r *Repo) SweepExpired(ctx context.Context) {
	db, err := r.db()
	if err != nil {
		return
	}
	_, _ = db.Exec(ctx, `DELETE FROM sessions WHERE expires_at < now()`)
}

func (r *Repo) Get(ctx context.Context, id uuid.UUID) (User, error) {
	db, err := r.db()
	if err != nil {
		return User{}, err
	}
	var user User
	err = db.QueryRow(ctx, `
		SELECT id, email, name, role, disabled, created_at
		FROM users WHERE id=$1`, id).
		Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.Disabled, &user.Created)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrUnauthorized
	}
	return user, err
}

func (r *Repo) List(ctx context.Context) ([]User, error) {
	db, err := r.db()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(ctx, `
		SELECT id, email, name, role, disabled, created_at
		FROM users
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]User, 0, 16)
	for rows.Next() {
		var user User
		if err := rows.Scan(&user.ID, &user.Email, &user.Name, &user.Role, &user.Disabled, &user.Created); err != nil {
			return nil, err
		}
		out = append(out, user)
	}
	return out, rows.Err()
}

func (r *Repo) SetDisabled(ctx context.Context, id uuid.UUID, disabled bool) error {
	db, err := r.db()
	if err != nil {
		return err
	}
	tag, err := db.Exec(ctx, `UPDATE users SET disabled=$2, updated_at=now() WHERE id=$1`, id, disabled)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrUnauthorized
	}
	if disabled {
		_ = r.DeleteUserSessions(ctx, id)
	}
	return nil
}

func isUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
