package idempotency

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

var ErrConflict = errors.New("request key was already used for different content")

type Digest [32]byte

type Request struct {
	Scope           string
	Key             string
	Operation       string
	Digest          Digest
	AcceptedDigests []Digest
	TTL             time.Duration
}

type Result struct {
	Type string
	ID   [16]byte
}

type Operation func(context.Context, pgx.Tx) (Result, error)

// Execute serializes one scope/key pair and records the operation result in the
// caller's transaction. The callback and record insertion either commit or
// roll back together when the caller completes tx.
func Execute(ctx context.Context, tx pgx.Tx, request Request, operation Operation) (Result, error) {
	if request.Scope == "" || request.Key == "" || request.Operation == "" {
		return Result{}, errors.New("idempotency scope, key, and operation are required")
	}
	if request.TTL.Microseconds() <= 0 {
		return Result{}, errors.New("idempotency TTL must be positive")
	}
	if tx == nil || operation == nil {
		return Result{}, errors.New("idempotency dependencies are incomplete")
	}

	lockKey := fmt.Sprintf("%d:%s%s", utf8.RuneCountInString(request.Scope), request.Scope, request.Key)
	if _, err := tx.Exec(ctx,
		"SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
		lockKey,
	); err != nil {
		return Result{}, err
	}

	var currentTime pgtype.Timestamptz
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&currentTime); err != nil || !currentTime.Valid {
		if err == nil {
			err = errors.New("database clock did not return a timestamp")
		}
		return Result{}, err
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM idempotency_records
		WHERE scope = $1 AND request_key = $2 AND expires_at <= $3
	`, request.Scope, request.Key, currentTime); err != nil {
		return Result{}, err
	}

	var storedOperation, resultType string
	var storedDigest []byte
	var resultID pgtype.UUID
	err := tx.QueryRow(ctx, `
		SELECT operation, request_digest, result_type, result_id
		FROM idempotency_records
		WHERE scope = $1 AND request_key = $2
	`, request.Scope, request.Key).Scan(&storedOperation, &storedDigest, &resultType, &resultID)
	if err == nil {
		if storedOperation != request.Operation || !digestAccepted(storedDigest, request) {
			return Result{}, ErrConflict
		}
		return Result{Type: resultType, ID: resultID.Bytes}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Result{}, err
	}

	result, err := operation(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	if result.Type == "" {
		return Result{}, errors.New("idempotency result type is required")
	}
	resultUUID := pgtype.UUID{Bytes: result.ID, Valid: true}
	if _, err := tx.Exec(ctx, `
		INSERT INTO idempotency_records (
			scope, request_key, operation, request_digest,
			result_type, result_id, created_at, expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7,
			$7::timestamptz + $8::bigint * interval '1 microsecond'
		)
	`, request.Scope, request.Key, request.Operation, request.Digest[:], result.Type,
		resultUUID, currentTime, request.TTL.Microseconds()); err != nil {
		return Result{}, err
	}
	return result, nil
}

func digestAccepted(stored []byte, request Request) bool {
	accepted := subtle.ConstantTimeCompare(stored, request.Digest[:])
	for index := range request.AcceptedDigests {
		accepted |= subtle.ConstantTimeCompare(stored, request.AcceptedDigests[index][:])
	}
	return accepted == 1
}
