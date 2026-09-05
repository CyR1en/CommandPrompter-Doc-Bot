package discord

import (
	"context"
	"errors"
	"slices"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type DeliveryPermit struct {
	Invocation    Invocation
	RunID         agents.RunID
	DestinationID Snowflake
}

type DeliveryAuthorizer interface {
	ReauthorizeDelivery(context.Context, DeliveryPermit) error
}

type DeliveryREST interface {
	RefreshDelivery(context.Context, string, Snowflake, Snowflake, Snowflake, Snowflake, Snowflake) (LiveDeliveryState, error)
	Close()
}

type DeliveryRESTFactory func() (DeliveryREST, error)

var _ DeliveryAuthorizer = (*Store)(nil)

func (store *Store) ReauthorizeDelivery(ctx context.Context, permit DeliveryPermit) error {
	if permit.RunID == (agents.RunID{}) || permit.DestinationID == "" ||
		permit.Invocation.InvocationChannelID == "" ||
		validateContextKey(ContextKey{
			BindingID: permit.Invocation.Binding.ID, AgentID: permit.Invocation.Binding.AgentID,
			AgentVersionID: permit.Invocation.AgentVersionID,
			UserID:         Snowflake(permit.Invocation.Subject), DestinationID: permit.DestinationID,
		}) != nil {
		return ErrConflict
	}
	botUserID, err := store.assertDeliveryDatabaseState(ctx, permit)
	if err != nil {
		return err
	}
	reader, err := credentials.NewSecretReader(store.pool, store.vault)
	if err != nil {
		return err
	}
	secret, err := reader.Read(ctx, permit.Invocation.CredentialID,
		security.CredentialDiscordBotToken, permit.Invocation.CredentialVersion)
	if err != nil {
		return ErrConflict
	}
	client, err := store.deliveryRESTFactory()
	if err != nil {
		return err
	}
	defer client.Close()
	state, err := client.RefreshDelivery(
		ctx, secret.Reveal(), botUserID, permit.Invocation.Binding.ServerID,
		Snowflake(permit.Invocation.Subject), permit.Invocation.Binding.ListenChannelID,
		permit.DestinationID,
	)
	if err != nil {
		return ErrConflict
	}
	callerRoleIDs := make([]Snowflake, 0, len(state.CallerRoleIDs))
	for roleID := range state.CallerRoleIDs {
		callerRoleIDs = append(callerRoleIDs, roleID)
	}
	sortSnowflakes(callerRoleIDs)
	if !slices.Equal(permit.Invocation.CallerRoleIDs, callerRoleIDs) {
		return ErrConflict
	}
	if (permit.Invocation.EffectiveAccess == AccessRestricted ||
		len(permit.Invocation.Binding.AllowedRoleIDs) > 0 || len(permit.Invocation.Binding.AllowedUserIDs) > 0) &&
		!BindingPermits(permit.Invocation.Binding, Snowflake(permit.Invocation.Subject), state.CallerRoleIDs) {
		return ErrConflict
	}
	if permit.Invocation.Binding.ReplyPolicy == ReplyThread && permit.DestinationID != permit.Invocation.Binding.ListenChannelID &&
		(state.Destination.ParentID == nil || *state.Destination.ParentID != permit.Invocation.Binding.ListenChannelID) {
		return ErrConflict
	}
	listenPermissions := PermissionViewChannel | PermissionReadMessageHistory
	replyPermissions := requiredReplyPermissions(permit.Invocation.Binding, state.Destination)
	if !sameCapturedChannelSecurity(permit.Invocation.CapturedListen, state.Listen, listenPermissions) ||
		!sameCapturedChannelSecurity(permit.Invocation.CapturedReply, state.Destination, replyPermissions) {
		return ErrConflict
	}
	if err = ValidateBindingPolicy(
		permit.Invocation.Binding, permit.Invocation.EffectiveAccess, state.Listen, state.Destination,
	); err != nil {
		return ErrConflict
	}
	_, err = store.assertDeliveryDatabaseState(ctx, permit)
	return err
}

func (store *Store) assertDeliveryDatabaseState(ctx context.Context, permit DeliveryPermit) (Snowflake, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if err = assertInvocationRouteWinner(ctx, tx, permit.Invocation); err != nil {
		return "", err
	}
	currentBinding, err := readBinding(ctx, tx, permit.Invocation.Binding.ID, false)
	if err != nil || !sameDeliveryBinding(currentBinding, permit.Invocation.Binding) {
		return "", ErrConflict
	}
	if !currentBinding.Enabled || currentBinding.Health != BindingHealthy ||
		!currentBinding.HasTrigger(permit.Invocation.Trigger) {
		return "", ErrConflict
	}
	switch currentBinding.ReplyPolicy {
	case ReplySameChannel:
		if permit.DestinationID != currentBinding.ListenChannelID {
			return "", ErrConflict
		}
	case ReplySelectedChannel:
		if currentBinding.ReplyChannelID == nil || permit.DestinationID != *currentBinding.ReplyChannelID {
			return "", ErrConflict
		}
	case ReplyThread:
	default:
		return "", ErrConflict
	}
	connection, err := readConnection(ctx, tx, currentBinding.ConnectionID, false)
	if err != nil || connection.Version != permit.Invocation.ConnectionVersion ||
		connection.Lifecycle != ConnectionEnabled || connection.State != StateReady ||
		connection.CredentialID != permit.Invocation.CredentialID ||
		connection.CredentialVersion != permit.Invocation.CredentialVersion || connection.BotUserID == nil {
		return "", ErrConflict
	}
	var (
		runAgentID, runVersionID                  pgtype.UUID
		runResourceVersion                        int32
		runOrigin, runSubject, runAccess, outcome string
	)
	err = tx.QueryRow(ctx, `
		SELECT agent_id,agent_version_id,agent_resource_version,origin,subject,
		       effective_access_policy,outcome
		FROM agent_runs WHERE id=$1
	`, pgDiscordUUID([16]byte(permit.RunID))).Scan(
		&runAgentID, &runVersionID, &runResourceVersion, &runOrigin, &runSubject, &runAccess, &outcome,
	)
	if err != nil || runAgentID.Bytes != [16]byte(permit.Invocation.Binding.AgentID) ||
		runVersionID.Bytes != [16]byte(permit.Invocation.AgentVersionID) ||
		runResourceVersion != permit.Invocation.AgentResourceVersion || runOrigin != string(agents.OriginDiscord) ||
		runSubject != permit.Invocation.Subject || AccessPolicy(runAccess) != permit.Invocation.EffectiveAccess ||
		agents.CompletionStatus(outcome) == agents.CompletionFailed {
		return "", ErrConflict
	}
	var currentVersionID pgtype.UUID
	var lifecycle string
	var currentResourceVersion int32
	if err = tx.QueryRow(ctx, `
		SELECT current_version_id,lifecycle,version FROM agents WHERE id=$1
	`, pgDiscordUUID([16]byte(permit.Invocation.Binding.AgentID))).Scan(
		&currentVersionID, &lifecycle, &currentResourceVersion,
	); err != nil || currentVersionID.Bytes != [16]byte(permit.Invocation.AgentVersionID) ||
		lifecycle != "ACTIVE" || currentResourceVersion != permit.Invocation.AgentResourceVersion {
		return "", ErrConflict
	}
	rows, err := tx.Query(ctx, `
		SELECT membership.position,membership.knowledge_base_id,kb.lifecycle,
		       kb.access_policy,kb.published_wiki_id IS NOT NULL,
		       run_scope.knowledge_base_id,run_scope.knowledge_base_version,run_scope.access_policy,
		       run_scope.wiki_version_id
		FROM agent_version_knowledge_bases AS membership
		JOIN knowledge_bases AS kb ON kb.id=membership.knowledge_base_id
		JOIN agent_run_knowledge_bases AS run_scope
		  ON run_scope.run_id=$2 AND run_scope.position=membership.position
		WHERE membership.agent_version_id=$1
		ORDER BY membership.position
	`, currentVersionID, pgDiscordUUID([16]byte(permit.RunID)))
	if err != nil {
		return "", err
	}
	position := 0
	effective := AccessPublic
	for rows.Next() {
		var storedPosition, runVersion int32
		var knowledgeBaseID, runKnowledgeBaseID, runWikiVersionID pgtype.UUID
		var kbLifecycle, access, runScopeAccess string
		var published bool
		if err = rows.Scan(
			&storedPosition, &knowledgeBaseID, &kbLifecycle, &access, &published,
			&runKnowledgeBaseID, &runVersion, &runScopeAccess, &runWikiVersionID,
		); err != nil {
			rows.Close()
			return "", err
		}
		if position >= len(permit.Invocation.Corpus) {
			rows.Close()
			return "", ErrConflict
		}
		expected := permit.Invocation.Corpus[position]
		if storedPosition != expected.Position || knowledgeBaseID.Bytes != [16]byte(expected.KnowledgeBaseID) ||
			kbLifecycle != "ACTIVE" || !published ||
			AccessPolicy(access) != expected.AccessPolicy || runKnowledgeBaseID.Bytes != knowledgeBaseID.Bytes ||
			runVersion != expected.KnowledgeBaseVersion || AccessPolicy(runScopeAccess) != expected.AccessPolicy ||
			!runWikiVersionID.Valid {
			rows.Close()
			return "", ErrConflict
		}
		if AccessPolicy(access) == AccessRestricted {
			effective = AccessRestricted
		}
		position++
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return "", err
	}
	rows.Close()
	if position != len(permit.Invocation.Corpus) || effective != permit.Invocation.EffectiveAccess {
		return "", ErrConflict
	}
	var runScopeCount int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM agent_run_knowledge_bases WHERE run_id=$1`,
		pgDiscordUUID([16]byte(permit.RunID))).Scan(&runScopeCount); err != nil || runScopeCount != position {
		return "", ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	return *connection.BotUserID, nil
}

func assertInvocationRouteWinner(ctx context.Context, tx pgx.Tx, invocation Invocation) error {
	var parentRoute any
	if invocation.InvocationParentID != nil {
		parentRoute = string(*invocation.InvocationParentID)
	}
	var bindingID pgtype.UUID
	err := tx.QueryRow(ctx, `
		SELECT route.binding_id
		FROM channel_binding_triggers AS route
		JOIN channel_bindings AS binding ON binding.id=route.binding_id
		WHERE route.connection_id=$1 AND route.server_id=$2 AND route.trigger_type=$5
		  AND binding.deleted_at IS NULL
		  AND (route.listen_channel_id=$3 OR
		       ($4::varchar IS NOT NULL AND route.listen_channel_id=$4
		        AND binding.reply_policy='THREAD' AND route.enabled=true))
		ORDER BY (route.listen_channel_id=$3) DESC, route.enabled DESC, route.binding_id
		LIMIT 1
	`, pgDiscordUUID([16]byte(invocation.Binding.ConnectionID)), string(invocation.Binding.ServerID),
		string(invocation.InvocationChannelID), parentRoute, string(invocation.Trigger)).Scan(&bindingID)
	if err != nil || !bindingID.Valid || bindingID.Bytes != [16]byte(invocation.Binding.ID) {
		return ErrConflict
	}
	return nil
}

func sameDeliveryBinding(left Binding, right Binding) bool {
	return left.ID == right.ID && left.ConnectionID == right.ConnectionID &&
		left.ServerID == right.ServerID && left.ListenChannelID == right.ListenChannelID &&
		left.AgentID == right.AgentID && slices.Equal(left.Triggers, right.Triggers) &&
		left.ReplyPolicy == right.ReplyPolicy && equalSnowflakePointer(left.ReplyChannelID, right.ReplyChannelID) &&
		slices.Equal(left.AllowedRoleIDs, right.AllowedRoleIDs) &&
		slices.Equal(left.AllowedUserIDs, right.AllowedUserIDs) && left.Enabled == right.Enabled &&
		left.Health == right.Health && left.Version == right.Version
}

func equalSnowflakePointer(left, right *Snowflake) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameCapturedChannelSecurity(captured, current ChannelCheck, required Permission) bool {
	return captured.ServerID == current.ServerID && captured.EveryoneCanView == current.EveryoneCanView &&
		captured.EffectiveBotPermissions&required == current.EffectiveBotPermissions&required &&
		slices.Equal(captured.ViewerRoleIDs, current.ViewerRoleIDs) &&
		slices.Equal(captured.ViewerUserIDs, current.ViewerUserIDs) &&
		captured.AudienceOverwriteSHA256 == current.AudienceOverwriteSHA256
}

func deliveryConflict(err error) bool {
	return err != nil && (errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound))
}
