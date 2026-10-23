// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.

package sourceartifact

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/midnight/midnight/internal/db/pg"
)

const (
	legacyLocalProvider       = "legacy-local"
	legacyLocalURIPrefix      = "local://"
	legacyStagePrefix         = "midnight-up-"
	legacyStageSuffix         = ".tar.gz"
	legacyCheckoutPrefix      = "midnight-up-src-"
	legacyQuarantineDirectory = "legacy-quarantine"
	legacyQuarantinePrefix    = "midnight-legacy-quarantine-"

	// A separate election lock keeps compatibility cleanup from making every
	// build worker repeat the same filesystem pass. It deliberately does not
	// serialize CAS binding; legacy candidates are protected by durable
	// pending/leased queue truth and a conservative age gate.
	legacyJanitorAdvisoryLockKey int64 = 60718768232145223
)

// LegacyJanitorResult reports only exact, Midnight-owned compatibility paths.
// It never counts or removes the CAS staging/checkouts managed by RunJanitor.
type LegacyJanitorResult struct {
	Skipped               bool
	StagesQuarantined     int
	CheckoutsQuarantined  int
	QuarantineRestored    int
	QuarantineFinalized   int
	CandidatesExamined    int
	ProtectedCheckoutRefs int
}

type legacyWorkKind string

const (
	legacyWorkStage    legacyWorkKind = "stage"
	legacyWorkCheckout legacyWorkKind = "checkout"
)

type legacyWorkCandidate struct {
	kind        legacyWorkKind
	path        string
	original    string
	container   string
	modifiedAt  time.Time
	quarantined bool
	finalize    bool
}

func (candidate legacyWorkCandidate) repoURI() string {
	if candidate.kind != legacyWorkCheckout {
		return ""
	}
	return legacyLocalURIPrefix + candidate.original
}

type preparedLegacySweep struct {
	store  *FSStore
	moves  []legacyWorkCandidate
	result LegacyJanitorResult
}

// RunLegacyJanitor reclaims only crash leftovers from the retained
// automation/operator extractor:
//
//   - <root>/midnight-up-*.tar.gz
//   - <root>/midnight-up-src-*
//
// Pending/delivering outbox rows and pending/leased build jobs protect their
// exact local:// checkout. A path is first atomically moved to a dedicated
// quarantine and can be deleted only by a later pass after the quarantine
// grace period. The compatibility path remains intentionally separate from
// content-addressed tenant artifacts.
func (s *PostgresStore) RunLegacyJanitor(
	ctx context.Context,
	filesystem *FSStore,
	now time.Time,
	config JanitorConfig,
) (LegacyJanitorResult, error) {
	if s == nil || s.Pool == nil {
		return LegacyJanitorResult{}, errors.New("source artifact: Postgres pool is required")
	}
	if filesystem == nil {
		return LegacyJanitorResult{}, errors.New("source artifact: filesystem store is required")
	}
	if err := config.Validate(); err != nil {
		return LegacyJanitorResult{}, err
	}
	if now.IsZero() {
		now = s.now()
	}
	now = now.UTC()

	// Discovery performs no mutation and therefore does not hold a database
	// lock. Bound the candidate window above the mutation budget so a small
	// number of active compatibility jobs cannot starve old crash leftovers.
	discoveryLimit := config.MaxItems * 8
	if discoveryLimit < config.MaxItems {
		discoveryLimit = config.MaxItems
	}
	candidates, err := filesystem.discoverLegacyWork(ctx, discoveryLimit)
	if err != nil {
		return LegacyJanitorResult{}, err
	}

	var sweep *preparedLegacySweep
	var result LegacyJanitorResult
	err = s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		locked, err := tryAcquireLegacyJanitorLock(ctx, tx)
		if err != nil {
			return err
		}
		if !locked {
			result.Skipped = true
			return nil
		}
		protected, err := protectedLegacyReposInTx(ctx, tx, candidates)
		if err != nil {
			return err
		}
		sweep, err = filesystem.prepareLegacySweep(
			ctx,
			candidates,
			protected,
			now.Add(-config.WorkRetention),
			now.Add(-config.QuarantineRetention),
			config.MaxItems,
		)
		if err != nil {
			return err
		}
		result = sweep.result
		result.ProtectedCheckoutRefs = len(protected)
		return nil
	})
	if err != nil {
		if sweep != nil {
			err = errors.Join(err, sweep.Restore())
		}
		return result, err
	}
	if sweep != nil {
		if err := sweep.Finalize(); err != nil {
			return result, err
		}
		result.QuarantineFinalized = sweep.result.QuarantineFinalized
	}
	return result, nil
}

func tryAcquireLegacyJanitorLock(ctx context.Context, tx *pg.Tx) (bool, error) {
	if tx == nil {
		return false, errors.New("source artifact: legacy janitor transaction is required")
	}
	var locked bool
	if err := tx.QueryRow(
		ctx,
		`SELECT pg_try_advisory_xact_lock($1)`,
		legacyJanitorAdvisoryLockKey,
	).Scan(&locked); err != nil {
		return false, fmt.Errorf("source artifact: try legacy janitor lock: %w", err)
	}
	return locked, nil
}

func protectedLegacyReposInTx(
	ctx context.Context,
	tx *pg.Tx,
	candidates []legacyWorkCandidate,
) (map[string]struct{}, error) {
	repos := make([]string, 0, len(candidates))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		repo := candidate.repoURI()
		if repo == "" {
			continue
		}
		if _, exists := seen[repo]; exists {
			continue
		}
		seen[repo] = struct{}{}
		repos = append(repos, repo)
	}
	protected := map[string]struct{}{}
	if len(repos) == 0 {
		return protected, nil
	}
	rows, err := tx.Query(
		ctx,
		`SELECT DISTINCT protected.repo
		   FROM (
		       SELECT payload #>> '{request,source,repo}' AS repo
		         FROM jobs
		        WHERE queue='build'
		          AND state IN ('pending','leased')
		          AND payload #>> '{request,source,provider}'=$1
		          AND payload #>> '{request,source,repo}'=ANY($2::text[])
		       UNION
		       SELECT payload #>> '{request,source,repo}' AS repo
		         FROM deploy_intake_outbox
		        WHERE queue='build'
		          AND state IN ('pending','delivering')
		          AND payload #>> '{request,source,provider}'=$1
		          AND payload #>> '{request,source,repo}'=ANY($2::text[])
		   ) AS protected
		  WHERE protected.repo IS NOT NULL`,
		legacyLocalProvider,
		repos,
	)
	if err != nil {
		return nil, fmt.Errorf("source artifact: list protected legacy checkouts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var repo string
		if err := rows.Scan(&repo); err != nil {
			return nil, fmt.Errorf("source artifact: scan protected legacy checkout: %w", err)
		}
		if _, candidate := seen[repo]; !candidate {
			return nil, fmt.Errorf("%w: database returned an unexpected legacy checkout", ErrIntegrity)
		}
		protected[repo] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("source artifact: list protected legacy checkouts: %w", err)
	}
	return protected, nil
}

func (s *FSStore) discoverLegacyWork(
	ctx context.Context,
	maxItems int,
) ([]legacyWorkCandidate, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if maxItems <= 0 {
		return nil, errors.New("source artifact: legacy discovery limit must be positive")
	}
	quarantineRoot := filepath.Join(s.root, legacyQuarantineDirectory)
	if err := ensureDirectory(quarantineRoot, 0o750); err != nil {
		return nil, fmt.Errorf("source artifact: initialize legacy quarantine: %w", err)
	}

	s.janitorScanMu.Lock()
	defer s.janitorScanMu.Unlock()
	if s.janitorScanHandles == nil {
		s.janitorScanHandles = map[string]*os.File{}
	}

	candidates := make([]legacyWorkCandidate, 0, maxItems)
	examined := 0
	emptyCategories := 0
	exhausted := [2]bool{}
	seenCandidates := map[string]struct{}{}
	for examined < maxItems && emptyCategories < 2 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		category := s.janitorLegacyScanCategory % 2
		s.janitorLegacyScanCategory = (s.janitorLegacyScanCategory + 1) % 2
		if exhausted[category] {
			emptyCategories++
			continue
		}
		key, directory := "legacy-quarantine", quarantineRoot
		if category == 1 {
			key, directory = "legacy-root", s.root
		}
		entry, found, err := s.nextJanitorDirectoryEntry(key, directory)
		if err != nil {
			return nil, err
		}
		if !found {
			exhausted[category] = true
			emptyCategories++
			continue
		}
		emptyCategories = 0
		examined++
		if category == 0 {
			if entry.Type()&os.ModeSymlink != 0 ||
				!entry.IsDir() ||
				!strings.HasPrefix(entry.Name(), legacyQuarantinePrefix) {
				return nil, fmt.Errorf("%w: unexpected legacy quarantine entry", ErrIntegrity)
			}
			candidate, err := s.readLegacyQuarantine(quarantineRoot, entry.Name())
			if err != nil {
				return nil, err
			}
			if _, duplicate := seenCandidates[candidate.path]; !duplicate {
				seenCandidates[candidate.path] = struct{}{}
				candidates = append(candidates, candidate)
			}
			continue
		}

		kind, recognized := legacyKindForName(entry.Name())
		if !recognized {
			continue
		}
		path := filepath.Join(s.root, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("source artifact: inspect legacy %s: %w", kind, err)
		}
		if info.Mode()&os.ModeSymlink != 0 ||
			(kind == legacyWorkStage && !info.Mode().IsRegular()) ||
			(kind == legacyWorkCheckout && !info.IsDir()) {
			return nil, fmt.Errorf("%w: malformed legacy %s path", ErrIntegrity, kind)
		}
		candidate := legacyWorkCandidate{
			kind:       kind,
			path:       path,
			original:   path,
			modifiedAt: info.ModTime().UTC(),
		}
		if _, duplicate := seenCandidates[candidate.path]; !duplicate {
			seenCandidates[candidate.path] = struct{}{}
			candidates = append(candidates, candidate)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].quarantined != candidates[j].quarantined {
			return candidates[i].quarantined
		}
		if !candidates[i].modifiedAt.Equal(candidates[j].modifiedAt) {
			return candidates[i].modifiedAt.Before(candidates[j].modifiedAt)
		}
		return candidates[i].path < candidates[j].path
	})
	return candidates, nil
}

func legacyKindForName(name string) (legacyWorkKind, bool) {
	switch {
	case strings.HasPrefix(name, legacyCheckoutPrefix) &&
		len(name) > len(legacyCheckoutPrefix):
		return legacyWorkCheckout, true
	case strings.HasPrefix(name, legacyStagePrefix) &&
		strings.HasSuffix(name, legacyStageSuffix) &&
		len(name) > len(legacyStagePrefix)+len(legacyStageSuffix):
		return legacyWorkStage, true
	default:
		return "", false
	}
}

func (s *FSStore) readLegacyQuarantine(
	quarantineRoot string,
	containerName string,
) (legacyWorkCandidate, error) {
	container := filepath.Join(quarantineRoot, containerName)
	if filepath.Dir(container) != quarantineRoot ||
		!strings.HasPrefix(containerName, legacyQuarantinePrefix) {
		return legacyWorkCandidate{}, ErrOwnership
	}
	entries, err := os.ReadDir(container)
	if err != nil || len(entries) != 1 {
		return legacyWorkCandidate{}, fmt.Errorf("%w: legacy quarantine must contain one payload", ErrIntegrity)
	}
	kind, recognized := legacyKindForName(entries[0].Name())
	if !recognized {
		return legacyWorkCandidate{}, fmt.Errorf("%w: malformed legacy quarantine payload", ErrIntegrity)
	}
	path := filepath.Join(container, entries[0].Name())
	info, err := os.Lstat(path)
	if err != nil {
		return legacyWorkCandidate{}, fmt.Errorf("source artifact: inspect legacy quarantine payload: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		(kind == legacyWorkStage && !info.Mode().IsRegular()) ||
		(kind == legacyWorkCheckout && !info.IsDir()) {
		return legacyWorkCandidate{}, fmt.Errorf("%w: malformed legacy quarantine payload type", ErrIntegrity)
	}
	containerInfo, err := os.Lstat(container)
	if err != nil || !containerInfo.IsDir() || containerInfo.Mode()&os.ModeSymlink != 0 {
		return legacyWorkCandidate{}, fmt.Errorf("%w: malformed legacy quarantine container", ErrIntegrity)
	}
	return legacyWorkCandidate{
		kind:        kind,
		path:        path,
		original:    filepath.Join(s.root, entries[0].Name()),
		container:   container,
		modifiedAt:  info.ModTime().UTC(),
		quarantined: true,
	}, nil
}

func (s *FSStore) prepareLegacySweep(
	ctx context.Context,
	candidates []legacyWorkCandidate,
	protected map[string]struct{},
	workCutoff time.Time,
	quarantineCutoff time.Time,
	maxItems int,
) (*preparedLegacySweep, error) {
	sweep := &preparedLegacySweep{store: s}
	actions := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			_ = sweep.Restore()
			return nil, err
		}
		sweep.result.CandidatesExamined++
		_, isProtected := protected[candidate.repoURI()]
		if candidate.quarantined && isProtected {
			if actions >= maxItems {
				continue
			}
			if err := restoreLegacyCandidate(candidate); err != nil {
				_ = sweep.Restore()
				return nil, err
			}
			sweep.result.QuarantineRestored++
			actions++
			continue
		}
		if isProtected ||
			workCutoff.IsZero() ||
			!candidate.modifiedAt.Before(workCutoff) {
			continue
		}
		if candidate.quarantined {
			info, err := os.Lstat(candidate.container)
			if err != nil {
				_ = sweep.Restore()
				return nil, fmt.Errorf("source artifact: inspect legacy quarantine age: %w", err)
			}
			if quarantineCutoff.IsZero() ||
				!info.ModTime().UTC().Before(quarantineCutoff) ||
				actions >= maxItems {
				continue
			}
			candidate.finalize = true
			sweep.moves = append(sweep.moves, candidate)
			actions++
			continue
		}
		if actions >= maxItems {
			continue
		}
		move, err := s.quarantineLegacyCandidate(candidate)
		if err != nil {
			_ = sweep.Restore()
			return nil, err
		}
		sweep.moves = append(sweep.moves, move)
		if candidate.kind == legacyWorkStage {
			sweep.result.StagesQuarantined++
		} else {
			sweep.result.CheckoutsQuarantined++
		}
		actions++
	}
	return sweep, nil
}

func (s *FSStore) quarantineLegacyCandidate(
	candidate legacyWorkCandidate,
) (legacyWorkCandidate, error) {
	if candidate.quarantined ||
		filepath.Dir(candidate.path) != s.root {
		return legacyWorkCandidate{}, ErrOwnership
	}
	if kind, recognized := legacyKindForName(filepath.Base(candidate.path)); !recognized || kind != candidate.kind {
		return legacyWorkCandidate{}, ErrOwnership
	}
	quarantineRoot := filepath.Join(s.root, legacyQuarantineDirectory)
	if err := ensureDirectory(quarantineRoot, 0o750); err != nil {
		return legacyWorkCandidate{}, err
	}
	container, err := os.MkdirTemp(quarantineRoot, legacyQuarantinePrefix)
	if err != nil {
		return legacyWorkCandidate{}, fmt.Errorf("source artifact: create legacy quarantine: %w", err)
	}
	move := candidate
	move.container = container
	move.path = filepath.Join(container, filepath.Base(candidate.path))
	move.quarantined = true
	if err := os.Rename(candidate.path, move.path); err != nil {
		_ = os.Remove(container)
		return legacyWorkCandidate{}, fmt.Errorf("source artifact: quarantine legacy %s: %w", candidate.kind, err)
	}
	if err := syncDirectory(s.root); err != nil {
		return move, errors.Join(err, restoreLegacyCandidate(move))
	}
	if err := syncDirectory(container); err != nil {
		return move, errors.Join(err, restoreLegacyCandidate(move))
	}
	if err := syncDirectory(quarantineRoot); err != nil {
		return move, errors.Join(err, restoreLegacyCandidate(move))
	}
	return move, nil
}

func restoreLegacyCandidate(candidate legacyWorkCandidate) error {
	if !candidate.quarantined ||
		candidate.container == "" ||
		filepath.Dir(candidate.container) == candidate.container {
		return ErrOwnership
	}
	if _, err := os.Lstat(candidate.path); errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(candidate.container)
		return nil
	} else if err != nil {
		return fmt.Errorf("source artifact: inspect legacy quarantine payload: %w", err)
	}
	if _, err := os.Lstat(candidate.original); err == nil {
		return fmt.Errorf("%w: legacy restore destination already exists", ErrIntegrity)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("source artifact: inspect legacy restore destination: %w", err)
	}
	if err := os.Rename(candidate.path, candidate.original); err != nil {
		return fmt.Errorf("source artifact: restore legacy %s: %w", candidate.kind, err)
	}
	if err := syncDirectory(filepath.Dir(candidate.original)); err != nil {
		return err
	}
	if err := os.Remove(candidate.container); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("source artifact: remove legacy quarantine container: %w", err)
	}
	return syncDirectory(filepath.Dir(candidate.container))
}

func (sweep *preparedLegacySweep) Restore() error {
	if sweep == nil {
		return nil
	}
	var result error
	for index := len(sweep.moves) - 1; index >= 0; index-- {
		move := sweep.moves[index]
		if move.finalize {
			continue
		}
		if err := restoreLegacyCandidate(move); err != nil {
			result = errors.Join(result, err)
		}
	}
	return result
}

func (sweep *preparedLegacySweep) Finalize() error {
	if sweep == nil {
		return nil
	}
	quarantineRoot := filepath.Join(sweep.store.root, legacyQuarantineDirectory)
	var result error
	for _, candidate := range sweep.moves {
		if !candidate.finalize {
			continue
		}
		if filepath.Dir(candidate.container) != quarantineRoot ||
			!strings.HasPrefix(filepath.Base(candidate.container), legacyQuarantinePrefix) {
			result = errors.Join(result, ErrOwnership)
			continue
		}
		if err := os.RemoveAll(candidate.container); err != nil {
			result = errors.Join(result, fmt.Errorf("source artifact: finalize legacy quarantine: %w", err))
			continue
		}
		sweep.result.QuarantineFinalized++
	}
	if err := syncDirectory(quarantineRoot); err != nil {
		result = errors.Join(result, err)
	}
	return result
}
