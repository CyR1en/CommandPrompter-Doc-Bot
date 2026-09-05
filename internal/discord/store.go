package discord

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type ConnectionID [16]byte

func (id ConnectionID) String() string { return jobs.UUID(id).String() }

type GatewayConfig struct {
	ID                ConnectionID
	ConnectionVersion int32
	CredentialID      credentials.ID
	CredentialVersion int32
	token             *security.SecretValue
}

func (config GatewayConfig) Token() *security.SecretValue { return config.token }

func (config GatewayConfig) Capture() GatewayCapture {
	return GatewayCapture{
		ConnectionID: config.ID, ConnectionVersion: config.ConnectionVersion,
		CredentialID: config.CredentialID, CredentialVersion: config.CredentialVersion,
	}
}

type GatewayCapture struct {
	ConnectionID      ConnectionID
	ConnectionVersion int32
	CredentialID      credentials.ID
	CredentialVersion int32
}

type Store struct {
	pool                *pgxpool.Pool
	vault               *security.CredentialVault
	contextOptions      ContextOptions
	deliveryRESTFactory DeliveryRESTFactory
}

func NewStore(pool *pgxpool.Pool, vault *security.CredentialVault) (*Store, error) {
	return NewStoreWithOptions(pool, vault, StoreOptions{})
}

type StoreOptions struct {
	Context             ContextOptions
	DeliveryRESTFactory DeliveryRESTFactory
}

func NewStoreWithOptions(pool *pgxpool.Pool, vault *security.CredentialVault, options StoreOptions) (*Store, error) {
	if pool == nil || vault == nil {
		return nil, errors.New("Discord store dependencies are incomplete")
	}
	contextOptions, err := normalizeContextOptions(options.Context)
	if err != nil {
		return nil, err
	}
	if options.DeliveryRESTFactory == nil {
		options.DeliveryRESTFactory = func() (DeliveryREST, error) {
			return NewRESTClient(RESTOptions{})
		}
	}
	return &Store{
		pool: pool, vault: vault, contextOptions: contextOptions,
		deliveryRESTFactory: options.DeliveryRESTFactory,
	}, nil
}

func (store *Store) EnabledConnections(ctx context.Context) ([]GatewayConfig, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT dc.id, dc.version, dc.credential_version,
		       c.id, c.kind, c.secret_version, c.key_id, c.nonce, c.ciphertext,
		       c.deleted_at
		FROM discord_connections AS dc
		JOIN credentials AS c ON c.id = dc.credential_id
		WHERE dc.lifecycle = 'ENABLED'
		ORDER BY dc.id
	`)
	if err != nil {
		return nil, err
	}
	type encryptedConfig struct {
		connectionID, credentialID                        pgtype.UUID
		connectionVersion, capturedVersion, secretVersion int32
		kind, keyID                                       string
		nonce, ciphertext                                 []byte
		deletedAt                                         pgtype.Timestamptz
	}
	encrypted := []encryptedConfig{}
	for rows.Next() {
		var value encryptedConfig
		if err := rows.Scan(
			&value.connectionID, &value.connectionVersion, &value.capturedVersion, &value.credentialID, &value.kind,
			&value.secretVersion, &value.keyID, &value.nonce, &value.ciphertext,
			&value.deletedAt,
		); err != nil {
			rows.Close()
			return nil, err
		}
		encrypted = append(encrypted, value)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	configs := make([]GatewayConfig, 0, len(encrypted))
	for _, value := range encrypted {
		id := ConnectionID(value.connectionID.Bytes)
		capture := GatewayCapture{
			ConnectionID: id, ConnectionVersion: value.connectionVersion,
			CredentialID: credentials.ID(value.credentialID.Bytes), CredentialVersion: value.capturedVersion,
		}
		if value.deletedAt.Valid || value.kind != string(security.CredentialDiscordBotToken) || value.capturedVersion != value.secretVersion {
			_ = store.Degraded(ctx, capture, "Discord credential is unavailable.")
			continue
		}
		envelope, err := security.NewCredentialEnvelope(
			security.CredentialID(value.credentialID.Bytes), security.CredentialDiscordBotToken,
			int64(value.secretVersion), value.keyID, value.nonce, value.ciphertext,
		)
		if err != nil {
			_ = store.Degraded(ctx, capture, "Discord credential is unavailable.")
			continue
		}
		secret, err := store.vault.Decrypt(envelope)
		if err != nil {
			_ = store.Degraded(ctx, capture, "Discord credential is unavailable.")
			continue
		}
		configs = append(configs, GatewayConfig{
			ID: id, ConnectionVersion: value.connectionVersion, CredentialID: credentials.ID(value.credentialID.Bytes),
			CredentialVersion: value.secretVersion, token: secret,
		})
	}
	return configs, nil
}

type Ownership struct {
	connection *pgxpool.Conn
	key        string
	owned      bool
}

type OwnershipLease interface {
	Owned() bool
	Close(context.Context) error
}

func (store *Store) AcquireOwnership(ctx context.Context, id ConnectionID) (OwnershipLease, error) {
	connection, err := store.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	key := "discord-gateway:" + id.String()
	var owned bool
	if err := connection.QueryRow(ctx,
		"SELECT pg_try_advisory_lock(hashtextextended($1, 0))", key,
	).Scan(&owned); err != nil {
		connection.Release()
		return nil, err
	}
	return &Ownership{connection: connection, key: key, owned: owned}, nil
}

func (ownership *Ownership) Owned() bool { return ownership != nil && ownership.owned }

func (ownership *Ownership) Close(ctx context.Context) error {
	if ownership == nil || ownership.connection == nil {
		return nil
	}
	connection := ownership.connection
	ownership.connection = nil
	defer connection.Release()
	if !ownership.owned {
		return nil
	}
	var released bool
	err := connection.QueryRow(ctx,
		"SELECT pg_advisory_unlock(hashtextextended($1, 0))", ownership.key,
	).Scan(&released)
	if err != nil {
		connection.Conn().Close(context.Background())
		return err
	}
	if !released {
		return errors.New("Discord gateway ownership lock was not held")
	}
	return nil
}

func (store *Store) Connecting(ctx context.Context, capture GatewayCapture) error {
	return store.state(ctx, capture, "CONNECTING", nil, nil, false, false)
}

func (store *Store) Ready(ctx context.Context, capture GatewayCapture, latency time.Duration) error {
	milliseconds := nonnegativeMilliseconds(latency)
	return store.state(ctx, capture, "READY", nil, &milliseconds, true, false)
}

func (store *Store) EventReceived(ctx context.Context, capture GatewayCapture, latency time.Duration) error {
	milliseconds := nonnegativeMilliseconds(latency)
	return store.state(ctx, capture, "READY", nil, &milliseconds, false, true)
}

func (store *Store) Degraded(ctx context.Context, capture GatewayCapture, sanitizedError string) error {
	if len([]rune(sanitizedError)) > 1000 {
		sanitizedError = string([]rune(sanitizedError)[:1000])
	}
	return store.state(ctx, capture, "DEGRADED", &sanitizedError, nil, false, false)
}

func (store *Store) Disabled(ctx context.Context, id ConnectionID) error {
	return store.unfencedState(ctx, id, "DISABLED", nil)
}

func (store *Store) state(
	ctx context.Context,
	capture GatewayCapture,
	state string,
	sanitizedError *string,
	latency *int32,
	heartbeat, event bool,
) error {
	if capture.ConnectionID == (ConnectionID{}) || capture.ConnectionVersion <= 0 ||
		capture.CredentialID == (credentials.ID{}) || capture.CredentialVersion <= 0 {
		return ErrConflict
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var previousState string
	var previousError *string
	var connectionVersion, credentialVersion int32
	var credentialID pgtype.UUID
	if err := tx.QueryRow(ctx, `
		SELECT state, sanitized_error, version, credential_id, credential_version
		FROM discord_connections
		WHERE id = $1
		FOR UPDATE
	`, pgtype.UUID{Bytes: [16]byte(capture.ConnectionID), Valid: true}).Scan(
		&previousState, &previousError, &connectionVersion, &credentialID, &credentialVersion,
	); err != nil {
		return err
	}
	if !credentialID.Valid || connectionVersion != capture.ConnectionVersion ||
		credentials.ID(credentialID.Bytes) != capture.CredentialID || credentialVersion != capture.CredentialVersion {
		return fmt.Errorf("%w: Discord gateway capture is stale", ErrConflict)
	}
	assignments := `state=$2, sanitized_error=$3, updated_at=clock_timestamp()`
	arguments := []any{pgtype.UUID{Bytes: [16]byte(capture.ConnectionID), Valid: true}, state, sanitizedError}
	if latency != nil {
		assignments += fmt.Sprintf(", gateway_latency_ms=$%d", len(arguments)+1)
		arguments = append(arguments, *latency)
	}
	if heartbeat {
		assignments += ", last_heartbeat_at=clock_timestamp()"
	}
	if event {
		assignments += ", last_event_at=clock_timestamp()"
	}
	if _, err := tx.Exec(ctx, "UPDATE discord_connections SET "+assignments+" WHERE id=$1", arguments...); err != nil {
		return err
	}
	if previousState != state || !equalOptionalString(previousError, sanitizedError) {
		if err := appendStateEvent(ctx, tx, capture.ConnectionID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (store *Store) unfencedState(ctx context.Context, id ConnectionID, state string, sanitizedError *string) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var previousState string
	var previousError *string
	if err = tx.QueryRow(ctx, `
		SELECT state,sanitized_error FROM discord_connections WHERE id=$1 FOR UPDATE
	`, pgtype.UUID{Bytes: [16]byte(id), Valid: true}).Scan(&previousState, &previousError); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `
		UPDATE discord_connections SET state=$2,sanitized_error=$3,updated_at=clock_timestamp() WHERE id=$1
	`, pgtype.UUID{Bytes: [16]byte(id), Valid: true}, state, sanitizedError); err != nil {
		return err
	}
	if previousState != state || !equalOptionalString(previousError, sanitizedError) {
		if err = appendStateEvent(ctx, tx, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func appendStateEvent(ctx context.Context, tx pgx.Tx, id ConnectionID) error {
	value, err := readConnection(ctx, tx, id, false)
	if err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{
		Type: "discord.connection.state_changed", ResourceType: "discord_connection",
		ResourceID: [16]byte(id), Snapshot: connectionEventSnapshot(value),
	})
}

func nonnegativeMilliseconds(latency time.Duration) int32 {
	if latency < 0 {
		return 0
	}
	value := latency.Milliseconds()
	if value > int64(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	return int32(value)
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
