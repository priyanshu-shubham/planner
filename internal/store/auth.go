// This file holds the auth-related storage on the shared sqlStore: the per-user
// scoping switch (WithOwner) plus CRUD for users, rotating refresh tokens, and
// personal access tokens (PATs). Like the rest of the store these queries are
// dialect-agnostic and reused by both SQLite and Postgres. Only credential
// *hashes* are stored — the raw tokens live with the client.
package store

import (
	"database/sql"
	"errors"
	"time"

	"planner/internal/id"
)

// WithOwner returns a shallow copy of the store scoped to a single user, so its
// plan-touching queries see and mutate only that user's plans. ownerID == ""
// yields an unscoped store (the no-auth, single-user behavior). The copy shares
// the underlying *sql.DB; it is used per-request and never Closed.
func (s *sqlStore) WithOwner(ownerID string) Store {
	c := *s
	c.owner = ownerID
	c.grantPlan = ""
	return &c
}

// WithPlanGrant returns a shallow copy of the store scoped to exactly one plan,
// regardless of owner — the access a share link grants. Like WithOwner the copy
// shares the underlying *sql.DB, is used per-request, and is never Closed.
func (s *sqlStore) WithPlanGrant(planID string) Store {
	c := *s
	c.owner = ""
	c.grantPlan = planID
	return &c
}

// UpsertUserByGoogleSub finds or creates the user with the given Google subject,
// refreshing their profile fields on each login. The Google sub is the stable
// account key; email/name/picture can change and are kept current.
func (s *sqlStore) UpsertUserByGoogleSub(sub, email, name, picture string) (User, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	var u User
	var existing string
	err = tx.QueryRow(s.rebind(`SELECT id FROM users WHERE google_sub=?`), sub).Scan(&existing)
	switch {
	case err == nil:
		if _, err := tx.Exec(s.rebind(`UPDATE users SET email=?, name=?, picture=? WHERE id=?`),
			email, name, picture, existing); err != nil {
			return User{}, err
		}
		u = User{ID: existing, GoogleSub: sub, Email: email, Name: name, Picture: picture}
		if err := tx.QueryRow(s.rebind(`SELECT created_at FROM users WHERE id=?`), existing).Scan(&u.CreatedAt); err != nil {
			return User{}, err
		}
	case errors.Is(err, sql.ErrNoRows):
		u = User{ID: id.New("u"), GoogleSub: sub, Email: email, Name: name, Picture: picture, CreatedAt: now()}
		if _, err := tx.Exec(s.rebind(`INSERT INTO users(id,google_sub,email,name,picture,created_at) VALUES(?,?,?,?,?,?)`),
			u.ID, u.GoogleSub, u.Email, u.Name, u.Picture, u.CreatedAt); err != nil {
			return User{}, err
		}
	default:
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

// GetUser returns a user by id (ErrNotFound if absent).
func (s *sqlStore) GetUser(userID string) (User, error) {
	var u User
	err := s.db.QueryRow(s.rebind(`SELECT id,google_sub,email,name,picture,created_at FROM users WHERE id=?`), userID).
		Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name, &u.Picture, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

// CreateRefreshToken stores a new refresh-token hash for a user. expiresAt is
// normalized to UTC so it sorts consistently with now() in the expiry sweep
// regardless of the caller's timezone.
func (s *sqlStore) CreateRefreshToken(userID, tokenHash string, expiresAt time.Time) error {
	_, err := s.db.Exec(s.rebind(`INSERT INTO refresh_tokens(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`),
		tokenHash, userID, expiresAt.UTC(), now())
	return err
}

// RotateRefreshToken atomically consumes oldHash and issues newHash for the same
// user, returning that user. ErrNotFound means the presented hash was unknown
// (or already rotated) — i.e. reuse/theft — and the caller should reject the
// session. An expired token is likewise refused.
func (s *sqlStore) RotateRefreshToken(oldHash, newHash string, expiresAt time.Time) (User, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback()

	var userID string
	var exp time.Time
	err = tx.QueryRow(s.rebind(`SELECT user_id,expires_at FROM refresh_tokens WHERE token_hash=?`), oldHash).Scan(&userID, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	// Always consume the presented token, even if expired, so a leaked-but-expired
	// token can't be probed repeatedly.
	if _, err := tx.Exec(s.rebind(`DELETE FROM refresh_tokens WHERE token_hash=?`), oldHash); err != nil {
		return User{}, err
	}
	if !exp.After(now()) {
		return User{}, ErrNotFound
	}
	if _, err := tx.Exec(s.rebind(`INSERT INTO refresh_tokens(token_hash,user_id,expires_at,created_at) VALUES(?,?,?,?)`),
		newHash, userID, expiresAt.UTC(), now()); err != nil {
		return User{}, err
	}
	// Opportunistic GC: a refresh token whose cookie is never presented again
	// (logout-by-closing-the-browser, a rotated-away device) would otherwise sit
	// in the table forever. Sweeping expired rows on each rotation bounds the
	// table to roughly the set of live sessions.
	if _, err := tx.Exec(s.rebind(`DELETE FROM refresh_tokens WHERE expires_at < ?`), now()); err != nil {
		return User{}, err
	}

	var u User
	if err := tx.QueryRow(s.rebind(`SELECT id,google_sub,email,name,picture,created_at FROM users WHERE id=?`), userID).
		Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name, &u.Picture, &u.CreatedAt); err != nil {
		return User{}, err
	}
	if err := tx.Commit(); err != nil {
		return User{}, err
	}
	return u, nil
}

// DeleteRefreshToken removes a refresh-token hash (logout). A missing hash is not
// an error — logout is idempotent.
func (s *sqlStore) DeleteRefreshToken(tokenHash string) error {
	_, err := s.db.Exec(s.rebind(`DELETE FROM refresh_tokens WHERE token_hash=?`), tokenHash)
	return err
}

// ---- Personal access tokens ----

// CreatePAT stores a new PAT hash for a user under a human-readable name.
func (s *sqlStore) CreatePAT(userID, name, tokenHash string) (PAT, error) {
	p := PAT{ID: id.New("pat"), UserID: userID, Name: name, TokenHash: tokenHash, CreatedAt: now()}
	_, err := s.db.Exec(s.rebind(`INSERT INTO pats(id,user_id,name,token_hash,created_at) VALUES(?,?,?,?,?)`),
		p.ID, p.UserID, p.Name, p.TokenHash, p.CreatedAt)
	return p, err
}

// ListPATs returns a user's PATs (no hashes), newest first.
func (s *sqlStore) ListPATs(userID string) ([]PAT, error) {
	rows, err := s.db.Query(s.rebind(`SELECT id,user_id,name,created_at,last_used_at FROM pats WHERE user_id=? ORDER BY created_at DESC`), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PAT
	for rows.Next() {
		var p PAT
		var last sql.NullTime
		if err := rows.Scan(&p.ID, &p.UserID, &p.Name, &p.CreatedAt, &last); err != nil {
			return nil, err
		}
		p.LastUsedAt = last.Time
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetUserByPATHash resolves a PAT hash to its user and the PAT record (ErrNotFound
// if the hash is unknown). The bearer-auth path calls this on every PAT request.
func (s *sqlStore) GetUserByPATHash(tokenHash string) (User, PAT, error) {
	var u User
	var p PAT
	var last sql.NullTime
	err := s.db.QueryRow(s.rebind(`SELECT u.id,u.google_sub,u.email,u.name,u.picture,u.created_at,
		p.id,p.user_id,p.name,p.created_at,p.last_used_at
		FROM pats p JOIN users u ON u.id=p.user_id WHERE p.token_hash=?`), tokenHash).
		Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name, &u.Picture, &u.CreatedAt,
			&p.ID, &p.UserID, &p.Name, &p.CreatedAt, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, PAT{}, ErrNotFound
	}
	if err != nil {
		return User{}, PAT{}, err
	}
	p.LastUsedAt = last.Time
	return u, p, nil
}

// TouchPAT records that a PAT was used at when, but only if it has not been
// touched within the last minute — bounding writes on a busy token to roughly
// one per minute while keeping "last used" meaningful.
func (s *sqlStore) TouchPAT(patID string, when time.Time) error {
	cutoff := when.Add(-time.Minute)
	_, err := s.db.Exec(s.rebind(`UPDATE pats SET last_used_at=? WHERE id=? AND (last_used_at IS NULL OR last_used_at < ?)`),
		when, patID, cutoff)
	return err
}

// DeletePAT revokes one of a user's PATs (scoped to the user so one user cannot
// revoke another's token). ErrNotFound if the user has no such PAT.
func (s *sqlStore) DeletePAT(userID, patID string) error {
	res, err := s.db.Exec(s.rebind(`DELETE FROM pats WHERE id=? AND user_id=?`), patID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
