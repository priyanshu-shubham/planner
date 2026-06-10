// This file holds plan-sharing storage on the shared sqlStore. A plan's
// share_id is a high-entropy capability stored in plaintext on the plans row:
// presenting it (web/auth.go resolvePlan) yields a WithPlanGrant-scoped store
// with view+comment access. Revocation nulls the column; the unique index keeps
// one plan per share id while any number of unshared (NULL) plans coexist.
package store

import (
	"database/sql"
	"errors"

	"planner/internal/id"
)

// EnsureShareID returns the plan's share id, minting and storing one if the
// plan has none. Idempotent: re-sharing returns the existing id (revoke first
// to rotate). Scope misses (not the owner's plan) are ErrNotFound.
func (s *sqlStore) EnsureShareID(planID string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	pred, args := s.planPred("id", "owner_id")
	var existing sql.NullString
	err = tx.QueryRow(s.rebind(`SELECT share_id FROM plans WHERE id=?`+pred), append([]any{planID}, args...)...).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if existing.String != "" {
		return existing.String, nil
	}
	sid := id.NewLong("share")
	if _, err := tx.Exec(s.rebind(`UPDATE plans SET share_id=? WHERE id=?`), sid, planID); err != nil {
		return "", err
	}
	return sid, tx.Commit()
}

// ClearShareID revokes a plan's share id (sets it NULL). Idempotent for an
// already-unshared plan; a scope miss or unknown plan is ErrNotFound.
func (s *sqlStore) ClearShareID(planID string) error {
	pred, args := s.planPred("id", "owner_id")
	res, err := s.db.Exec(s.rebind(`UPDATE plans SET share_id=NULL WHERE id=?`+pred), append([]any{planID}, args...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveShareID maps a share id to its plan id, or ErrNotFound when unknown or
// revoked. Deliberately unscoped: possession of the share id is the
// authorization, so this runs on the root store before any scope exists.
func (s *sqlStore) ResolveShareID(shareID string) (string, error) {
	if shareID == "" {
		return "", ErrNotFound
	}
	var planID string
	err := s.db.QueryRow(s.rebind(`SELECT id FROM plans WHERE share_id=?`), shareID).Scan(&planID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return planID, err
}
