package agents

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/events"
	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const catalogIdempotencyTTL = 24 * time.Hour

type Catalog struct {
	*Store
	vault *security.CredentialVault
}

func NewCatalog(pool *pgxpool.Pool, vault *security.CredentialVault) (*Catalog, error) {
	store, err := NewStore(pool)
	if err != nil {
		return nil, err
	}
	if vault == nil {
		return nil, errors.New("agent catalog digest vault is required")
	}
	return &Catalog{Store: store, vault: vault}, nil
}

func (catalog *Catalog) Create(
	ctx context.Context,
	command CreateCommand,
	actor auth.OperatorID,
	requestKey string,
) (Agent, error) {
	key, err := ParseKey(command.Key)
	if err != nil {
		return Agent{}, err
	}
	configuration, err := NormalizeConfiguration(command.Configuration)
	if err != nil {
		return Agent{}, err
	}
	command = CreateCommand{Key: key, Configuration: configuration}
	if err = ValidateCreate(command); err != nil {
		return Agent{}, err
	}
	request, err := catalog.request(actor, requestKey, "agent.create", map[string]any{
		"key": command.Key, "configuration": configurationPayload(command.Configuration),
	})
	if err != nil {
		return Agent{}, err
	}
	value, err := withTx(ctx, catalog.pool, func(tx pgx.Tx) (Agent, error) {
		result, executeErr := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			if err := validateConfigurationReferences(ctx, tx, command.Configuration); err != nil {
				return idempotency.Result{}, err
			}
			agentID, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			versionID, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			if _, err = tx.Exec(ctx, `
				INSERT INTO agents (
					id,agent_key,lifecycle,current_version_id,version,created_at,updated_at
				) VALUES ($1,$2,'DRAFT',$3,1,clock_timestamp(),clock_timestamp())
			`, pgUUID(agentID), command.Key, pgUUID(versionID)); err != nil {
				return idempotency.Result{}, uniqueAgentConflict(err)
			}
			if err = insertVersion(ctx, tx, AgentID(agentID), VersionID(versionID), 1, command.Configuration, actor); err != nil {
				return idempotency.Result{}, err
			}
			created, err := loadAgent(ctx, tx, AgentID(agentID), false)
			if err != nil {
				return idempotency.Result{}, err
			}
			if err = recordAgentChange(ctx, tx, created, actor, "agent.create", "agent.created"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "agent:1", ID: agentID}, nil
		})
		if executeErr != nil {
			return Agent{}, executeErr
		}
		return agentResult(ctx, tx, result)
	})
	return value, err
}

func (catalog *Catalog) ReplaceConfiguration(
	ctx context.Context,
	command ReplaceConfigurationCommand,
	actor auth.OperatorID,
	requestKey string,
) (Agent, error) {
	configuration, err := NormalizeConfiguration(command.Configuration)
	if err != nil {
		return Agent{}, err
	}
	command.Configuration = configuration
	if err = ValidateReplacement(command); err != nil {
		return Agent{}, err
	}
	request, err := catalog.request(actor, requestKey, "agent.configuration.replace", map[string]any{
		"agent_id": command.AgentID.String(), "expected_version": command.ExpectedVersion,
		"configuration": configurationPayload(command.Configuration),
	})
	if err != nil {
		return Agent{}, err
	}
	return withTx(ctx, catalog.pool, func(tx pgx.Tx) (Agent, error) {
		result, executeErr := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			current, err := loadAgent(ctx, tx, command.AgentID, true)
			if err != nil {
				return idempotency.Result{}, err
			}
			if current.Version != command.ExpectedVersion {
				return idempotency.Result{}, conflict("agent version is stale")
			}
			if equalConfiguration(current.CurrentVersion.Configuration, command.Configuration) {
				return idempotency.Result{}, conflict("replacement configuration has no changes")
			}
			if err = validateConfigurationReferences(ctx, tx, command.Configuration); err != nil {
				return idempotency.Result{}, err
			}
			versionID, err := newUUID()
			if err != nil {
				return idempotency.Result{}, err
			}
			versionNumber := current.CurrentVersion.VersionNumber + 1
			if err = insertVersion(ctx, tx, current.ID, VersionID(versionID), versionNumber, command.Configuration, actor); err != nil {
				return idempotency.Result{}, err
			}
			replacement, err := loadVersion(ctx, tx, VersionID(versionID))
			if err != nil {
				return idempotency.Result{}, err
			}
			if current.Lifecycle == Active {
				readiness, readinessErr := evaluateReadiness(ctx, tx, replacement)
				if readinessErr != nil {
					return idempotency.Result{}, readinessErr
				}
				if !readiness.Ready {
					return idempotency.Result{}, &readinessError{readiness: readiness}
				}
			}
			newVersion := current.Version + 1
			if _, err = tx.Exec(ctx, `
				UPDATE agents SET current_version_id=$2,version=$3,updated_at=clock_timestamp()
				WHERE id=$1
			`, pgUUID(ID(current.ID)), pgUUID(versionID), newVersion); err != nil {
				return idempotency.Result{}, err
			}
			updated, err := loadAgent(ctx, tx, current.ID, false)
			if err != nil {
				return idempotency.Result{}, err
			}
			if err = recordAgentChange(ctx, tx, updated, actor, "agent.configuration.replace", "agent.version.created"); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "agent:" + strconv.Itoa(int(newVersion)), ID: [16]byte(current.ID)}, nil
		})
		if executeErr != nil {
			return Agent{}, executeErr
		}
		return agentResult(ctx, tx, result)
	})
}

func (catalog *Catalog) SetLifecycle(
	ctx context.Context,
	command SetLifecycleCommand,
	actor auth.OperatorID,
	requestKey string,
) (Agent, error) {
	if err := ValidateLifecycle(command); err != nil {
		return Agent{}, err
	}
	request, err := catalog.request(actor, requestKey, "agent.lifecycle.set", map[string]any{
		"agent_id": command.AgentID.String(), "expected_version": command.ExpectedVersion,
		"lifecycle": command.Lifecycle,
	})
	if err != nil {
		return Agent{}, err
	}
	return withTx(ctx, catalog.pool, func(tx pgx.Tx) (Agent, error) {
		result, executeErr := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			current, err := loadAgent(ctx, tx, command.AgentID, true)
			if err != nil {
				return idempotency.Result{}, err
			}
			if current.Version != command.ExpectedVersion {
				return idempotency.Result{}, conflict("agent version is stale")
			}
			if !transitionAllowed(current.Lifecycle, command.Lifecycle) {
				return idempotency.Result{}, conflict("agent lifecycle transition is invalid")
			}
			if command.Lifecycle == Active {
				readiness, readinessErr := evaluateReadiness(ctx, tx, current.CurrentVersion)
				if readinessErr != nil {
					return idempotency.Result{}, readinessErr
				}
				if !readiness.Ready {
					return idempotency.Result{}, &readinessError{readiness: readiness}
				}
			}
			newVersion := current.Version + 1
			if _, err = tx.Exec(ctx, `
				UPDATE agents SET lifecycle=$2::varchar(16),version=$3,updated_at=clock_timestamp(),
					activated_at=CASE WHEN $2::varchar(16)='ACTIVE' THEN clock_timestamp() ELSE activated_at END,
					archived_at=CASE WHEN $2::varchar(16)='ARCHIVED' THEN clock_timestamp() ELSE NULL END
				WHERE id=$1
			`, pgUUID(ID(current.ID)), command.Lifecycle, newVersion); err != nil {
				return idempotency.Result{}, err
			}
			updated, err := loadAgent(ctx, tx, current.ID, false)
			if err != nil {
				return idempotency.Result{}, err
			}
			eventType := "agent.archived"
			if command.Lifecycle == Active {
				eventType = "agent.activated"
			}
			if err = recordAgentChange(ctx, tx, updated, actor, "agent.lifecycle.set", eventType); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "agent:" + strconv.Itoa(int(newVersion)), ID: [16]byte(current.ID)}, nil
		})
		if executeErr != nil {
			return Agent{}, executeErr
		}
		return agentResult(ctx, tx, result)
	})
}

func insertVersion(
	ctx context.Context,
	tx pgx.Tx,
	agentID AgentID,
	versionID VersionID,
	versionNumber int32,
	configuration Configuration,
	actor auth.OperatorID,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_versions (
			id,agent_id,version_number,display_name,description,response_language,
			identity_instructions,model_profile_id,reasoning_effort,answer_mode,
			behavioral_instructions,evidence_access,refusal_markdown,max_tool_calls,
			max_answer_tokens,created_by_operator_id,created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,clock_timestamp())
	`, pgUUID(ID(versionID)), pgUUID(ID(agentID)), versionNumber,
		configuration.DisplayName, configuration.Description, configuration.ResponseLanguage,
		configuration.IdentityInstructions, pgUUID(ID(configuration.ModelProfileID)),
		configuration.ReasoningEffort, configuration.AnswerMode, configuration.BehavioralInstructions,
		configuration.EvidenceAccess, configuration.RefusalMarkdown, configuration.MaxToolCalls,
		configuration.MaxAnswerTokens, pgUUID(ID(actor))); err != nil {
		return err
	}
	for position, knowledgeBaseID := range configuration.KnowledgeBaseIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO agent_version_knowledge_bases (
				agent_id,agent_version_id,position,knowledge_base_id
			) VALUES ($1,$2,$3,$4)
		`, pgUUID(ID(agentID)), pgUUID(ID(versionID)), position, pgUUID(ID(knowledgeBaseID))); err != nil {
			return err
		}
	}
	return nil
}

func validateConfigurationReferences(ctx context.Context, tx pgx.Tx, configuration Configuration) error {
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM model_profiles WHERE id=$1)
	`, pgUUID(ID(configuration.ModelProfileID))).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return notFound("model profile does not exist")
	}
	for _, knowledgeBaseID := range configuration.KnowledgeBaseIDs {
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM knowledge_bases WHERE id=$1)
		`, pgUUID(ID(knowledgeBaseID))).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return notFound("knowledge base does not exist")
		}
	}
	return nil
}

func (catalog *Catalog) request(
	actor auth.OperatorID,
	key string,
	operation string,
	payload map[string]any,
) (idempotency.Request, error) {
	if actor == (auth.OperatorID{}) {
		return idempotency.Request{}, invalid("operator is required")
	}
	if key == "" || key != strings.TrimFunc(key, pythonWhitespace) || !utf8.ValidString(key) ||
		utf8.RuneCountInString(key) > 255 || strings.ContainsAny(key, "\x00\r\n") {
		return idempotency.Request{}, invalid("idempotency key is invalid")
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return idempotency.Request{}, errors.New("agent idempotency payload is invalid")
	}
	digests, err := catalog.vault.KeyedDigests([]byte(operation), canonical)
	if err != nil || len(digests) == 0 {
		return idempotency.Request{}, errors.New("agent idempotency digest is unavailable")
	}
	request := idempotency.Request{
		Scope: "operator:" + actor.String(), Key: key, Operation: operation, TTL: catalogIdempotencyTTL,
	}
	copy(request.Digest[:], digests[0])
	for _, raw := range digests[1:] {
		var digest idempotency.Digest
		copy(digest[:], raw)
		request.AcceptedDigests = append(request.AcceptedDigests, digest)
	}
	return request, nil
}

func agentResult(ctx context.Context, tx pgx.Tx, result idempotency.Result) (Agent, error) {
	version, ok := resultVersion(result.Type)
	if !ok {
		return Agent{}, idempotency.ErrConflict
	}
	value, err := loadAgent(ctx, tx, AgentID(result.ID), false)
	if err != nil || value.Version != version {
		if err == nil || errors.Is(err, ErrNotFound) {
			return Agent{}, idempotency.ErrConflict
		}
		return Agent{}, err
	}
	return value, nil
}

func resultVersion(value string) (int32, bool) {
	prefix, raw, ok := strings.Cut(value, ":")
	if !ok || prefix != "agent" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(raw, 10, 32)
	return int32(parsed), err == nil && parsed > 0
}

func recordAgentChange(
	ctx context.Context,
	tx pgx.Tx,
	value Agent,
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
	snapshot := agentSnapshot(value)
	if err = events.AppendAudit(ctx, tx, events.AuditEvent{
		ActorType: "operator", ActorID: &actorID, Action: action,
		TargetType: "agent", TargetID: &resourceID, RequestID: requestID,
		Details: snapshot,
	}); err != nil {
		return err
	}
	return events.Append(ctx, tx, events.ResourceEvent{
		Type: eventType, ResourceType: "agent", ResourceID: resourceID, Snapshot: snapshot,
	})
}

func agentSnapshot(value Agent) map[string]any {
	return map[string]any{
		"id": value.ID.String(), "key": value.Key, "selector": value.Selector(),
		"lifecycle":              strings.ToLower(string(value.Lifecycle)),
		"current_version_id":     value.CurrentVersionID.String(),
		"current_version_number": value.CurrentVersion.VersionNumber,
		"configuration":          configurationPayload(value.CurrentVersion.Configuration),
		"version":                value.Version, "created_at": value.CreatedAt.Format(time.RFC3339Nano),
		"updated_at":   value.UpdatedAt.Format(time.RFC3339Nano),
		"activated_at": formattedTime(value.ActivatedAt), "archived_at": formattedTime(value.ArchivedAt),
	}
}

func configurationPayload(value Configuration) map[string]any {
	knowledgeBaseIDs := make([]string, len(value.KnowledgeBaseIDs))
	for index, id := range value.KnowledgeBaseIDs {
		knowledgeBaseIDs[index] = id.String()
	}
	return map[string]any{
		"display_name": value.DisplayName, "description": value.Description,
		"response_language": value.ResponseLanguage, "identity_instructions": value.IdentityInstructions,
		"model_profile_id": value.ModelProfileID.String(), "reasoning_effort": strings.ToLower(string(value.ReasoningEffort)),
		"answer_mode": strings.ToLower(string(value.AnswerMode)), "behavioral_instructions": value.BehavioralInstructions,
		"evidence_access": strings.ToLower(string(value.EvidenceAccess)), "refusal_markdown": value.RefusalMarkdown,
		"max_tool_calls": value.MaxToolCalls, "max_answer_tokens": value.MaxAnswerTokens,
		"knowledge_base_ids": knowledgeBaseIDs,
	}
}

func formattedTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.Format(time.RFC3339Nano)
}

func uniqueAgentConflict(err error) error {
	var databaseError interface{ SQLState() string }
	if errors.As(err, &databaseError) && databaseError.SQLState() == "23505" {
		return conflict("agent key already exists")
	}
	return err
}

func newUUID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, fmt.Errorf("generate agent ID: %w", err)
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
