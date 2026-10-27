package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ACaccel/lcm/internal/store"
)

// Errors returned by Service.
var (
	ErrInvalidCredentials = errors.New("auth: invalid credentials")
	ErrUnauthenticated    = errors.New("auth: unauthenticated")
	ErrNotFound           = errors.New("auth: not found")
	ErrWeakPassword       = fmt.Errorf("auth: password must be at least %d characters", MinPasswordLength)
	ErrInvalidName        = errors.New("auth: name must be 1-64 characters")
	ErrInvalidUsername    = errors.New("auth: username must be 1-64 characters")
)

const (
	maxNameLen = 64

	// tokenPrefix makes API tokens recognizable in logs and secret scanners.
	tokenPrefix = "lcm_"

	// lastUsedGranularity bounds how often a busy token causes a write.
	lastUsedGranularity = time.Minute
)

// Option customizes a Service.
type Option func(*Service)

// WithClock replaces the time source, for tests.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// Service manages the user account, sessions and API tokens.
type Service struct {
	db         *store.DB
	now        func() time.Time
	sessionTTL time.Duration
	// dummyHash is verified when the username is unknown so that the response
	// time does not reveal which usernames exist.
	dummyHash string
}

// NewService returns a Service whose sessions live for sessionTTL.
func NewService(db *store.DB, sessionTTL time.Duration, opts ...Option) (*Service, error) {
	dummy, err := HashPassword("unused-placeholder-password")
	if err != nil {
		return nil, err
	}
	s := &Service{db: db, now: time.Now, sessionTTL: sessionTTL, dummyHash: dummy}
	for _, o := range opts {
		o(s)
	}
	return s, nil
}

// User is a login account.
type User struct {
	ID       int64
	Username string
}

func (u User) actor() Actor {
	return Actor{Kind: KindUser, ID: strconv.FormatInt(u.ID, 10), Name: u.Username}
}

// Actor returns the actor representing the user.
func (u User) Actor() Actor { return u.actor() }

// SetPassword creates the user or replaces its password, and ends all of the
// user's sessions. It reports whether the user was created.
func (s *Service) SetPassword(ctx context.Context, username, password string) (created bool, err error) {
	if l := len(username); l == 0 || l > maxNameLen {
		return false, ErrInvalidUsername
	}
	if len(password) < MinPasswordLength {
		return false, ErrWeakPassword
	}
	hash, err := HashPassword(password)
	if err != nil {
		return false, err
	}
	now := s.now().UnixMilli()
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		var id int64
		switch err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE username = ?`, username).Scan(&id); {
		case errors.Is(err, sql.ErrNoRows):
			created = true
			_, err = tx.ExecContext(ctx,
				`INSERT INTO users (username, password_hash, created_at, updated_at) VALUES (?, ?, ?, ?)`,
				username, hash, now, now)
			return err
		case err != nil:
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?`, hash, now, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, id)
		return err
	})
	if err != nil {
		return false, fmt.Errorf("auth: set password: %w", err)
	}
	return created, nil
}

// Login verifies the credentials and opens a session. It returns the opaque
// session token (shown once) and its expiry.
func (s *Service) Login(ctx context.Context, username, password string) (token string, expires time.Time, user User, err error) {
	var hash string
	user.Username = username
	qerr := s.db.Reader().QueryRowContext(ctx,
		`SELECT id, username, password_hash FROM users WHERE username = ?`, username).
		Scan(&user.ID, &user.Username, &hash)
	known := qerr == nil
	if qerr != nil && !errors.Is(qerr, sql.ErrNoRows) {
		return "", time.Time{}, User{}, fmt.Errorf("auth: look up user: %w", qerr)
	}
	if !known {
		hash = s.dummyHash
	}
	ok, verr := VerifyPassword(password, hash)
	if verr != nil {
		return "", time.Time{}, User{}, fmt.Errorf("auth: verify password: %w", verr)
	}
	if !known || !ok {
		return "", time.Time{}, User{}, ErrInvalidCredentials
	}

	token, err = randomToken()
	if err != nil {
		return "", time.Time{}, User{}, err
	}
	now := s.now()
	expires = now.Add(s.sessionTTL)
	err = s.db.Write(ctx, func(tx *sql.Tx) error {
		// Opportunistic cleanup keeps the table small without a janitor.
		if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, now.UnixMilli()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`INSERT INTO sessions (token_hash, user_id, created_at, expires_at) VALUES (?, ?, ?, ?)`,
			hashToken(token), user.ID, now.UnixMilli(), expires.UnixMilli())
		return err
	})
	if err != nil {
		return "", time.Time{}, User{}, fmt.Errorf("auth: create session: %w", err)
	}
	return token, expires, user, nil
}

// Session resolves a session token to its user.
func (s *Service) Session(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrUnauthenticated
	}
	var (
		u       User
		expires int64
	)
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT u.id, u.username, s.expires_at
		   FROM sessions s JOIN users u ON u.id = s.user_id
		  WHERE s.token_hash = ?`, hashToken(token)).Scan(&u.ID, &u.Username, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrUnauthenticated
	}
	if err != nil {
		return User{}, fmt.Errorf("auth: look up session: %w", err)
	}
	if !s.now().Before(time.UnixMilli(expires)) {
		return User{}, ErrUnauthenticated
	}
	return u, nil
}

// Logout deletes the session. Unknown tokens are ignored.
func (s *Service) Logout(ctx context.Context, token string) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
		return err
	})
	if err != nil {
		return fmt.Errorf("auth: logout: %w", err)
	}
	return nil
}

// Token describes an API token without its secret.
type Token struct {
	ID         int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
	RevokedAt  *time.Time
}

// CreateToken issues a new API token. The secret is returned only here.
func (s *Service) CreateToken(ctx context.Context, name string) (Token, string, error) {
	name = strings.TrimSpace(name)
	if l := len(name); l == 0 || l > maxNameLen {
		return Token{}, "", ErrInvalidName
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return Token{}, "", fmt.Errorf("auth: generate token: %w", err)
	}
	secret := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	now := s.now()
	t := Token{Name: name, CreatedAt: now.UTC().Truncate(time.Millisecond)}
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO api_tokens (name, token_hash, created_at) VALUES (?, ?, ?)`,
			name, hashToken(secret), now.UnixMilli())
		if err != nil {
			return err
		}
		t.ID, err = res.LastInsertId()
		return err
	})
	if err != nil {
		return Token{}, "", fmt.Errorf("auth: create token: %w", err)
	}
	return t, secret, nil
}

// ListTokens returns all tokens, newest first.
func (s *Service) ListTokens(ctx context.Context) ([]Token, error) {
	rows, err := s.db.Reader().QueryContext(ctx,
		`SELECT id, name, created_at, last_used_at, revoked_at FROM api_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, fmt.Errorf("auth: list tokens: %w", err)
	}
	defer func() { _ = rows.Close() }()
	tokens := []Token{}
	for rows.Next() {
		var (
			t                 Token
			created           int64
			lastUsed, revoked sql.NullInt64
		)
		if err := rows.Scan(&t.ID, &t.Name, &created, &lastUsed, &revoked); err != nil {
			return nil, fmt.Errorf("auth: list tokens: %w", err)
		}
		t.CreatedAt = time.UnixMilli(created).UTC()
		t.LastUsedAt = optTime(lastUsed)
		t.RevokedAt = optTime(revoked)
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list tokens: %w", err)
	}
	return tokens, nil
}

// RevokeToken revokes a token. Revoking an already revoked token succeeds
// and keeps the original revocation time.
func (s *Service) RevokeToken(ctx context.Context, id int64) error {
	err := s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE api_tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
			s.now().UnixMilli(), id)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil {
			return err
		} else if n > 0 {
			return nil
		}
		var one int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM api_tokens WHERE id = ?`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("auth: revoke token: %w", err)
	}
	return nil
}

// VerifyToken authenticates an API token and returns its actor. The last-used
// timestamp is refreshed at most once per minute.
func (s *Service) VerifyToken(ctx context.Context, secret string) (Actor, error) {
	if !strings.HasPrefix(secret, tokenPrefix) {
		return Actor{}, ErrUnauthenticated
	}
	var (
		id       int64
		name     string
		lastUsed sql.NullInt64
		revoked  sql.NullInt64
	)
	err := s.db.Reader().QueryRowContext(ctx,
		`SELECT id, name, last_used_at, revoked_at FROM api_tokens WHERE token_hash = ?`,
		hashToken(secret)).Scan(&id, &name, &lastUsed, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return Actor{}, ErrUnauthenticated
	}
	if err != nil {
		return Actor{}, fmt.Errorf("auth: look up token: %w", err)
	}
	if revoked.Valid {
		return Actor{}, ErrUnauthenticated
	}
	now := s.now()
	if !lastUsed.Valid || now.Sub(time.UnixMilli(lastUsed.Int64)) >= lastUsedGranularity {
		err := s.db.Write(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `UPDATE api_tokens SET last_used_at = ? WHERE id = ?`, now.UnixMilli(), id)
			return err
		})
		if err != nil {
			return Actor{}, fmt.Errorf("auth: update token usage: %w", err)
		}
	}
	return Actor{Kind: KindToken, ID: strconv.FormatInt(id, 10), Name: name}, nil
}

func optTime(n sql.NullInt64) *time.Time {
	if !n.Valid {
		return nil
	}
	t := time.UnixMilli(n.Int64).UTC()
	return &t
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
