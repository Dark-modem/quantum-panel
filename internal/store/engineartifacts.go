// engineartifacts.go — Phase 5 R5 engine artifact store (ex-models3.go,
// REFACTOR-S3: the phase-history file split is dissolved into per-subsystem
// files). One row per signed engine binary the operator uploaded (engine
// id, version, arch, sha256, size, ed25519 signature). The FILE lives in
// <configDir>/engine-artifacts/ (0600); this table carries only the pins
// the agent re-verifies on its side — the panel is never trusted for
// integrity (R5's trust model).
//
// The duplicate rule lives at the UNIQUE(engine, version, goos, goarch)
// constraint plus the handler's hash check: re-uploading the SAME build
// (same sha256) is an accepted update; a DIFFERENT hash for the same
// slot is ErrArtifactHashConflict (409 at the REST layer).

package store

import (
	"database/sql"
	"errors"
)

// ErrArtifactHashConflict is returned when an upload targets an
// (engine, version, goos, goarch) slot that already holds a DIFFERENT
// build. Same-hash re-uploads are accepted (idempotent update).
var ErrArtifactHashConflict = errors.New("store: engine artifact version+arch already exists with a different sha256")

// EngineArtifact is one uploaded engine artifact. Signature is the
// base64 ed25519 signature over the exact artifact bytes; the operator
// signs with the release key whose PUBLIC half the agent build pins
// (engineArtifactPubKeyB64) and the panel verifies at upload time.
type EngineArtifact struct {
	ID         int64  `json:"id"`
	Engine     string `json:"engine"`
	Version    string `json:"version"`
	Goos       string `json:"goos"`
	Goarch     string `json:"goarch"`
	Artifact   string `json:"artifact"` // download name (strict pattern)
	SHA256     string `json:"sha256"`   // lowercase hex
	Size       int64  `json:"size"`
	Signature  string `json:"signature"`
	Uploader   string `json:"uploader,omitempty"`
	UploadedAt int64  `json:"uploaded_at"`
	// Source is provenance only (BACKEND-38 §2.22): "upload" = manual
	// multipart upload, "repo" = resolved from the quantum-cores release
	// feed. Never part of the agent-facing integrity pins.
	Source string `json:"source"`
}

// UpsertEngineArtifact inserts or updates one artifact row. Slot rule:
// (engine, version, goos, goarch). Existing slot + same sha256 → update
// in place; existing slot + different sha256 → ErrArtifactHashConflict;
// existing artifact NAME on a different slot → name-taken error (the
// REST layer maps both to 409).
func (s *Store) UpsertEngineArtifact(a EngineArtifact) (int64, error) {
	var existing EngineArtifact
	err := s.db.QueryRow(`SELECT id, sha256, artifact FROM engine_artifacts
		WHERE engine = ? AND version = ? AND goos = ? AND goarch = ?`,
		a.Engine, a.Version, a.Goos, a.Goarch).Scan(&existing.ID, &existing.SHA256, &existing.Artifact)
	switch {
	case err == nil:
		if existing.SHA256 != a.SHA256 {
			return 0, ErrArtifactHashConflict
		}
		// Same build re-uploaded: keep the download name stable unless it
		// is free (a changed name for the same hash is fine).
		if a.Artifact != existing.Artifact {
			if err := s.engineArtifactNameTaken(a.Artifact, existing.ID); err != nil {
				return 0, err
			}
		}
		_, err = s.db.Exec(`UPDATE engine_artifacts
			SET artifact = ?, size = ?, signature = ?, uploader = ?, uploaded_at = ?, source = ?
			WHERE id = ?`,
			a.Artifact, a.Size, a.Signature, a.Uploader, a.UploadedAt, a.Source, existing.ID)
		return existing.ID, err
	case errors.Is(err, sql.ErrNoRows):
		if err := s.engineArtifactNameTaken(a.Artifact, 0); err != nil {
			return 0, err
		}
		res, err := s.db.Exec(`INSERT INTO engine_artifacts
			(engine, version, goos, goarch, artifact, sha256, size, signature, uploader, uploaded_at, source)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.Engine, a.Version, a.Goos, a.Goarch, a.Artifact, a.SHA256, a.Size, a.Signature, a.Uploader, a.UploadedAt, a.Source)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	default:
		return 0, err
	}
}

// engineArtifactNameTaken rejects an artifact download name already used
// by a DIFFERENT row (the file namespace is 1:1 with rows).
func (s *Store) engineArtifactNameTaken(name string, rowID int64) error {
	var owner int64
	err := s.db.QueryRow(`SELECT id FROM engine_artifacts WHERE artifact = ?`, name).Scan(&owner)
	switch {
	case err == sql.ErrNoRows:
		return nil
	case err != nil:
		return err
	case owner != rowID:
		return errors.New("store: artifact download name already in use")
	}
	return nil
}

// ListEngineArtifacts returns every uploaded artifact, newest first.
func (s *Store) ListEngineArtifacts() ([]EngineArtifact, error) {
	return scanList(s, `SELECT id, engine, version, goos, goarch, artifact, sha256, size, signature, uploader, uploaded_at, source
		FROM engine_artifacts ORDER BY uploaded_at DESC, id DESC`, func(a *EngineArtifact) []any {
		return []any{&a.ID, &a.Engine, &a.Version, &a.Goos, &a.Goarch, &a.Artifact, &a.SHA256, &a.Size, &a.Signature, &a.Uploader, &a.UploadedAt, &a.Source}
	})
}

// GetEngineArtifact returns one artifact row by id.
func (s *Store) GetEngineArtifact(id int64) (EngineArtifact, error) {
	return scanOne(s, `SELECT id, engine, version, goos, goarch, artifact, sha256, size, signature, uploader, uploaded_at, source
		FROM engine_artifacts WHERE id = ?`, func(a *EngineArtifact) []any {
		return []any{&a.ID, &a.Engine, &a.Version, &a.Goos, &a.Goarch, &a.Artifact, &a.SHA256, &a.Size, &a.Signature, &a.Uploader, &a.UploadedAt, &a.Source}
	}, id)
}

// GetEngineArtifactByName resolves the download name → pins (the agent
// file endpoint uses it for a consistent 404 on unknown names).
func (s *Store) GetEngineArtifactByName(name string) (EngineArtifact, error) {
	return scanOne(s, `SELECT id, engine, version, goos, goarch, artifact, sha256, size, signature, uploader, uploaded_at, source
		FROM engine_artifacts WHERE artifact = ?`, func(a *EngineArtifact) []any {
		return []any{&a.ID, &a.Engine, &a.Version, &a.Goos, &a.Goarch, &a.Artifact, &a.SHA256, &a.Size, &a.Signature, &a.Uploader, &a.UploadedAt, &a.Source}
	}, name)
}

// DeleteEngineArtifact removes the row (the caller deletes the file).
func (s *Store) DeleteEngineArtifact(id int64) error {
	res, err := s.db.Exec(`DELETE FROM engine_artifacts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return ErrNotFound
	}
	return nil
}

// EngineEnabledCounts counts enabled tunnels per engine id (the Engines
// tab uses this for the "engine enabled but no signed artifact" warning).
func (s *Store) EngineEnabledCounts() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT engine, COUNT(*) FROM tunnels WHERE enabled = 1 AND engine <> '' GROUP BY engine`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var engine string
		var n int
		if err := rows.Scan(&engine, &n); err != nil {
			return nil, err
		}
		out[engine] = n
	}
	return out, rows.Err()
}
