package credentials

import (
	"context"
	"errors"

	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SecretReader struct {
	pool  *pgxpool.Pool
	vault *security.CredentialVault
}

func NewSecretReader(pool *pgxpool.Pool, vault *security.CredentialVault) (*SecretReader, error) {
	if pool == nil || vault == nil {
		return nil, errors.New("credential reader dependencies are incomplete")
	}
	return &SecretReader{pool: pool, vault: vault}, nil
}

func (reader *SecretReader) Read(
	ctx context.Context,
	id ID,
	expectedKind Kind,
	expectedSecretVersion int32,
) (*security.SecretValue, error) {
	if expectedSecretVersion <= 0 {
		return nil, errors.New("expected_secret_version must be positive")
	}
	var storedKind string
	var storedVersion int32
	var keyID string
	var nonce, ciphertext []byte
	err := reader.pool.QueryRow(ctx, `
		SELECT kind, secret_version, key_id, nonce, ciphertext
		FROM credentials
		WHERE id = $1
		  AND kind = $2
		  AND secret_version = $3
		  AND deleted_at IS NULL
	`, pgUUID(id), string(expectedKind), expectedSecretVersion).Scan(
		&storedKind, &storedVersion, &keyID, &nonce, &ciphertext,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrSecretUnavailable
		}
		return nil, err
	}
	if Kind(storedKind) != expectedKind || storedVersion != expectedSecretVersion {
		return nil, ErrSecretUnavailable
	}
	envelope, err := security.NewCredentialEnvelope(
		security.CredentialID(id), expectedKind, int64(expectedSecretVersion),
		keyID, nonce, ciphertext,
	)
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	secret, err := reader.vault.Decrypt(envelope)
	if err != nil {
		return nil, ErrSecretUnavailable
	}
	return secret, nil
}
