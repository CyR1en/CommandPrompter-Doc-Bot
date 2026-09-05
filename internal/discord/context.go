package discord

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

const (
	defaultContextIdleExpiry = 30 * time.Minute
	defaultContextMessages   = 20
	defaultContextTokens     = 32_768
	maximumContextMessages   = agents.MaxTranscriptMessages - 1
	maximumContextTokens     = 262_144
)

type ContextOptions struct {
	IdleExpiry  time.Duration
	MaxMessages int
	MaxTokens   int
}

type ContextKey struct {
	BindingID      BindingID
	AgentID        agents.AgentID
	AgentVersionID agents.VersionID
	UserID         Snowflake
	DestinationID  Snowflake
}

type ContextService interface {
	LoadContext(context.Context, ContextKey) ([]agents.Message, error)
	AppendContext(context.Context, ContextKey, string, string) error
}

var _ ContextService = (*Store)(nil)

func normalizeContextOptions(options ContextOptions) (ContextOptions, error) {
	if options.IdleExpiry == 0 {
		options.IdleExpiry = defaultContextIdleExpiry
	}
	if options.MaxMessages == 0 {
		options.MaxMessages = defaultContextMessages
	}
	if options.MaxTokens == 0 {
		options.MaxTokens = defaultContextTokens
	}
	if options.IdleExpiry < time.Minute || options.IdleExpiry > 30*24*time.Hour ||
		options.MaxMessages < 2 || options.MaxMessages > maximumContextMessages || options.MaxMessages%2 != 0 ||
		options.MaxTokens < 2 || options.MaxTokens > maximumContextTokens {
		return ContextOptions{}, errors.New("Discord context options are invalid")
	}
	return options, nil
}

func validateContextKey(key ContextKey) error {
	if key.BindingID == (BindingID{}) || key.AgentID == (agents.AgentID{}) ||
		key.AgentVersionID == (agents.VersionID{}) {
		return errors.New("Discord context identity is invalid")
	}
	if _, err := ParseSnowflake(string(key.UserID)); err != nil {
		return err
	}
	if _, err := ParseSnowflake(string(key.DestinationID)); err != nil {
		return err
	}
	return nil
}

func (store *Store) LoadContext(ctx context.Context, key ContextKey) ([]agents.Message, error) {
	if err := validateContextKey(key); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		SELECT message.role,message.markdown
		FROM discord_conversations AS conversation
		JOIN discord_conversation_messages AS message ON message.conversation_id=conversation.id
		WHERE conversation.binding_id=$1 AND conversation.agent_id=$2
		  AND conversation.agent_version_id=$3 AND conversation.external_user_id=$4
		  AND conversation.destination_id=$5 AND conversation.expires_at>clock_timestamp()
		ORDER BY message.sequence
	`, pgDiscordUUID([16]byte(key.BindingID)), pgDiscordUUID([16]byte(key.AgentID)),
		pgDiscordUUID([16]byte(key.AgentVersionID)), string(key.UserID), string(key.DestinationID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	messages := make([]agents.Message, 0, store.contextOptions.MaxMessages)
	for rows.Next() {
		var role, markdown string
		if err = rows.Scan(&role, &markdown); err != nil {
			return nil, err
		}
		messageRole := agents.RoleUser
		if role == "ASSISTANT" {
			messageRole = agents.RoleAssistant
		} else if role != "USER" {
			return nil, errors.New("stored Discord context role is invalid")
		}
		messages = append(messages, agents.Message{Role: messageRole, Content: markdown})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(messages) > store.contextOptions.MaxMessages || len(messages)%2 != 0 {
		return nil, errors.New("stored Discord context is invalid")
	}
	return messages, nil
}

func (store *Store) AppendContext(
	ctx context.Context,
	key ContextKey,
	userMarkdown string,
	assistantMarkdown string,
) error {
	if err := validateContextKey(key); err != nil {
		return err
	}
	userTokens, err := contextMessageTokens(userMarkdown)
	if err != nil {
		return err
	}
	assistantTokens, err := contextMessageTokens(assistantMarkdown)
	if err != nil {
		return err
	}
	if userTokens+assistantTokens > store.contextOptions.MaxTokens {
		return errors.New("Discord context turn exceeds its token bound")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	lockKey := fmt.Sprintf("discord-context:%s:%s:%s:%s", key.BindingID.String(),
		key.AgentVersionID.String(), key.UserID, key.DestinationID)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, lockKey); err != nil {
		return err
	}
	now, err := discordClock(ctx, tx)
	if err != nil {
		return err
	}
	var conversationID pgtype.UUID
	var expiresAt time.Time
	err = tx.QueryRow(ctx, `
		SELECT id,expires_at FROM discord_conversations
		WHERE binding_id=$1 AND agent_id=$2 AND agent_version_id=$3
		  AND external_user_id=$4 AND destination_id=$5
		FOR UPDATE
	`, pgDiscordUUID([16]byte(key.BindingID)), pgDiscordUUID([16]byte(key.AgentID)),
		pgDiscordUUID([16]byte(key.AgentVersionID)), string(key.UserID), string(key.DestinationID)).Scan(
		&conversationID, &expiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = tx.QueryRow(ctx, `
			INSERT INTO discord_conversations(
				binding_id,agent_id,agent_version_id,external_user_id,destination_id,
				created_at,updated_at,last_activity_at,expires_at
			) VALUES($1,$2,$3,$4,$5,$6,$6,$6,$7)
			RETURNING id
		`, pgDiscordUUID([16]byte(key.BindingID)), pgDiscordUUID([16]byte(key.AgentID)),
			pgDiscordUUID([16]byte(key.AgentVersionID)), string(key.UserID), string(key.DestinationID),
			now, now.Add(store.contextOptions.IdleExpiry)).Scan(&conversationID); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if !expiresAt.After(now) {
			if _, err = tx.Exec(ctx, `DELETE FROM discord_conversation_messages WHERE conversation_id=$1`, conversationID); err != nil {
				return err
			}
		}
		if _, err = tx.Exec(ctx, `
			UPDATE discord_conversations SET updated_at=$2,last_activity_at=$2,expires_at=$3
			WHERE id=$1
		`, conversationID, now, now.Add(store.contextOptions.IdleExpiry)); err != nil {
			return err
		}
	}
	var maximumSequence int64
	if err = tx.QueryRow(ctx, `
		SELECT COALESCE(max(sequence),0)::bigint
		FROM discord_conversation_messages WHERE conversation_id=$1
	`, conversationID).Scan(&maximumSequence); err != nil {
		return err
	}
	if maximumSequence%2 != 0 {
		return errors.New("stored Discord context sequence is invalid")
	}
	if maximumSequence > math.MaxInt32-2 {
		if _, err = tx.Exec(ctx, `DELETE FROM discord_conversation_messages WHERE conversation_id=$1`, conversationID); err != nil {
			return err
		}
		maximumSequence = 0
	}
	if _, err = tx.Exec(ctx, `
		INSERT INTO discord_conversation_messages(
			conversation_id,sequence,role,markdown,estimated_tokens,created_at
		) VALUES($1,$2,'USER',$3,$4,$6),($1,$5,'ASSISTANT',$7,$8,$6)
	`, conversationID, maximumSequence+1, userMarkdown, userTokens, maximumSequence+2, now,
		assistantMarkdown, assistantTokens); err != nil {
		return err
	}
	if err = pruneContext(ctx, tx, conversationID, store.contextOptions); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func pruneContext(ctx context.Context, tx pgx.Tx, conversationID pgtype.UUID, options ContextOptions) error {
	rows, err := tx.Query(ctx, `
		SELECT sequence,estimated_tokens
		FROM discord_conversation_messages
		WHERE conversation_id=$1 ORDER BY sequence DESC
	`, conversationID)
	if err != nil {
		return err
	}
	type item struct{ sequence, tokens int }
	items := make([]item, 0, options.MaxMessages+2)
	for rows.Next() {
		var value item
		if err = rows.Scan(&value.sequence, &value.tokens); err != nil {
			rows.Close()
			return err
		}
		items = append(items, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(items)%2 != 0 {
		return errors.New("stored Discord context turn is incomplete")
	}
	keptMessages, keptTokens, deleteThrough := 0, 0, 0
	for index := 0; index < len(items); index += 2 {
		pairTokens := items[index].tokens + items[index+1].tokens
		if keptMessages+2 > options.MaxMessages || keptTokens+pairTokens > options.MaxTokens {
			deleteThrough = items[index].sequence
			break
		}
		keptMessages += 2
		keptTokens += pairTokens
	}
	if deleteThrough == 0 {
		return nil
	}
	_, err = tx.Exec(ctx, `
		DELETE FROM discord_conversation_messages
		WHERE conversation_id=$1 AND sequence<=$2
	`, conversationID, deleteThrough)
	return err
}

func contextMessageTokens(markdown string) (int, error) {
	if !utf8.ValidString(markdown) || markdown == "" || len([]byte(markdown)) > agents.MaxMessageBytes {
		return 0, errors.New("Discord context message is invalid")
	}
	tokens := (utf8.RuneCountInString(markdown) + 3) / 4
	if tokens < 1 {
		tokens = 1
	}
	return tokens, nil
}
