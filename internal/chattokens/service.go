// Package chattokens owns issue-once bearer credentials and their fixed Agent
// scopes. It deliberately does not own Agent readiness or execution policy.
package chattokens

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	secretPrefix      = "ref0_chat_"
	maxPageSize       = 100
	idempotencyTTL    = 24 * time.Hour
	idempotencyDomain = "chat_token.control_plane"

	// MaxAgentScopesPerToken is a bearer-token request/storage bound, not a
	// product limit on the number of Agents in the catalog.
	MaxAgentScopesPerToken = 2048
)

var (
	ErrInvalid             = errors.New("chat token request is invalid")
	ErrNotFound            = errors.New("chat token was not found")
	ErrConflict            = errors.New("chat token mutation conflicts with current state")
	ErrUnauthorized        = errors.New("chat token is unauthorized")
	ErrSecretAlreadyIssued = errors.New("chat token secret was already issued")
)

type ID agents.ID

func (id ID) String() string { return agents.ID(id).String() }

func ParseID(value string) (ID, error) {
	parsed, err := agents.ParseID(value)
	if err != nil {
		return ID{}, ErrInvalid
	}
	return ID(parsed), nil
}

type Token struct {
	ID                ID
	Prefix            string
	Label             string
	CreatedByOperator auth.OperatorID
	AgentIDs          []agents.AgentID
	CreatedAt         time.Time
	ExpiresAt         time.Time
	RevokedAt         *time.Time
	LastUsedAt        *time.Time
}

// Summary is the bounded ledger representation used by list and revoke
// control-plane responses. Agent identities and expanded catalog metadata are
// intentionally omitted so ledger reads remain constant-size per token.
type Summary struct {
	ID                ID
	Prefix            string
	Label             string
	CreatedByOperator auth.OperatorID
	AgentCount        int
	CreatedAt         time.Time
	ExpiresAt         time.Time
	RevokedAt         *time.Time
	LastUsedAt        *time.Time
}

type CreateCommand struct {
	Label     string
	AgentIDs  []agents.AgentID
	ExpiresAt time.Time
}

type Issued struct {
	Token  Token
	Secret *security.SecretValue
}

type Grant struct {
	TokenID  ID
	Subject  string
	agentIDs []agents.AgentID
}

// NewGrant builds the immutable value returned by an authenticated token
// lookup. It is primarily useful at transport seams and keeps scope slices
// defensive; possession of this in-process value does not bypass HTTP auth.
func NewGrant(tokenID ID, agentIDs []agents.AgentID) (Grant, error) {
	if zeroID(tokenID) || len(agentIDs) == 0 {
		return Grant{}, ErrUnauthorized
	}
	copyIDs := append([]agents.AgentID(nil), agentIDs...)
	seen := make(map[agents.AgentID]struct{}, len(copyIDs))
	for _, agentID := range copyIDs {
		if agentID == (agents.AgentID{}) {
			return Grant{}, ErrUnauthorized
		}
		if _, exists := seen[agentID]; exists {
			return Grant{}, ErrUnauthorized
		}
		seen[agentID] = struct{}{}
	}
	return Grant{TokenID: tokenID, Subject: "chat-token:" + tokenID.String(), agentIDs: copyIDs}, nil
}

func (grant Grant) AgentIDs() []agents.AgentID {
	return append([]agents.AgentID(nil), grant.agentIDs...)
}

func (grant Grant) Allows(agentID agents.AgentID) bool {
	for _, allowed := range grant.agentIDs {
		if allowed == agentID {
			return true
		}
	}
	return false
}

type PageCursor struct {
	CreatedAt time.Time
	TokenID   ID
}

type Page struct {
	Summaries  []Summary
	NextCursor *PageCursor
}

type Service struct {
	pool  *pgxpool.Pool
	vault *security.CredentialVault
}

func NewService(pool *pgxpool.Pool, vault *security.CredentialVault) (*Service, error) {
	if pool == nil || vault == nil {
		return nil, errors.New("chat token dependencies are incomplete")
	}
	return &Service{pool: pool, vault: vault}, nil
}

func (service *Service) List(ctx context.Context, after *PageCursor, limit int) (Page, error) {
	if limit < 1 || limit > maxPageSize {
		return Page{}, ErrInvalid
	}
	tx, err := service.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Page{}, err
	}
	defer tx.Rollback(ctx)
	query := `
		WITH token_page AS MATERIALIZED (
			SELECT token.id,token.token_prefix,token.label,token.created_by_operator_id,
			       token.created_at,token.expires_at,token.revoked_at,token.last_used_at
			FROM chat_access_tokens token`
	args := make([]any, 0, 3)
	if after != nil {
		if after.CreatedAt.IsZero() || zeroID(after.TokenID) {
			return Page{}, ErrInvalid
		}
		query += ` WHERE (token.created_at,token.id)<($1,$2)`
		args = append(args, after.CreatedAt, pgUUID(after.TokenID))
	}
	query += fmt.Sprintf(`
			ORDER BY token.created_at DESC,token.id DESC
			LIMIT $%d
		)
		SELECT token_page.id,token_page.token_prefix,token_page.label,token_page.created_by_operator_id,
		       token_page.created_at,token_page.expires_at,token_page.revoked_at,token_page.last_used_at,
		       count(scope.agent_id)
		FROM token_page
		LEFT JOIN chat_access_token_agents scope ON scope.token_id=token_page.id
		GROUP BY token_page.id,token_page.token_prefix,token_page.label,token_page.created_by_operator_id,
		         token_page.created_at,token_page.expires_at,token_page.revoked_at,token_page.last_used_at
		ORDER BY token_page.created_at DESC,token_page.id DESC`, len(args)+1)
	args = append(args, limit+1)
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return Page{}, err
	}
	summaries := make([]Summary, 0, limit+1)
	for rows.Next() {
		var summary Summary
		var id, actorID pgtype.UUID
		var revokedAt, lastUsedAt pgtype.Timestamptz
		var agentCount int64
		if err = rows.Scan(
			&id, &summary.Prefix, &summary.Label, &actorID,
			&summary.CreatedAt, &summary.ExpiresAt, &revokedAt, &lastUsedAt, &agentCount,
		); err != nil {
			rows.Close()
			return Page{}, err
		}
		summary.ID = ID(id.Bytes)
		summary.CreatedByOperator = auth.OperatorID(actorID.Bytes)
		summary.RevokedAt = optionalTime(revokedAt)
		summary.LastUsedAt = optionalTime(lastUsedAt)
		if agentCount < 1 || agentCount > MaxAgentScopesPerToken {
			rows.Close()
			return Page{}, errors.New("stored chat token scope count is invalid")
		}
		summary.AgentCount = int(agentCount)
		if err = validateSummary(summary); err != nil {
			rows.Close()
			return Page{}, err
		}
		summaries = append(summaries, summary)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return Page{}, err
	}
	rows.Close()
	more := len(summaries) > limit
	if more {
		summaries = summaries[:limit]
	}
	page := Page{Summaries: summaries}
	if more {
		last := summaries[len(summaries)-1]
		page.NextCursor = &PageCursor{CreatedAt: last.CreatedAt, TokenID: last.ID}
	}
	if err = tx.Commit(ctx); err != nil {
		return Page{}, err
	}
	return page, nil
}

func (service *Service) Create(
	ctx context.Context,
	command CreateCommand,
	actor auth.OperatorID,
	requestKey string,
) (Issued, error) {
	command.Label = strings.TrimFunc(command.Label, pythonWhitespace)
	if err := validateCreate(command); err != nil {
		return Issued{}, err
	}
	command.AgentIDs = append([]agents.AgentID(nil), command.AgentIDs...)
	sort.Slice(command.AgentIDs, func(left, right int) bool {
		return command.AgentIDs[left].String() < command.AgentIDs[right].String()
	})
	request, err := service.request(actor, requestKey, "chat_token.create", map[string]any{
		"label": command.Label, "expires_at": command.ExpiresAt.UTC().Format(time.RFC3339Nano),
		"agent_ids": stringAgentIDs(command.AgentIDs),
	})
	if err != nil {
		return Issued{}, err
	}
	var issued *security.SecretValue
	value, err := withTx(ctx, service.pool, func(tx pgx.Tx) (Token, error) {
		created := false
		result, executeErr := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			var databaseNow time.Time
			if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
				return idempotency.Result{}, err
			}
			if !command.ExpiresAt.After(databaseNow) {
				return idempotency.Result{}, ErrInvalid
			}
			if err := validateAgentScopes(ctx, tx, command.AgentIDs); err != nil {
				return idempotency.Result{}, err
			}
			id, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			secret, prefix, digest, err := generate()
			if err != nil {
				return idempotency.Result{}, err
			}
			if _, err = tx.Exec(ctx, `
				INSERT INTO chat_access_tokens (
					id,token_digest,token_prefix,label,created_by_operator_id,created_at,expires_at
				) VALUES ($1,$2,$3,$4,$5,$6,$7)
			`, pgUUID(id), digest[:], prefix, command.Label, pgtype.UUID{Bytes: [16]byte(actor), Valid: true}, databaseNow, command.ExpiresAt); err != nil {
				return idempotency.Result{}, err
			}
			for _, agentID := range command.AgentIDs {
				if _, err = tx.Exec(ctx, `
					INSERT INTO chat_access_token_agents (token_id,agent_id) VALUES ($1,$2)
				`, pgUUID(id), pgtype.UUID{Bytes: [16]byte(agentID), Valid: true}); err != nil {
					return idempotency.Result{}, err
				}
			}
			value, err := load(ctx, tx, id, false)
			if err != nil {
				return idempotency.Result{}, err
			}
			if err = recordChange(ctx, tx, value, actor, "chat_token.create", "chat_token.created"); err != nil {
				return idempotency.Result{}, err
			}
			issued = secret
			created = true
			return idempotency.Result{Type: "chat_token:issued", ID: [16]byte(id)}, nil
		})
		if executeErr != nil {
			return Token{}, executeErr
		}
		if result.Type != "chat_token:issued" {
			return Token{}, idempotency.ErrConflict
		}
		loaded, loadErr := load(ctx, tx, ID(result.ID), false)
		if loadErr != nil {
			return Token{}, loadErr
		}
		if !created {
			return loaded, ErrSecretAlreadyIssued
		}
		return loaded, nil
	})
	return Issued{Token: value, Secret: issued}, err
}

func (service *Service) Revoke(
	ctx context.Context,
	id ID,
	actor auth.OperatorID,
	requestKey string,
) (Summary, error) {
	if zeroID(id) {
		return Summary{}, ErrInvalid
	}
	request, err := service.request(actor, requestKey, "chat_token.revoke", map[string]any{"token_id": id.String()})
	if err != nil {
		return Summary{}, err
	}
	return withTx(ctx, service.pool, func(tx pgx.Tx) (Summary, error) {
		result, executeErr := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			current, err := load(ctx, tx, id, true)
			if err != nil {
				return idempotency.Result{}, err
			}
			if current.RevokedAt != nil {
				return idempotency.Result{}, ErrConflict
			}
			if _, err = tx.Exec(ctx, `
				UPDATE chat_access_tokens SET revoked_at=clock_timestamp() WHERE id=$1 AND revoked_at IS NULL
			`, pgUUID(id)); err != nil {
				return idempotency.Result{}, err
			}
			value, err := load(ctx, tx, id, false)
			if err != nil {
				return idempotency.Result{}, err
			}
			if err = recordChange(ctx, tx, value, actor, "chat_token.revoke", "chat_token.revoked"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "chat_token:revoked", ID: [16]byte(id)}, nil
		})
		if executeErr != nil {
			return Summary{}, executeErr
		}
		if result.Type != "chat_token:revoked" || ID(result.ID) != id {
			return Summary{}, idempotency.ErrConflict
		}
		value, loadErr := load(ctx, tx, id, false)
		if loadErr != nil {
			return Summary{}, loadErr
		}
		return summaryFromToken(value)
	})
}

func summaryFromToken(value Token) (Summary, error) {
	summary := Summary{
		ID: value.ID, Prefix: value.Prefix, Label: value.Label,
		CreatedByOperator: value.CreatedByOperator, AgentCount: len(value.AgentIDs),
		CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt,
		RevokedAt: value.RevokedAt, LastUsedAt: value.LastUsedAt,
	}
	if err := validateSummary(summary); err != nil {
		return Summary{}, err
	}
	return summary, nil
}

func validateSummary(value Summary) error {
	if zeroID(value.ID) || value.CreatedByOperator == (auth.OperatorID{}) || value.Prefix == "" ||
		value.Label == "" || value.AgentCount < 1 || value.AgentCount > MaxAgentScopesPerToken ||
		value.CreatedAt.IsZero() || !value.ExpiresAt.After(value.CreatedAt) {
		return errors.New("stored chat token summary is invalid")
	}
	return nil
}

func (service *Service) Authenticate(ctx context.Context, plaintext string) (Grant, error) {
	if !validSecret(plaintext) {
		return Grant{}, ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(plaintext))
	return withTx(ctx, service.pool, func(tx pgx.Tx) (Grant, error) {
		var id pgtype.UUID
		var expiresAt time.Time
		var revokedAt pgtype.Timestamptz
		err := tx.QueryRow(ctx, `
			SELECT id,expires_at,revoked_at FROM chat_access_tokens
			WHERE token_digest=$1
			FOR UPDATE
		`, digest[:]).Scan(&id, &expiresAt, &revokedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return Grant{}, ErrUnauthorized
		}
		if err != nil {
			return Grant{}, err
		}
		var databaseNow time.Time
		if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
			return Grant{}, err
		}
		if revokedAt.Valid || !expiresAt.After(databaseNow) {
			return Grant{}, ErrUnauthorized
		}
		if _, err = tx.Exec(ctx, `
			UPDATE chat_access_tokens
			SET last_used_at=GREATEST(COALESCE(last_used_at,created_at),$2)
			WHERE id=$1
		`, id, databaseNow); err != nil {
			return Grant{}, err
		}
		agentIDs, err := loadScopes(ctx, tx, ID(id.Bytes))
		if err != nil || len(agentIDs) == 0 {
			if err == nil {
				err = errors.New("stored chat token has no Agent scope")
			}
			return Grant{}, err
		}
		return NewGrant(ID(id.Bytes), agentIDs)
	})
}

func (service *Service) request(
	actor auth.OperatorID,
	key string,
	operation string,
	payload map[string]any,
) (idempotency.Request, error) {
	if actor == (auth.OperatorID{}) || key == "" || key != strings.TrimFunc(key, pythonWhitespace) ||
		!utf8.ValidString(key) || utf8.RuneCountInString(key) > 255 || strings.ContainsAny(key, "\x00\r\n") {
		return idempotency.Request{}, ErrInvalid
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return idempotency.Request{}, ErrInvalid
	}
	digests, err := service.vault.KeyedDigests([]byte(idempotencyDomain), []byte(operation), canonical)
	if err != nil || len(digests) == 0 {
		return idempotency.Request{}, errors.New("chat token idempotency digest is unavailable")
	}
	request := idempotency.Request{
		Scope: "operator:" + actor.String(), Key: key, Operation: operation, TTL: idempotencyTTL,
	}
	copy(request.Digest[:], digests[0])
	for _, raw := range digests[1:] {
		var digest idempotency.Digest
		copy(digest[:], raw)
		request.AcceptedDigests = append(request.AcceptedDigests, digest)
	}
	return request, nil
}

func validateCreate(command CreateCommand) error {
	if !utf8.ValidString(command.Label) || utf8.RuneCountInString(command.Label) < 1 ||
		utf8.RuneCountInString(command.Label) > 255 || command.ExpiresAt.IsZero() ||
		len(command.AgentIDs) < 1 || len(command.AgentIDs) > MaxAgentScopesPerToken {
		return ErrInvalid
	}
	seen := make(map[agents.AgentID]struct{}, len(command.AgentIDs))
	for _, id := range command.AgentIDs {
		if id == (agents.AgentID{}) {
			return ErrInvalid
		}
		if _, exists := seen[id]; exists {
			return ErrInvalid
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateAgentScopes(ctx context.Context, tx pgx.Tx, ids []agents.AgentID) error {
	var count int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM agents WHERE id=ANY($1::uuid[])
	`, agentUUIDs(ids)).Scan(&count); err != nil {
		return err
	}
	if count != len(ids) {
		return ErrNotFound
	}
	return nil
}

func generate() (*security.SecretValue, string, [32]byte, error) {
	var entropy [32]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return nil, "", [32]byte{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(entropy[:])
	plaintext := secretPrefix + encoded
	secret, err := security.NewSecretValue(plaintext)
	if err != nil {
		return nil, "", [32]byte{}, err
	}
	return secret, secretPrefix + encoded[:8], sha256.Sum256([]byte(plaintext)), nil
}

func validSecret(value string) bool {
	if !strings.HasPrefix(value, secretPrefix) {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(value, secretPrefix))
	return err == nil && len(decoded) == 32
}

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func load(ctx context.Context, database queryer, id ID, lock bool) (Token, error) {
	var value Token
	var storedID, actorID pgtype.UUID
	var revokedAt, lastUsedAt pgtype.Timestamptz
	query := `
		SELECT id,token_prefix,label,created_by_operator_id,created_at,expires_at,revoked_at,last_used_at
		FROM chat_access_tokens WHERE id=$1`
	if lock {
		query += ` FOR UPDATE`
	}
	err := database.QueryRow(ctx, query, pgUUID(id)).Scan(
		&storedID, &value.Prefix, &value.Label, &actorID, &value.CreatedAt, &value.ExpiresAt, &revokedAt, &lastUsedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, err
	}
	value.ID = ID(storedID.Bytes)
	value.CreatedByOperator = auth.OperatorID(actorID.Bytes)
	value.RevokedAt = optionalTime(revokedAt)
	value.LastUsedAt = optionalTime(lastUsedAt)
	value.AgentIDs, err = loadScopes(ctx, database, value.ID)
	if err != nil {
		return Token{}, err
	}
	if zeroID(value.ID) || value.Prefix == "" || value.Label == "" || len(value.AgentIDs) == 0 ||
		!value.ExpiresAt.After(value.CreatedAt) {
		return Token{}, errors.New("stored chat token is invalid")
	}
	return value, nil
}

func loadScopes(ctx context.Context, database queryer, id ID) ([]agents.AgentID, error) {
	rows, err := database.Query(ctx, `
		SELECT scope.agent_id FROM chat_access_token_agents scope
		WHERE scope.token_id=$1 ORDER BY scope.agent_id
	`, pgUUID(id))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]agents.AgentID, 0)
	for rows.Next() {
		var id pgtype.UUID
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, agents.AgentID(id.Bytes))
	}
	return result, rows.Err()
}

func recordChange(
	ctx context.Context,
	tx pgx.Tx,
	value Token,
	actor auth.OperatorID,
	action string,
	eventType string,
) error {
	requestID, err := newUUID()
	if err != nil {
		return err
	}
	actorID := [16]byte(actor)
	resourceID := [16]byte(value.ID)
	snapshot := tokenSnapshot(value)
	if err = events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: "operator", ActorID: &actorID, Action: action,
		TargetType: "chat_token", TargetID: &resourceID, RequestID: requestID, Details: snapshot,
	}); err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{
		Type: eventType, ResourceType: "chat_token", ResourceID: resourceID, Snapshot: snapshot,
	})
}

func tokenSnapshot(value Token) map[string]any {
	return map[string]any{
		"id": value.ID.String(), "prefix": value.Prefix, "label": value.Label,
		"agent_ids":  stringAgentIDs(value.AgentIDs),
		"created_at": value.CreatedAt.Format(time.RFC3339Nano), "expires_at": value.ExpiresAt.Format(time.RFC3339Nano),
		"revoked_at": formattedTime(value.RevokedAt), "last_used_at": formattedTime(value.LastUsedAt),
	}
}

func stringAgentIDs(ids []agents.AgentID) []string {
	result := make([]string, len(ids))
	for index, id := range ids {
		result[index] = id.String()
	}
	return result
}

func agentUUIDs(ids []agents.AgentID) []pgtype.UUID {
	result := make([]pgtype.UUID, len(ids))
	for index, id := range ids {
		result[index] = pgtype.UUID{Bytes: [16]byte(id), Valid: true}
	}
	return result
}

func pgUUID(id ID) pgtype.UUID { return pgtype.UUID{Bytes: [16]byte(id), Valid: true} }

func zeroID(id ID) bool { return id == (ID{}) }

func optionalTime(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func formattedTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}

func pythonWhitespace(character rune) bool {
	return unicode.IsSpace(character) || character >= '\x1c' && character <= '\x1f'
}

func newUUID() (ID, error) {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("generate chat token ID: %w", err)
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}

func withTx[T any](
	ctx context.Context,
	pool *pgxpool.Pool,
	operation func(pgx.Tx) (T, error),
) (value T, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return value, err
	}
	defer tx.Rollback(ctx)
	value, err = operation(tx)
	if err != nil {
		return value, err
	}
	return value, tx.Commit(ctx)
}
