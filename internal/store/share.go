// This file holds plan-sharing storage on the shared sqlStore. A plan's
// share_id is a high-entropy capability stored in plaintext on the plans row:
// presenting it (web/auth.go resolvePlan) yields a WithPlanGrant-scoped store
// with view+comment access. Revocation nulls the column; the unique index keeps
// one plan per share id while any number of unshared (NULL) plans coexist.
package store

import (
	"database/sql"
	"errors"
	"strings"

	"planner/internal/id"
)

// EnsureShareID returns the plan's share id, minting and storing one if the
// plan has none. Idempotent: re-sharing returns the existing id and leaves the
// current share policy unchanged (revoke first to rotate). Scope misses (not the
// owner's plan) are ErrNotFound.
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

// SetSharePolicy creates a share id if needed and stores what versions the link
// exposes. allVersions=true exposes the whole plan, including future versions.
// allVersions=false exposes exactly the supplied existing version numbers.
func (s *sqlStore) SetSharePolicy(planID string, allVersions bool, versions []int) (string, error) {
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
	sid := existing.String
	if sid == "" {
		sid = id.NewLong("share")
	}

	scope := "selected"
	if allVersions {
		scope = "all"
	}
	if _, err := tx.Exec(s.rebind(`UPDATE plans SET share_id=?, share_scope=? WHERE id=?`), sid, scope, planID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(s.rebind(`DELETE FROM share_versions WHERE plan_id=?`), planID); err != nil {
		return "", err
	}
	if !allVersions {
		versionIDs, err := s.versionIDsForNumbers(tx, planID, versions)
		if err != nil {
			return "", err
		}
		if len(versionIDs) > 0 {
			values := strings.TrimRight(strings.Repeat("(?,?),", len(versionIDs)), ",")
			insertArgs := make([]any, 0, len(versionIDs)*2)
			for _, versionID := range versionIDs {
				insertArgs = append(insertArgs, planID, versionID)
			}
			if _, err := tx.Exec(s.rebind(`INSERT INTO share_versions(plan_id,version_id) VALUES `+values), insertArgs...); err != nil {
				return "", err
			}
		}
	}
	return sid, tx.Commit()
}

func (s *sqlStore) versionIDsForNumbers(tx *sql.Tx, planID string, numbers []int) ([]string, error) {
	seen := map[int]bool{}
	var distinct []int
	for _, number := range numbers {
		if seen[number] {
			continue
		}
		seen[number] = true
		distinct = append(distinct, number)
	}
	if len(distinct) == 0 {
		return nil, nil
	}

	placeholders := strings.TrimRight(strings.Repeat("?,", len(distinct)), ",")
	args := make([]any, 0, len(distinct)+1)
	args = append(args, planID)
	for _, number := range distinct {
		args = append(args, number)
	}
	rows, err := tx.Query(s.rebind(`SELECT id,number FROM versions WHERE plan_id=? AND number IN (`+placeholders+`)`), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byNumber := map[int]string{}
	for rows.Next() {
		var versionID string
		var number int
		if err := rows.Scan(&versionID, &number); err != nil {
			return nil, err
		}
		byNumber[number] = versionID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(byNumber) != len(distinct) {
		return nil, ErrNotFound
	}

	versionIDs := make([]string, 0, len(distinct))
	for _, number := range distinct {
		versionID, ok := byNumber[number]
		if !ok {
			return nil, ErrNotFound
		}
		versionIDs = append(versionIDs, versionID)
	}
	return versionIDs, nil
}

// ClearShareID revokes a plan's share id (sets it NULL) and resets the share
// policy. Idempotent for an already-unshared plan; a scope miss or unknown plan
// is ErrNotFound.
func (s *sqlStore) ClearShareID(planID string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	pred, args := s.planPred("id", "owner_id")
	res, err := tx.Exec(s.rebind(`UPDATE plans SET share_id=NULL, share_scope='all' WHERE id=?`+pred), append([]any{planID}, args...)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(s.rebind(`DELETE FROM share_versions WHERE plan_id=?`), planID); err != nil {
		return err
	}
	return tx.Commit()
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
