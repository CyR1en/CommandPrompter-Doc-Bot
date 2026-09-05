package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"time"

	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	DefaultPasswordConcurrency = 2
	dummyPasswordHash          = "scrypt$v=1$n=16384$r=8$p=1$cmVmMC1kdW1teS1zYWx0IQ" +
		"$2yl6gybxhistUUXdIeovo5ykmeamzwJQdTwsGJ3nS90"
)

type SessionService interface {
	Bootstrap(context.Context, BootstrapCommand) (AuthenticatedSession, error)
	Login(context.Context, LoginCommand) (AuthenticatedSession, error)
	Authenticate(context.Context, SessionToken) (OperatorSession, error)
	Logout(context.Context, SessionID) error
}

type Service struct {
	pool           *pgxpool.Pool
	sessionTTL     time.Duration
	passwordSlots  chan struct{}
	clock          func() time.Time
	hashPassword   func(string) (string, error)
	verifyPassword func(string, string) bool
}

func NewService(pool *pgxpool.Pool, sessionTTL time.Duration, passwordConcurrency int) (*Service, error) {
	if pool == nil || sessionTTL <= 0 || passwordConcurrency <= 0 {
		return nil, errors.New("authentication service configuration is invalid")
	}
	return &Service{
		pool:           pool,
		sessionTTL:     sessionTTL,
		passwordSlots:  make(chan struct{}, passwordConcurrency),
		clock:          time.Now,
		hashPassword:   security.HashPassword,
		verifyPassword: security.VerifyPassword,
	}, nil
}

func (service *Service) InitializeBootstrap(
	ctx context.Context,
	token *security.SecretValue,
	expiresIn time.Duration,
) error {
	if expiresIn <= 0 {
		return ErrServiceUnavailable
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err = tx.Exec(ctx, `
		SELECT pg_advisory_xact_lock(
			hashtextextended('ref0:operator-bootstrap', 0)
		)
	`); err != nil {
		return ErrServiceUnavailable
	}
	var operatorExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operators)`).Scan(&operatorExists); err != nil {
		return ErrServiceUnavailable
	}
	if operatorExists || token == nil {
		return commit(tx, ctx)
	}

	now := service.now()
	digest := DigestToken(token.Reveal())
	var storedDigest []byte
	var consumedAt pgtype.Timestamptz
	err = tx.QueryRow(ctx, `
		SELECT token_digest, consumed_at
		FROM bootstrap_tokens
		WHERE id = 1
		FOR UPDATE
	`).Scan(&storedDigest, &consumedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err = tx.Exec(ctx, `
			INSERT INTO bootstrap_tokens (
				id, token_digest, created_at, expires_at
			) VALUES (1, $1, $2, $3)
		`, digest[:], now, now.Add(expiresIn)); err != nil {
			return ErrServiceUnavailable
		}
	case err != nil:
		return ErrServiceUnavailable
	case !consumedAt.Valid && !hmac.Equal(storedDigest, digest[:]):
		if _, err = tx.Exec(ctx, `
			UPDATE bootstrap_tokens
			SET token_digest = $1, created_at = $2, expires_at = $3
			WHERE id = 1
		`, digest[:], now, now.Add(expiresIn)); err != nil {
			return ErrServiceUnavailable
		}
	}
	return commit(tx, ctx)
}

func (service *Service) Bootstrap(
	ctx context.Context,
	command BootstrapCommand,
) (AuthenticatedSession, error) {
	if command.Password == nil || command.BootstrapToken == nil ||
		command.Username.Display == "" || command.Username.Key == "" {
		return AuthenticatedSession{}, ErrBootstrapDenied
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var storedDigest []byte
	var expiresAt time.Time
	var consumedAt pgtype.Timestamptz
	err = tx.QueryRow(ctx, `
		SELECT token_digest, expires_at, consumed_at
		FROM bootstrap_tokens
		WHERE id = 1
		FOR UPDATE
	`).Scan(&storedDigest, &expiresAt, &consumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthenticatedSession{}, ErrBootstrapDenied
	}
	if err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}

	now := service.now()
	submittedDigest := DigestToken(command.BootstrapToken.Reveal())
	if consumedAt.Valid || !expiresAt.After(now) || !hmac.Equal(storedDigest, submittedDigest[:]) {
		return AuthenticatedSession{}, ErrBootstrapDenied
	}
	var operatorExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM operators)`).Scan(&operatorExists); err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	if operatorExists {
		return AuthenticatedSession{}, ErrBootstrapDenied
	}

	if err = service.acquirePasswordSlot(ctx); err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	passwordHash, hashErr := service.hashPassword(command.Password.Reveal())
	service.releasePasswordSlot()
	if hashErr != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}

	var operatorID pgtype.UUID
	if err = tx.QueryRow(ctx, `
		INSERT INTO operators (
			username, username_key, password_hash, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $4)
		RETURNING id
	`, command.Username.Display, command.Username.Key, passwordHash, now).Scan(&operatorID); err != nil || !operatorID.Valid {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	if _, err = tx.Exec(ctx, `
		UPDATE bootstrap_tokens SET consumed_at = $1 WHERE id = 1
	`, now); err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}

	authenticated, err := service.createSession(
		ctx,
		tx,
		Operator{ID: OperatorID(operatorID.Bytes), Username: command.Username.Display},
		now,
	)
	if err != nil {
		return AuthenticatedSession{}, err
	}
	if err = commit(tx, ctx); err != nil {
		return AuthenticatedSession{}, err
	}
	return authenticated, nil
}

func (service *Service) Login(
	ctx context.Context,
	command LoginCommand,
) (AuthenticatedSession, error) {
	if err := service.acquirePasswordSlot(ctx); err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	defer service.releasePasswordSlot()

	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := service.now()
	var operatorID pgtype.UUID
	var username string
	var passwordHash string
	err = tx.QueryRow(ctx, `
		SELECT id, username, password_hash
		FROM operators
		WHERE username_key = $1 AND disabled_at IS NULL
	`, command.Username.Key).Scan(&operatorID, &username, &passwordHash)
	operatorFound := err == nil && operatorID.Valid
	if errors.Is(err, pgx.ErrNoRows) {
		passwordHash = dummyPasswordHash
	} else if err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	password := ""
	if command.Password != nil {
		password = command.Password.Reveal()
	}
	passwordValid := service.verifyPassword(password, passwordHash)
	if !operatorFound || !passwordValid {
		return AuthenticatedSession{}, ErrAuthentication
	}

	authenticated, err := service.createSession(
		ctx,
		tx,
		Operator{ID: OperatorID(operatorID.Bytes), Username: username},
		now,
	)
	if err != nil {
		return AuthenticatedSession{}, err
	}
	if err = commit(tx, ctx); err != nil {
		return AuthenticatedSession{}, err
	}
	return authenticated, nil
}

func (service *Service) Authenticate(
	ctx context.Context,
	token SessionToken,
) (OperatorSession, error) {
	if token.value == "" {
		return OperatorSession{}, ErrAuthentication
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OperatorSession{}, ErrServiceUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := service.now()
	digest := DigestToken(token.value)
	var sessionID pgtype.UUID
	var operatorID pgtype.UUID
	var username string
	var createdAt time.Time
	var lastSeenAt time.Time
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT
			s.id, s.created_at, s.last_seen_at, s.expires_at,
			o.id, o.username
		FROM operator_sessions AS s
		JOIN operators AS o ON o.id = s.operator_id
		WHERE s.token_digest = $1
		  AND s.revoked_at IS NULL
		  AND s.expires_at > $2
		  AND o.disabled_at IS NULL
	`, digest[:], now).Scan(
		&sessionID,
		&createdAt,
		&lastSeenAt,
		&expiresAt,
		&operatorID,
		&username,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OperatorSession{}, ErrAuthentication
	}
	if err != nil || !sessionID.Valid || !operatorID.Valid {
		return OperatorSession{}, ErrServiceUnavailable
	}
	if _, err = tx.Exec(ctx, `
		UPDATE operator_sessions SET last_seen_at = $1 WHERE id = $2
	`, now, sessionID); err != nil {
		return OperatorSession{}, ErrServiceUnavailable
	}
	if err = commit(tx, ctx); err != nil {
		return OperatorSession{}, err
	}
	return OperatorSession{
		ID:         SessionID(sessionID.Bytes),
		Operator:   Operator{ID: OperatorID(operatorID.Bytes), Username: username},
		CreatedAt:  createdAt,
		LastSeenAt: now,
		ExpiresAt:  expiresAt,
	}, nil
}

func (service *Service) Logout(ctx context.Context, sessionID SessionID) error {
	_, err := service.pool.Exec(ctx, `
		UPDATE operator_sessions
		SET revoked_at = $1
		WHERE id = $2 AND revoked_at IS NULL
	`, service.now(), pgtype.UUID{Bytes: [16]byte(sessionID), Valid: true})
	if err != nil {
		return ErrServiceUnavailable
	}
	return nil
}

func (service *Service) createSession(
	ctx context.Context,
	tx pgx.Tx,
	operator Operator,
	now time.Time,
) (AuthenticatedSession, error) {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	token, err := NewSessionToken(base64.RawURLEncoding.EncodeToString(random[:]))
	clear(random[:])
	if err != nil {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	expiresAt := now.Add(service.sessionTTL)
	digest := DigestToken(token.value)
	var sessionID pgtype.UUID
	if err = tx.QueryRow(ctx, `
		INSERT INTO operator_sessions (
			operator_id, token_digest, created_at, last_seen_at, expires_at
		) VALUES ($1, $2, $3, $3, $4)
		RETURNING id
	`, pgtype.UUID{Bytes: [16]byte(operator.ID), Valid: true}, digest[:], now, expiresAt).Scan(&sessionID); err != nil || !sessionID.Valid {
		return AuthenticatedSession{}, ErrServiceUnavailable
	}
	record := OperatorSession{
		ID:         SessionID(sessionID.Bytes),
		Operator:   operator,
		CreatedAt:  now,
		LastSeenAt: now,
		ExpiresAt:  expiresAt,
	}
	return AuthenticatedSession{
		Session:   record,
		Token:     token,
		CSRFToken: CSRFTokenFor(token, record.ID),
	}, nil
}

func (service *Service) acquirePasswordSlot(ctx context.Context) error {
	select {
	case service.passwordSlots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (service *Service) releasePasswordSlot() {
	<-service.passwordSlots
}

func (service *Service) now() time.Time {
	return service.clock().UTC().Truncate(time.Microsecond)
}

func commit(tx pgx.Tx, ctx context.Context) error {
	if err := tx.Commit(ctx); err != nil {
		return ErrServiceUnavailable
	}
	return nil
}

var _ SessionService = (*Service)(nil)
