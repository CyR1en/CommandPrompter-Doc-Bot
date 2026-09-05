package providers

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/cyr1en/ref0/internal/idempotency"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (store *Store) ListAssignments(ctx context.Context, knowledgeBaseID KnowledgeBaseID) ([]Assignment, error) {
	rows, err := store.pool.Query(ctx, `
		SELECT `+assignmentColumns+` FROM model_assignments
		WHERE knowledge_base_id=$1 ORDER BY role
	`, uuid(knowledgeBaseID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Assignment{}
	for rows.Next() {
		value, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *Store) Assign(ctx context.Context, command AssignModel, actor ActorID, requestKey string) (Assignment, error) {
	if err := command.validate(); err != nil {
		return Assignment{}, err
	}
	request, err := store.request(actor, requestKey, "model_assignment.put", map[string]any{
		"knowledge_base_id": command.KnowledgeBaseID.String(), "role": command.Role,
		"profile_id": command.ProfileID.String(), "reasoning_effort": command.ReasoningEffort,
		"answer_mode": command.AnswerMode, "expected_version": command.ExpectedVersion,
	})
	if err != nil {
		return Assignment{}, err
	}
	return withTx(ctx, store.pool, func(tx pgx.Tx) (Assignment, error) {
		result, err := idempotency.Execute(ctx, tx, request, func(ctx context.Context, tx pgx.Tx) (idempotency.Result, error) {
			var lifecycle string
			err := tx.QueryRow(ctx, `SELECT lifecycle FROM knowledge_bases WHERE id=$1 FOR UPDATE`, uuid(command.KnowledgeBaseID)).Scan(&lifecycle)
			if errors.Is(err, pgx.ErrNoRows) {
				return idempotency.Result{}, fmt.Errorf("%w: knowledge base not found", ErrNotFound)
			}
			if err != nil {
				return idempotency.Result{}, err
			}
			if lifecycle != "ACTIVE" {
				return idempotency.Result{}, fmt.Errorf("%w: knowledge base cannot accept assignments", ErrConflict)
			}
			hint, err := scanProfileRow(tx.QueryRow(ctx, profileQuery(false), uuid(command.ProfileID)))
			if errors.Is(err, pgx.ErrNoRows) {
				return idempotency.Result{}, fmt.Errorf("%w: model profile not found", ErrNotFound)
			}
			if err != nil {
				return idempotency.Result{}, err
			}
			endpoint, err := scanEndpoint(tx.QueryRow(ctx, endpointQuery(true), uuid(hint.EndpointID)))
			if err != nil {
				return idempotency.Result{}, err
			}
			if endpoint.Lifecycle != Active {
				return idempotency.Result{}, fmt.Errorf("%w: provider endpoint is archived", ErrConflict)
			}
			profile, err := getProfileTx(ctx, tx, command.ProfileID, true)
			if err != nil {
				return idempotency.Result{}, err
			}
			if profile.Availability == Unavailable {
				return idempotency.Result{}, fmt.Errorf("%w: unavailable model cannot be assigned", ErrConflict)
			}
			if profile.CurrentVersion.ConfigurationVersion != endpoint.ConfigurationVersion {
				return idempotency.Result{}, fmt.Errorf("%w: model profile configuration is stale", ErrConflict)
			}
			if err := validateAssignment(command, profile.CurrentVersion.Settings); err != nil {
				return idempotency.Result{}, err
			}
			var existingID pgtype.UUID
			var existingVersion int32
			err = tx.QueryRow(ctx, `
				SELECT id, version FROM model_assignments
				WHERE knowledge_base_id=$1 AND role=$2 FOR UPDATE
			`, uuid(command.KnowledgeBaseID), command.Role).Scan(&existingID, &existingVersion)
			var assignmentID AssignmentID
			var version int32
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				if command.ExpectedVersion != nil {
					return idempotency.Result{}, fmt.Errorf("%w: model assignment version is stale", ErrConflict)
				}
				id, err := newUUID()
				if err != nil {
					return idempotency.Result{}, err
				}
				assignmentID, version = AssignmentID(id), 1
				_, err = tx.Exec(ctx, `
					INSERT INTO model_assignments (
						id, knowledge_base_id, role, model_profile_id, reasoning_effort,
						answer_mode, version, created_at, updated_at
					) VALUES ($1,$2,$3,$4,$5,$6,1,clock_timestamp(),clock_timestamp())
				`, uuid(assignmentID), uuid(command.KnowledgeBaseID), command.Role,
					uuid(command.ProfileID), command.ReasoningEffort, command.AnswerMode)
				if err != nil {
					return idempotency.Result{}, uniqueConflict(err)
				}
			case err != nil:
				return idempotency.Result{}, err
			default:
				if command.ExpectedVersion == nil || *command.ExpectedVersion != existingVersion {
					return idempotency.Result{}, fmt.Errorf("%w: model assignment version is stale", ErrConflict)
				}
				assignmentID, version = AssignmentID(existingID.Bytes), existingVersion+1
				_, err = tx.Exec(ctx, `
					UPDATE model_assignments SET model_profile_id=$2, reasoning_effort=$3,
						answer_mode=$4, version=$5, updated_at=clock_timestamp()
					WHERE id=$1
				`, uuid(assignmentID), uuid(command.ProfileID), command.ReasoningEffort,
					command.AnswerMode, version)
				if err != nil {
					return idempotency.Result{}, err
				}
			}
			value, err := scanAssignment(tx.QueryRow(ctx,
				"SELECT "+assignmentColumns+" FROM model_assignments WHERE id=$1", uuid(assignmentID)))
			if err != nil {
				return idempotency.Result{}, err
			}
			if err := store.recordAssignment(ctx, tx, value, actor); err != nil {
				return idempotency.Result{}, err
			}
			return idempotency.Result{Type: "model_assignment:" + strconv.Itoa(int(version)), ID: [16]byte(assignmentID)}, nil
		})
		if err != nil {
			return Assignment{}, err
		}
		version, ok := resultVersion(result.Type, "model_assignment")
		if !ok {
			return Assignment{}, idempotency.ErrConflict
		}
		value, err := scanAssignment(tx.QueryRow(ctx,
			"SELECT "+assignmentColumns+" FROM model_assignments WHERE id=$1", uuid(result.ID)))
		if err != nil || value.Version != version {
			if err == nil || errors.Is(err, pgx.ErrNoRows) {
				return Assignment{}, idempotency.ErrConflict
			}
			return Assignment{}, err
		}
		return value, nil
	})
}

func (store *Store) recordAssignment(ctx context.Context, tx pgx.Tx, value Assignment, actor ActorID) error {
	return store.record(ctx, tx, &actor, "model_assignment.put", "model_assignment.updated",
		"model_assignment", [16]byte(value.ID), assignmentSnapshot(value))
}

func assignmentSnapshot(value Assignment) map[string]any {
	return map[string]any{
		"id": value.ID.String(), "knowledge_base_id": value.KnowledgeBaseID.String(),
		"role": stringsLower(value.Role), "model_profile_id": value.ProfileID.String(),
		"reasoning_effort": stringsLower(value.ReasoningEffort), "answer_mode": stringsLower(value.AnswerMode),
		"version": value.Version, "created_at": pythonTime(value.CreatedAt), "updated_at": pythonTime(value.UpdatedAt),
	}
}
