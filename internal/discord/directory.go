package discord

import (
	"context"
	"errors"
	"fmt"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/jackc/pgx/v5"
)

func (store *Store) ListServers(ctx context.Context, connectionID ConnectionID) ([]Server, error) {
	if _, err := store.GetConnection(ctx, connectionID); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		SELECT server_id, name, icon_hash, owner, refreshed_at
		FROM discord_servers
		WHERE connection_id=$1 AND refreshed_at > $2
		ORDER BY name, server_id
	`, pgDiscordUUID([16]byte(connectionID)), directoryStaleAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Server{}
	for rows.Next() {
		var rawID string
		var value Server
		value.ConnectionID = connectionID
		if err = rows.Scan(&rawID, &value.Name, &value.IconHash, &value.Owner, &value.RefreshedAt); err != nil {
			return nil, err
		}
		if value.ServerID, err = ParseSnowflake(rawID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *Store) ListChannels(ctx context.Context, connectionID ConnectionID, serverID Snowflake) ([]Channel, error) {
	if _, err := ParseSnowflake(string(serverID)); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		SELECT channel_id, parent_id, name, channel_type, position,
		       effective_bot_permissions, everyone_can_view,
		       viewer_role_ids, viewer_user_ids, audience_overwrite_sha256, refreshed_at
		FROM discord_channels
		WHERE connection_id=$1 AND server_id=$2 AND refreshed_at > $3
		ORDER BY position, channel_id
	`, pgDiscordUUID([16]byte(connectionID)), string(serverID), directoryStaleAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Channel{}
	for rows.Next() {
		var rawID string
		var parent *string
		var roleJSON, userJSON, audienceDigest []byte
		var permission int64
		value := Channel{ConnectionID: connectionID, ServerID: serverID}
		if err = rows.Scan(&rawID, &parent, &value.Name, &value.ChannelType,
			&value.Position, &permission, &value.EveryoneCanView,
			&roleJSON, &userJSON, &audienceDigest, &value.RefreshedAt); err != nil {
			return nil, err
		}
		if value.ChannelID, err = ParseSnowflake(rawID); err != nil {
			return nil, err
		}
		if parent != nil {
			parsed, parseErr := ParseSnowflake(*parent)
			if parseErr != nil {
				return nil, parseErr
			}
			value.ParentID = &parsed
		}
		value.EffectiveBotPermissions = Permission(permission)
		if !validPermission(value.EffectiveBotPermissions) || !supportedChannelType(value.ChannelType) {
			return nil, errors.New("stored Discord channel is invalid")
		}
		if value.ViewerRoleIDs, err = decodeSnowflakes(roleJSON); err != nil {
			return nil, err
		}
		if value.ViewerUserIDs, err = decodeSnowflakes(userJSON); err != nil {
			return nil, err
		}
		if len(audienceDigest) != len(value.AudienceOverwriteSHA256) {
			return nil, errors.New("stored Discord channel audience digest is invalid")
		}
		copy(value.AudienceOverwriteSHA256[:], audienceDigest)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (store *Store) ListRoles(ctx context.Context, connectionID ConnectionID, serverID Snowflake) ([]Role, error) {
	if _, err := ParseSnowflake(string(serverID)); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		SELECT role_id, name, position, refreshed_at
		FROM discord_roles
		WHERE connection_id=$1 AND server_id=$2 AND refreshed_at > $3
		ORDER BY position, role_id
	`, pgDiscordUUID([16]byte(connectionID)), string(serverID), directoryStaleAt)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []Role{}
	for rows.Next() {
		var rawID string
		value := Role{ConnectionID: connectionID, ServerID: serverID}
		if err = rows.Scan(&rawID, &value.Name, &value.Position, &value.RefreshedAt); err != nil {
			return nil, err
		}
		if value.RoleID, err = ParseSnowflake(rawID); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func validateBindingPolicyTx(ctx context.Context, database discordRowQueryer, binding Binding) error {
	access, err := bindingAccessPolicyTx(ctx, database, binding.AgentID)
	if err != nil {
		return err
	}
	listen, err := readChannelCheck(ctx, database, binding.ConnectionID, binding.ServerID, binding.ListenChannelID)
	if err != nil {
		return err
	}
	replyID := binding.ListenChannelID
	if binding.ReplyChannelID != nil {
		replyID = *binding.ReplyChannelID
	}
	reply, err := readChannelCheck(ctx, database, binding.ConnectionID, binding.ServerID, replyID)
	if err != nil {
		return err
	}
	rows, err := queryRoles(ctx, database, binding.ConnectionID, binding.ServerID)
	if err != nil {
		return err
	}
	defer rows.Close()
	known := map[Snowflake]struct{}{}
	for rows.Next() {
		var raw string
		if err = rows.Scan(&raw); err != nil {
			return err
		}
		parsed, parseErr := ParseSnowflake(raw)
		if parseErr != nil {
			return parseErr
		}
		known[parsed] = struct{}{}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, allowed := range binding.AllowedRoleIDs {
		if _, exists := known[allowed]; !exists {
			return fmt.Errorf("%w: Discord role allowlist is stale", ErrConflict)
		}
	}
	if err = ValidateBindingPolicy(binding, access, listen, reply); err != nil {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return nil
}

func bindingAccessPolicyTx(
	ctx context.Context,
	database discordRowQueryer,
	agentID agents.AgentID,
) (AccessPolicy, error) {
	var lifecycle string
	var memberCount int32
	var corpusReady, restricted bool
	err := database.QueryRow(ctx, `
		SELECT agent.lifecycle, count(*)::integer,
		       bool_and(kb.lifecycle='ACTIVE' AND kb.published_wiki_id IS NOT NULL),
		       bool_or(kb.access_policy='RESTRICTED')
		FROM agents AS agent
		JOIN agent_version_knowledge_bases AS membership
		  ON membership.agent_version_id=agent.current_version_id
		JOIN knowledge_bases AS kb ON kb.id=membership.knowledge_base_id
		WHERE agent.id=$1
		GROUP BY agent.lifecycle
	`, pgDiscordUUID([16]byte(agentID))).Scan(&lifecycle, &memberCount, &corpusReady, &restricted)
	if errors.Is(err, pgx.ErrNoRows) || err == nil &&
		(lifecycle != "ACTIVE" || memberCount < 1 || !corpusReady) {
		return AccessPublic, fmt.Errorf("%w: Agent corpus must be active and published", ErrConflict)
	}
	if err != nil {
		return AccessPublic, err
	}
	if restricted {
		return AccessRestricted, nil
	}
	return AccessPublic, nil
}

type discordRows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}

type discordRowsQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func queryRoles(ctx context.Context, database discordRowQueryer, connectionID ConnectionID, serverID Snowflake) (pgx.Rows, error) {
	queryer, ok := database.(discordRowsQueryer)
	if !ok {
		return nil, errors.New("Discord role query is unavailable")
	}
	return queryer.Query(ctx, `
		SELECT role_id FROM discord_roles
		WHERE connection_id=$1 AND server_id=$2 AND refreshed_at > $3
	`, pgDiscordUUID([16]byte(connectionID)), string(serverID), directoryStaleAt)
}

func readChannelCheck(
	ctx context.Context,
	database discordRowQueryer,
	connectionID ConnectionID,
	serverID, channelID Snowflake,
) (ChannelCheck, error) {
	var value ChannelCheck
	var permission int64
	var parentID *string
	var roleJSON, userJSON, audienceDigest []byte
	err := database.QueryRow(ctx, `
		SELECT parent_id,channel_type, effective_bot_permissions, everyone_can_view,
		       viewer_role_ids,viewer_user_ids,audience_overwrite_sha256
		FROM discord_channels
		WHERE connection_id=$1 AND server_id=$2 AND channel_id=$3
		  AND refreshed_at > $4
	`, pgDiscordUUID([16]byte(connectionID)), string(serverID), string(channelID), directoryStaleAt).Scan(
		&parentID, &value.ChannelType, &permission, &value.EveryoneCanView, &roleJSON, &userJSON, &audienceDigest,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ChannelCheck{}, fmt.Errorf("%w: Discord channel metadata is unavailable", ErrConflict)
	}
	if err != nil {
		return ChannelCheck{}, err
	}
	value.ServerID = serverID
	value.ChannelID = channelID
	if parentID != nil {
		parent, parseErr := ParseSnowflake(*parentID)
		if parseErr != nil {
			return ChannelCheck{}, parseErr
		}
		value.ParentID = &parent
	}
	value.EffectiveBotPermissions = Permission(permission)
	if value.ViewerRoleIDs, err = decodeSnowflakes(roleJSON); err != nil {
		return ChannelCheck{}, err
	}
	if value.ViewerUserIDs, err = decodeSnowflakes(userJSON); err != nil {
		return ChannelCheck{}, err
	}
	if len(audienceDigest) != len(value.AudienceOverwriteSHA256) {
		return ChannelCheck{}, errors.New("stored Discord channel audience digest is invalid")
	}
	copy(value.AudienceOverwriteSHA256[:], audienceDigest)
	sortSnowflakes(value.ViewerRoleIDs)
	sortSnowflakes(value.ViewerUserIDs)
	return value, nil
}
