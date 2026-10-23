package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/midnight/midnight/internal/db/pg"
)

// PostgresStore is the production human identity and session store.
type PostgresStore struct {
	Pool *pg.Pool
}

func NewPostgresStore(pool *pg.Pool) *PostgresStore {
	return &PostgresStore{Pool: pool}
}

func (s *PostgresStore) UserByEmail(ctx context.Context, email string) (UserRecord, error) {
	if s == nil || s.Pool == nil {
		return UserRecord{}, errors.New("identity postgres store: pool is required")
	}
	var (
		user      UserRecord
		totpAt    *time.Time
		deletedAt *time.Time
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT id, email, display_name, COALESCE(password_hash, ''),
		       COALESCE(totp_secret_encrypted, ''::bytea), totp_enrolled_at,
		       must_rotate_password, session_generation, is_disabled, is_admin,
		       deleted_at
		FROM users
		WHERE LOWER(email) = LOWER($1)
		LIMIT 1`,
		email,
	).Scan(
		&user.ID,
		&user.Email,
		&user.DisplayName,
		&user.PasswordHash,
		&user.TOTPSecretEnvelope,
		&totpAt,
		&user.MustRotatePassword,
		&user.SessionGeneration,
		&user.Disabled,
		&user.InstanceAdmin,
		&deletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, ErrIdentityNotFound
	}
	if err != nil {
		return UserRecord{}, fmt.Errorf("identity user lookup: %w", err)
	}
	if totpAt != nil {
		user.TOTPEnrolledAt = totpAt.UTC()
	}
	user.Deleted = deletedAt != nil
	return user, nil
}

func (s *PostgresStore) InsertSession(ctx context.Context, user UserRecord, session Session, lastLogin time.Time, maxActive int) error {
	if s == nil || s.Pool == nil {
		return errors.New("identity postgres store: pool is required")
	}
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		var generation int64
		var disabled, deleted bool
		err := tx.QueryRow(ctx, `
			SELECT session_generation, is_disabled, deleted_at IS NOT NULL
			FROM users
			WHERE id = $1
			FOR UPDATE`,
			user.ID,
		).Scan(&generation, &disabled, &deleted)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidCredentials
		}
		if err != nil {
			return fmt.Errorf("identity session user lock: %w", err)
		}
		if disabled || deleted || generation != session.SessionGeneration {
			return ErrInvalidCredentials
		}
		if maxActive > 0 {
			if _, err := tx.Exec(ctx, `
					UPDATE auth_sessions
					SET revoked_at=$2, revoked_reason='session-limit'
					WHERE id IN (
						SELECT id
						FROM auth_sessions
						WHERE user_id=$1
						  AND revoked_at IS NULL
						  AND expires_at>$2
						  AND idle_expires_at>$2
						ORDER BY created_at DESC, id DESC
						OFFSET $3
					)`,
				user.ID,
				lastLogin,
				maxActive-1,
			); err != nil {
				return fmt.Errorf("identity active session bound: %w", err)
			}
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO auth_sessions (
				id, user_id, token_hash, token_prefix, client_kind,
				session_generation, user_agent_hash, created_at, last_seen_at,
				expires_at, idle_expires_at, revoked_reason
			)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,'')`,
			session.ID,
			session.UserID,
			session.TokenHash,
			session.TokenPrefix,
			session.ClientKind,
			session.SessionGeneration,
			session.UserAgentHash,
			session.CreatedAt,
			session.LastSeenAt,
			session.ExpiresAt,
			session.IdleExpiresAt,
		)
		if err != nil {
			return fmt.Errorf("identity session insert: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE users
			SET last_login_at = $2, updated_at = $2, resource_version = resource_version + 1
			WHERE id = $1`,
			user.ID,
			lastLogin,
		); err != nil {
			return fmt.Errorf("identity last-login update: %w", err)
		}
		return nil
	})
}

func (s *PostgresStore) ConsumeLoginAttempts(ctx context.Context, bucketHashes [][]byte, now time.Time, window time.Duration, limit int) (time.Duration, error) {
	if s == nil || s.Pool == nil {
		return 0, errors.New("identity postgres store: pool is required")
	}
	if window <= 0 || limit <= 0 {
		return 0, errors.New("identity login limit: positive window and limit are required")
	}
	var retryAfter time.Duration
	err := s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		for _, bucketHash := range bucketHashes {
			var startedAt time.Time
			var count int
			if err := tx.QueryRow(ctx, `
				INSERT INTO auth_login_limits (
					bucket_hash, window_started_at, attempt_count, updated_at
				)
				VALUES ($1,$2,1,$2)
				ON CONFLICT (bucket_hash) DO UPDATE
				SET window_started_at = CASE
				        WHEN auth_login_limits.window_started_at <= $2 - make_interval(secs => $3)
				        THEN $2
				        ELSE auth_login_limits.window_started_at
				    END,
				    attempt_count = CASE
				        WHEN auth_login_limits.window_started_at <= $2 - make_interval(secs => $3)
				        THEN 1
				        ELSE auth_login_limits.attempt_count + 1
				    END,
				    updated_at=$2
				RETURNING window_started_at, attempt_count`,
				bucketHash,
				now,
				int64(window.Seconds()),
			).Scan(&startedAt, &count); err != nil {
				return fmt.Errorf("identity login limit consume: %w", err)
			}
			if count > limit {
				retry := startedAt.Add(window).Sub(now)
				if retry > retryAfter {
					retryAfter = retry
				}
			}
		}
		return nil
	})
	return retryAfter, err
}

func (s *PostgresStore) ClearLoginAttempts(ctx context.Context, bucketHashes [][]byte) error {
	if s == nil || s.Pool == nil {
		return errors.New("identity postgres store: pool is required")
	}
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		for _, bucketHash := range bucketHashes {
			if _, err := tx.Exec(ctx, `DELETE FROM auth_login_limits WHERE bucket_hash=$1`, bucketHash); err != nil {
				return fmt.Errorf("identity login limit clear: %w", err)
			}
		}
		return nil
	})
}

func (s *PostgresStore) AuthenticateSession(ctx context.Context, tokenHash []byte, now, idleExpiresAt time.Time) (UserRecord, Session, error) {
	if s == nil || s.Pool == nil {
		return UserRecord{}, Session{}, errors.New("identity postgres store: pool is required")
	}
	var (
		user      UserRecord
		session   Session
		totpAt    *time.Time
		deletedAt *time.Time
	)
	err := s.Pool.QueryRow(ctx, `
		WITH active AS (
			SELECT s.id
			FROM auth_sessions s
			JOIN users u ON u.id = s.user_id
			WHERE s.token_hash = $1
			  AND s.revoked_at IS NULL
			  AND s.expires_at > $2
			  AND s.idle_expires_at > $2
			  AND u.deleted_at IS NULL
			  AND u.is_disabled = FALSE
			  AND u.must_rotate_password = FALSE
			  AND u.session_generation = s.session_generation
			FOR UPDATE OF s
		),
		touched AS (
			UPDATE auth_sessions s
			SET last_seen_at = $2,
			    idle_expires_at = LEAST(s.expires_at, $3)
			FROM active
			WHERE s.id = active.id
			RETURNING s.id, s.user_id, s.token_hash, s.token_prefix, s.client_kind,
			          s.session_generation, s.user_agent_hash, s.created_at,
			          s.last_seen_at, s.expires_at, s.idle_expires_at,
			          s.revoked_at, s.revoked_reason
		)
		SELECT t.id, t.user_id, t.token_hash, t.token_prefix, t.client_kind,
		       t.session_generation, t.user_agent_hash, t.created_at,
		       t.last_seen_at, t.expires_at, t.idle_expires_at,
		       t.revoked_at, t.revoked_reason,
		       u.id, u.email, u.display_name, COALESCE(u.password_hash, ''),
		       COALESCE(u.totp_secret_encrypted, ''::bytea), u.totp_enrolled_at,
		       u.must_rotate_password, u.session_generation, u.is_disabled, u.is_admin,
		       u.deleted_at
		FROM touched t
		JOIN users u ON u.id = t.user_id`,
		tokenHash,
		now,
		idleExpiresAt,
	).Scan(
		&session.ID,
		&session.UserID,
		&session.TokenHash,
		&session.TokenPrefix,
		&session.ClientKind,
		&session.SessionGeneration,
		&session.UserAgentHash,
		&session.CreatedAt,
		&session.LastSeenAt,
		&session.ExpiresAt,
		&session.IdleExpiresAt,
		&session.RevokedAt,
		&session.RevokedReason,
		&user.ID,
		&user.Email,
		&user.DisplayName,
		&user.PasswordHash,
		&user.TOTPSecretEnvelope,
		&totpAt,
		&user.MustRotatePassword,
		&user.SessionGeneration,
		&user.Disabled,
		&user.InstanceAdmin,
		&deletedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return UserRecord{}, Session{}, ErrSessionNotFound
	}
	if err != nil {
		return UserRecord{}, Session{}, fmt.Errorf("identity session authenticate: %w", err)
	}
	if totpAt != nil {
		user.TOTPEnrolledAt = totpAt.UTC()
	}
	user.Deleted = deletedAt != nil
	normalizeSessionTimes(&session)
	return user, session, nil
}

func (s *PostgresStore) ListSessions(ctx context.Context, userID string) ([]Session, error) {
	if s == nil || s.Pool == nil {
		return nil, errors.New("identity postgres store: pool is required")
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT id, user_id, token_hash, token_prefix, client_kind,
		       session_generation, user_agent_hash, created_at, last_seen_at,
		       expires_at, idle_expires_at, revoked_at, revoked_reason
		FROM auth_sessions
		WHERE user_id = $1
		ORDER BY created_at DESC, id ASC`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("identity session list: %w", err)
	}
	defer rows.Close()
	out := make([]Session, 0)
	for rows.Next() {
		var session Session
		if err := rows.Scan(
			&session.ID,
			&session.UserID,
			&session.TokenHash,
			&session.TokenPrefix,
			&session.ClientKind,
			&session.SessionGeneration,
			&session.UserAgentHash,
			&session.CreatedAt,
			&session.LastSeenAt,
			&session.ExpiresAt,
			&session.IdleExpiresAt,
			&session.RevokedAt,
			&session.RevokedReason,
		); err != nil {
			return nil, fmt.Errorf("identity session scan: %w", err)
		}
		normalizeSessionTimes(&session)
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity session rows: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) RevokeSession(ctx context.Context, userID, sessionID, reason string, now time.Time) error {
	if s == nil || s.Pool == nil {
		return errors.New("identity postgres store: pool is required")
	}
	var owner string
	err := s.Pool.QueryRow(ctx, `SELECT user_id FROM auth_sessions WHERE id = $1`, sessionID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrSessionNotFound
	}
	if err != nil {
		return fmt.Errorf("identity session owner lookup: %w", err)
	}
	if owner != userID {
		return ErrSessionForbidden
	}
	if _, err := s.Pool.Exec(ctx, `
		UPDATE auth_sessions
		SET revoked_at = COALESCE(revoked_at, $3),
		    revoked_reason = CASE WHEN revoked_at IS NULL THEN $4 ELSE revoked_reason END
		WHERE id = $1 AND user_id = $2`,
		sessionID,
		userID,
		now,
		reason,
	); err != nil {
		return fmt.Errorf("identity session revoke: %w", err)
	}
	return nil
}

func (s *PostgresStore) RevokeAllSessions(ctx context.Context, userID, reason string, now time.Time) error {
	if s == nil || s.Pool == nil {
		return errors.New("identity postgres store: pool is required")
	}
	return s.Pool.WithTx(ctx, func(tx *pg.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE users
			SET session_generation = session_generation + 1,
			    updated_at = $2,
			    resource_version = resource_version + 1
			WHERE id = $1 AND deleted_at IS NULL`,
			userID,
			now,
		)
		if err != nil {
			return fmt.Errorf("identity session generation revoke: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return ErrIdentityNotFound
		}
		if _, err := tx.Exec(ctx, `
			UPDATE auth_sessions
			SET revoked_at = $2, revoked_reason = $3
			WHERE user_id = $1 AND revoked_at IS NULL`,
			userID,
			now,
			reason,
		); err != nil {
			return fmt.Errorf("identity session bulk revoke: %w", err)
		}
		return nil
	})
}

func normalizeSessionTimes(session *Session) {
	session.CreatedAt = session.CreatedAt.UTC()
	session.LastSeenAt = session.LastSeenAt.UTC()
	session.ExpiresAt = session.ExpiresAt.UTC()
	session.IdleExpiresAt = session.IdleExpiresAt.UTC()
	if session.RevokedAt != nil {
		value := session.RevokedAt.UTC()
		session.RevokedAt = &value
	}
}
