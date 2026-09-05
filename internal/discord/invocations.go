package discord

import (
	"context"
	"fmt"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func (store *Store) AuthorizeInvocation(
	ctx context.Context,
	capture GatewayCapture,
	serverID, channelID Snowflake,
	parentChannelID *Snowflake,
	userID Snowflake,
	roleIDs map[Snowflake]struct{},
	slash bool,
) (Invocation, error) {
	if capture.ConnectionID == (ConnectionID{}) || capture.ConnectionVersion <= 0 ||
		capture.CredentialID == (credentials.ID{}) || capture.CredentialVersion <= 0 {
		return Invocation{}, ErrConflict
	}
	for _, value := range []Snowflake{serverID, channelID, userID} {
		if _, err := ParseSnowflake(string(value)); err != nil {
			return Invocation{}, ErrConflict
		}
	}
	callerRoleIDs := make([]Snowflake, 0, len(roleIDs))
	for roleID := range roleIDs {
		if _, err := ParseSnowflake(string(roleID)); err != nil || roleID == serverID {
			return Invocation{}, ErrConflict
		}
		callerRoleIDs = append(callerRoleIDs, roleID)
	}
	sortSnowflakes(callerRoleIDs)
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Invocation{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	trigger := TriggerMention
	if slash {
		trigger = TriggerSlashCommand
	}
	var parentRoute any
	if parentChannelID != nil {
		if _, err = ParseSnowflake(string(*parentChannelID)); err != nil || *parentChannelID == channelID {
			return Invocation{}, ErrConflict
		}
		parentRoute = string(*parentChannelID)
	}
	rows, err := tx.Query(ctx, `
		SELECT cb.id, dc.version, dc.credential_id, dc.credential_version,
		       agent.agent_key, agent.current_version_id, agent.version,
		       route.listen_channel_id,cb.enabled,cb.health,cb.deleted_at,
		       dc.lifecycle,dc.state,agent.lifecycle
		FROM channel_binding_triggers AS route
		JOIN channel_bindings AS cb ON cb.id=route.binding_id
		JOIN discord_connections AS dc ON dc.id=cb.connection_id
		JOIN agents AS agent ON agent.id=cb.agent_id
		WHERE route.connection_id=$1 AND route.server_id=$2 AND route.trigger_type=$5
		  AND cb.deleted_at IS NULL
		  AND (route.listen_channel_id=$3 OR
		       ($4::varchar IS NOT NULL AND route.listen_channel_id=$4
		        AND cb.reply_policy='THREAD' AND route.enabled=true))
		ORDER BY (route.listen_channel_id=$3) DESC, route.enabled DESC, route.binding_id
		LIMIT 1
	`, pgDiscordUUID([16]byte(capture.ConnectionID)), string(serverID), string(channelID), parentRoute, string(trigger))
	if err != nil {
		return Invocation{}, err
	}
	type candidate struct {
		id, credentialID, agentVersionID                           pgtype.UUID
		connectionVersion, credentialVersion, agentResourceVersion int32
		agentKey, routeChannel, bindingHealth                      string
		connectionLifecycle, connectionState, agentLifecycle       string
		bindingEnabled                                             bool
		deletedAt                                                  pgtype.Timestamptz
	}
	candidates := []candidate{}
	for rows.Next() {
		var value candidate
		if err = rows.Scan(
			&value.id, &value.connectionVersion, &value.credentialID, &value.credentialVersion,
			&value.agentKey, &value.agentVersionID, &value.agentResourceVersion,
			&value.routeChannel, &value.bindingEnabled, &value.bindingHealth, &value.deletedAt,
			&value.connectionLifecycle, &value.connectionState, &value.agentLifecycle,
		); err != nil {
			rows.Close()
			return Invocation{}, err
		}
		candidates = append(candidates, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return Invocation{}, err
	}
	if len(candidates) == 0 {
		return Invocation{}, fmt.Errorf("%w: Discord invocation route does not exist", ErrNotFound)
	}
	if len(candidates) != 1 {
		return Invocation{}, fmt.Errorf("%w: Discord invocation route is ambiguous", ErrConflict)
	}
	selected := candidates[0]
	if !selected.credentialID.Valid || !selected.agentVersionID.Valid ||
		selected.connectionVersion <= 0 || selected.credentialVersion <= 0 || selected.agentResourceVersion <= 0 ||
		!selected.bindingEnabled || selected.bindingHealth != string(BindingHealthy) || selected.deletedAt.Valid ||
		selected.connectionLifecycle != string(ConnectionEnabled) || selected.connectionState != string(StateReady) ||
		selected.agentLifecycle != string(agents.Active) {
		return Invocation{}, fmt.Errorf("%w: Discord invocation capture is invalid", ErrConflict)
	}
	if selected.connectionVersion != capture.ConnectionVersion ||
		credentials.ID(selected.credentialID.Bytes) != capture.CredentialID ||
		selected.credentialVersion != capture.CredentialVersion {
		return Invocation{}, fmt.Errorf("%w: Discord gateway capture is stale", ErrConflict)
	}
	binding, err := readBinding(ctx, tx, BindingID(selected.id.Bytes), false)
	if err != nil {
		return Invocation{}, err
	}
	if selected.routeChannel != string(channelID) &&
		(parentChannelID == nil || selected.routeChannel != string(*parentChannelID) || binding.ReplyPolicy != ReplyThread) {
		return Invocation{}, fmt.Errorf("%w: Discord invocation route is invalid", ErrConflict)
	}
	corpusRows, err := tx.Query(ctx, `
		SELECT membership.position, kb.id, kb.version, kb.lifecycle,
		       kb.access_policy, kb.published_wiki_id IS NOT NULL
		FROM agent_version_knowledge_bases AS membership
		JOIN knowledge_bases AS kb ON kb.id=membership.knowledge_base_id
		WHERE membership.agent_version_id=$1
		ORDER BY membership.position
	`, selected.agentVersionID)
	if err != nil {
		return Invocation{}, err
	}
	corpus := make([]InvocationCorpusMember, 0)
	effectiveAccess := AccessPublic
	for corpusRows.Next() {
		var position, version int32
		var knowledgeBaseID pgtype.UUID
		var lifecycle, access string
		var published bool
		if err = corpusRows.Scan(&position, &knowledgeBaseID, &version, &lifecycle, &access, &published); err != nil {
			corpusRows.Close()
			return Invocation{}, err
		}
		policy := AccessPolicy(access)
		if position != int32(len(corpus)) || !knowledgeBaseID.Valid || version <= 0 ||
			lifecycle != "ACTIVE" || !published || policy != AccessPublic && policy != AccessRestricted {
			corpusRows.Close()
			return Invocation{}, fmt.Errorf("%w: Discord Agent corpus is unavailable", ErrConflict)
		}
		if policy == AccessRestricted {
			effectiveAccess = AccessRestricted
		}
		corpus = append(corpus, InvocationCorpusMember{
			Position: position, KnowledgeBaseID: agents.KnowledgeBaseID(knowledgeBaseID.Bytes),
			KnowledgeBaseVersion: version, AccessPolicy: policy,
		})
	}
	err = corpusRows.Err()
	corpusRows.Close()
	if err != nil {
		return Invocation{}, err
	}
	if len(corpus) == 0 {
		return Invocation{}, fmt.Errorf("%w: Discord Agent corpus is unavailable", ErrConflict)
	}
	if (effectiveAccess == AccessRestricted || len(binding.AllowedRoleIDs) > 0 || len(binding.AllowedUserIDs) > 0) &&
		!BindingPermits(binding, userID, roleIDs) {
		return Invocation{}, fmt.Errorf("%w: Discord invocation is not authorized", ErrConflict)
	}
	if err = validateBindingPolicyTx(ctx, tx, binding); err != nil {
		return Invocation{}, err
	}
	listenCheck, err := readChannelCheck(ctx, tx, binding.ConnectionID, binding.ServerID, binding.ListenChannelID)
	if err != nil {
		return Invocation{}, err
	}
	replyID := binding.ListenChannelID
	if binding.ReplyChannelID != nil {
		replyID = *binding.ReplyChannelID
	}
	replyCheck, err := readChannelCheck(ctx, tx, binding.ConnectionID, binding.ServerID, replyID)
	if err != nil {
		return Invocation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Invocation{}, err
	}
	return Invocation{
		Binding: binding, Trigger: trigger, InvocationChannelID: channelID,
		InvocationParentID: parentChannelID, AgentKey: selected.agentKey,
		AgentVersionID:       agents.VersionID(selected.agentVersionID.Bytes),
		AgentResourceVersion: selected.agentResourceVersion, EffectiveAccess: effectiveAccess,
		Corpus: corpus, Subject: string(userID), ConnectionVersion: selected.connectionVersion,
		CredentialID: credentials.ID(selected.credentialID.Bytes), CredentialVersion: selected.credentialVersion,
		CallerRoleIDs:  callerRoleIDs,
		CapturedListen: listenCheck, CapturedReply: replyCheck,
	}, nil
}

func (store *Store) ConsumeRate(ctx context.Context, binding Binding, userID Snowflake) (bool, error) {
	if _, err := ParseSnowflake(string(userID)); err != nil {
		return false, err
	}
	if err := ValidateRatePolicy(binding.RatePolicy); err != nil {
		return false, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	key := fmt.Sprintf("discord-rate:%s:%s", binding.ID.String(), userID)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return false, err
	}
	now, err := discordClock(ctx, tx)
	if err != nil {
		return false, err
	}
	windowSeconds := int64(binding.RatePolicy.WindowSeconds)
	window := time.Unix((now.Unix()/windowSeconds)*windowSeconds, 0).In(now.Location())
	expires := window.Add(time.Duration(binding.RatePolicy.WindowSeconds) * time.Second)
	var count int32
	if err = tx.QueryRow(ctx, `
		INSERT INTO rate_limit_buckets(
			binding_id, external_user_id, window_started_at, request_count, expires_at
		) VALUES($1,$2,$3,1,$4)
		ON CONFLICT(binding_id, external_user_id, window_started_at)
		DO UPDATE SET request_count=rate_limit_buckets.request_count+1
		RETURNING request_count
	`, pgDiscordUUID([16]byte(binding.ID)), string(userID), window, expires).Scan(&count); err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, `
		DELETE FROM rate_limit_buckets WHERE expires_at < $1::timestamptz - interval '1 day'
	`, now); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return count <= binding.RatePolicy.Requests, nil
}
