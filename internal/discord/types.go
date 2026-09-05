package discord

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"golang.org/x/text/cases"
)

type BindingID [16]byte
type ActorID = auth.OperatorID
type Snowflake string

func (id BindingID) String() string { return jobs.UUID(id).String() }

func ParseConnectionID(value string) (ConnectionID, error) {
	id, err := jobs.ParseUUID(value)
	return ConnectionID(id), err
}

func ParseBindingID(value string) (BindingID, error) {
	id, err := jobs.ParseUUID(value)
	return BindingID(id), err
}

func ParseSnowflake(value string) (Snowflake, error) {
	if value == "" || value[0] == '0' {
		return "", errors.New("Discord snowflake is invalid")
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return "", errors.New("Discord snowflake is invalid")
		}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 {
		return "", errors.New("Discord snowflake is invalid")
	}
	return Snowflake(value), nil
}

type ConnectionLifecycle string

const (
	ConnectionEnabled  ConnectionLifecycle = "ENABLED"
	ConnectionDisabled ConnectionLifecycle = "DISABLED"
)

type ConnectionState string

const (
	StateDisabled   ConnectionState = "DISABLED"
	StateConnecting ConnectionState = "CONNECTING"
	StateReady      ConnectionState = "READY"
	StateDegraded   ConnectionState = "DEGRADED"
)

type TriggerType string

const (
	TriggerMention      TriggerType = "MENTION"
	TriggerSlashCommand TriggerType = "SLASH_COMMAND"
)

func (binding Binding) HasTrigger(trigger TriggerType) bool {
	for _, candidate := range binding.Triggers {
		if candidate == trigger {
			return true
		}
	}
	return false
}

type ReplyPolicy string

const (
	ReplySameChannel     ReplyPolicy = "SAME_CHANNEL"
	ReplyThread          ReplyPolicy = "THREAD"
	ReplySelectedChannel ReplyPolicy = "SELECTED_CHANNEL"
)

type BindingHealth string

const (
	BindingDraft     BindingHealth = "DRAFT"
	BindingHealthy   BindingHealth = "HEALTHY"
	BindingUnhealthy BindingHealth = "UNHEALTHY"
)

type AccessPolicy = agents.AccessPolicy

const (
	AccessPublic     = agents.Public
	AccessRestricted = agents.Restricted
)

type Permission int64

const (
	PermissionViewChannel          Permission = 1 << 10
	PermissionSendMessages         Permission = 1 << 11
	PermissionEmbedLinks           Permission = 1 << 14
	PermissionReadMessageHistory   Permission = 1 << 16
	PermissionCreatePublicThreads  Permission = 1 << 35
	PermissionSendMessagesInThread Permission = 1 << 38

	BasePermissions = PermissionViewChannel |
		PermissionSendMessages |
		PermissionEmbedLinks |
		PermissionReadMessageHistory
	ThreadPermissions = PermissionCreatePublicThreads | PermissionSendMessagesInThread
)

type Connection struct {
	ID                ConnectionID
	DisplayName       string
	CredentialID      credentials.ID
	CredentialVersion int32
	ApplicationID     *Snowflake
	BotUserID         *Snowflake
	BotUsername       *string
	AvatarHash        *string
	Lifecycle         ConnectionLifecycle
	State             ConnectionState
	GatewayLatencyMS  *int32
	LastHeartbeatAt   *time.Time
	LastEventAt       *time.Time
	SanitizedError    *string
	Version           int32
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

type CreateConnection struct {
	DisplayName  string
	CredentialID credentials.ID
}

type UpdateConnection struct {
	ConnectionID    ConnectionID
	ExpectedVersion int32
	DisplayName     string
	Lifecycle       ConnectionLifecycle
}

type RotateToken struct {
	ConnectionID    ConnectionID
	ExpectedVersion int32
	CredentialID    credentials.ID
}

type RatePolicy struct {
	Requests      int32
	WindowSeconds int32
}

func DefaultRatePolicy() RatePolicy { return RatePolicy{Requests: 5, WindowSeconds: 60} }

type Binding struct {
	ID              BindingID
	ConnectionID    ConnectionID
	ServerID        Snowflake
	ListenChannelID Snowflake
	AgentID         agents.AgentID
	Triggers        []TriggerType
	ReplyPolicy     ReplyPolicy
	ReplyChannelID  *Snowflake
	AllowedRoleIDs  []Snowflake
	AllowedUserIDs  []Snowflake
	RatePolicy      RatePolicy
	Enabled         bool
	Health          BindingHealth
	SanitizedError  *string
	Version         int32
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type BindingConfiguration struct {
	ConnectionID    ConnectionID
	ServerID        Snowflake
	ListenChannelID Snowflake
	AgentID         agents.AgentID
	Triggers        []TriggerType
	ReplyPolicy     ReplyPolicy
	ReplyChannelID  *Snowflake
	AllowedRoleIDs  []Snowflake
	AllowedUserIDs  []Snowflake
	RatePolicy      RatePolicy
}

type BindingCapture struct {
	ID      BindingID
	Version int32
}

type CreateBinding struct {
	Configuration BindingConfiguration
	Enabled       bool
}

type UpdateBinding struct {
	BindingID       BindingID
	ExpectedVersion int32
	Configuration   BindingConfiguration
	Enabled         bool
}

type ChannelCheck struct {
	ServerID                Snowflake
	ChannelID               Snowflake
	ParentID                *Snowflake
	ChannelType             int32
	EffectiveBotPermissions Permission
	EveryoneCanView         bool
	ViewerRoleIDs           []Snowflake
	ViewerUserIDs           []Snowflake
	AudienceOverwriteSHA256 [32]byte
}

type Server struct {
	ConnectionID ConnectionID
	ServerID     Snowflake
	Name         string
	IconHash     *string
	Owner        bool
	RefreshedAt  time.Time
}

type Channel struct {
	ConnectionID            ConnectionID
	ServerID                Snowflake
	ChannelID               Snowflake
	ParentID                *Snowflake
	Name                    string
	ChannelType             int32
	Position                int32
	EffectiveBotPermissions Permission
	EveryoneCanView         bool
	ViewerRoleIDs           []Snowflake
	ViewerUserIDs           []Snowflake
	AudienceOverwriteSHA256 [32]byte
	RefreshedAt             time.Time
}

type Role struct {
	ConnectionID ConnectionID
	ServerID     Snowflake
	RoleID       Snowflake
	Name         string
	Position     int32
	RefreshedAt  time.Time
}

type Invocation struct {
	Binding              Binding
	Trigger              TriggerType
	InvocationChannelID  Snowflake
	InvocationParentID   *Snowflake
	AgentKey             string
	AgentVersionID       agents.VersionID
	AgentResourceVersion int32
	EffectiveAccess      AccessPolicy
	Corpus               []InvocationCorpusMember
	Subject              string
	ConnectionVersion    int32
	CredentialID         credentials.ID
	CredentialVersion    int32
	CallerRoleIDs        []Snowflake
	CapturedListen       ChannelCheck
	CapturedReply        ChannelCheck
}

type InvocationCorpusMember struct {
	Position             int32
	KnowledgeBaseID      agents.KnowledgeBaseID
	KnowledgeBaseVersion int32
	AccessPolicy         AccessPolicy
}

func (invocation Invocation) Authorize(_ context.Context, scope agents.AuthorizationScope) error {
	if scope.Origin != agents.OriginDiscord || scope.Subject != invocation.Subject ||
		scope.AgentID != invocation.Binding.AgentID || scope.AgentKey != invocation.AgentKey ||
		scope.AgentVersionID != invocation.AgentVersionID ||
		scope.AgentResourceVersion != invocation.AgentResourceVersion ||
		scope.EffectiveAccess != invocation.EffectiveAccess || len(scope.Corpus) != len(invocation.Corpus) {
		return agents.ErrExecutionForbidden
	}
	for index, member := range invocation.Corpus {
		current := scope.Corpus[index]
		if current.Position != member.Position || current.KnowledgeBaseID != member.KnowledgeBaseID ||
			current.KnowledgeBaseVersion != member.KnowledgeBaseVersion || current.AccessPolicy != member.AccessPolicy {
			return agents.ErrExecutionForbidden
		}
	}
	return nil
}

var (
	ErrNotFound = errors.New("Discord resource not found")
	ErrConflict = errors.New("Discord resource conflicts with current state")
	ErrPolicy   = errors.New("Discord binding policy is invalid")
)

func ValidateCreateConnection(command CreateConnection) error {
	return validateDisplayName(command.DisplayName)
}

func ValidateUpdateConnection(command UpdateConnection) error {
	if command.ExpectedVersion <= 0 {
		return ErrConflict
	}
	if command.Lifecycle != ConnectionEnabled && command.Lifecycle != ConnectionDisabled {
		return errors.New("Discord connection lifecycle is invalid")
	}
	return validateDisplayName(command.DisplayName)
}

func ValidateRotateToken(command RotateToken) error {
	if command.ExpectedVersion <= 0 {
		return ErrConflict
	}
	return nil
}

func ValidateRatePolicy(policy RatePolicy) error {
	if policy.Requests < 1 || policy.Requests > 100 ||
		policy.WindowSeconds < 1 || policy.WindowSeconds > 86_400 {
		return errors.New("Discord rate policy is invalid")
	}
	return nil
}

func ValidateBindingConfiguration(config BindingConfiguration) error {
	if _, err := ParseSnowflake(string(config.ServerID)); err != nil {
		return err
	}
	if _, err := ParseSnowflake(string(config.ListenChannelID)); err != nil {
		return err
	}
	if config.ReplyChannelID != nil {
		if _, err := ParseSnowflake(string(*config.ReplyChannelID)); err != nil {
			return err
		}
	}
	if (config.ReplyPolicy == ReplySelectedChannel) != (config.ReplyChannelID != nil) {
		return errors.New("selected-channel reply requires exactly one reply channel")
	}
	if config.AgentID == (agents.AgentID{}) {
		return errors.New("Discord Agent is required")
	}
	if len(config.Triggers) < 1 || len(config.Triggers) > 2 {
		return errors.New("Discord triggers are invalid")
	}
	seenTriggers := make(map[TriggerType]struct{}, len(config.Triggers))
	for _, trigger := range config.Triggers {
		if trigger != TriggerMention && trigger != TriggerSlashCommand {
			return errors.New("Discord trigger is invalid")
		}
		if _, exists := seenTriggers[trigger]; exists {
			return errors.New("Discord triggers are invalid")
		}
		seenTriggers[trigger] = struct{}{}
	}
	if config.ReplyPolicy != ReplySameChannel && config.ReplyPolicy != ReplyThread && config.ReplyPolicy != ReplySelectedChannel {
		return errors.New("Discord reply policy is invalid")
	}
	if len(config.AllowedRoleIDs) > 100 || len(config.AllowedUserIDs) > 100 ||
		hasDuplicateSnowflake(config.AllowedRoleIDs) || hasDuplicateSnowflake(config.AllowedUserIDs) {
		return errors.New("Discord invocation allowlist is invalid")
	}
	for _, values := range [][]Snowflake{config.AllowedRoleIDs, config.AllowedUserIDs} {
		for _, value := range values {
			if _, err := ParseSnowflake(string(value)); err != nil {
				return err
			}
		}
	}
	return ValidateRatePolicy(config.RatePolicy)
}

func ValidateCreateBinding(command CreateBinding) error {
	return ValidateBindingConfiguration(command.Configuration)
}

func ValidateUpdateBinding(command UpdateBinding) error {
	if command.ExpectedVersion <= 0 {
		return ErrConflict
	}
	return ValidateBindingConfiguration(command.Configuration)
}

func ValidateBindingPolicy(
	binding Binding,
	access AccessPolicy,
	listen ChannelCheck,
	reply ChannelCheck,
) error {
	if listen.ServerID != binding.ServerID || reply.ServerID != binding.ServerID {
		return fmt.Errorf("%w: Discord channels must belong to the selected server", ErrPolicy)
	}
	if listen.ChannelID != binding.ListenChannelID {
		return fmt.Errorf("%w: listen-channel metadata does not match the binding", ErrPolicy)
	}
	if !supportedChannelType(listen.ChannelType) || !supportedChannelType(reply.ChannelType) {
		return fmt.Errorf("%w: Discord channel type is not supported", ErrPolicy)
	}
	required := requiredReplyPermissions(binding, reply)
	if reply.EffectiveBotPermissions&required != required {
		return fmt.Errorf("%w: Discord bot lacks a required reply permission", ErrPolicy)
	}
	listenRequired := PermissionViewChannel | PermissionReadMessageHistory
	if listen.EffectiveBotPermissions&listenRequired != listenRequired {
		return fmt.Errorf("%w: Discord bot cannot read the listen channel", ErrPolicy)
	}
	if access == AccessRestricted {
		if reply.EveryoneCanView {
			return fmt.Errorf("%w: restricted Agents cannot reply to @everyone", ErrPolicy)
		}
		if len(binding.AllowedRoleIDs) == 0 && len(binding.AllowedUserIDs) == 0 {
			return fmt.Errorf("%w: restricted Agents require an invocation allowlist", ErrPolicy)
		}
	}
	return nil
}

func requiredReplyPermissions(binding Binding, reply ChannelCheck) Permission {
	required := BasePermissions
	if reply.ChannelType == 11 {
		required |= PermissionSendMessagesInThread
	} else if binding.ReplyPolicy == ReplyThread {
		required |= ThreadPermissions
	}
	return required
}

func InstallationURL(applicationID Snowflake, threads bool) (string, error) {
	if _, err := ParseSnowflake(string(applicationID)); err != nil {
		return "", err
	}
	permissions := BasePermissions
	if threads {
		permissions |= ThreadPermissions
	}
	query := "client_id=" + url.QueryEscape(string(applicationID)) +
		"&permissions=" + strconv.FormatInt(int64(permissions), 10) +
		"&integration_type=0&scope=" + url.QueryEscape("bot applications.commands")
	return "https://discord.com/oauth2/authorize?" + query, nil
}

func BindingPermits(binding Binding, userID Snowflake, roleIDs map[Snowflake]struct{}) bool {
	for _, allowed := range binding.AllowedUserIDs {
		if allowed == userID {
			return true
		}
	}
	for _, allowed := range binding.AllowedRoleIDs {
		if _, ok := roleIDs[allowed]; ok {
			return true
		}
	}
	return false
}

func DisplayKey(value string) string { return cases.Fold().String(value) }

func validateDisplayName(value string) error {
	if !utf8.ValidString(value) || value != strings.TrimFunc(value, pythonWhitespace) ||
		utf8.RuneCountInString(value) < 1 || utf8.RuneCountInString(value) > 255 {
		return errors.New("Discord connection display name is invalid")
	}
	return nil
}

func pythonWhitespace(character rune) bool {
	return unicode.IsSpace(character) || character >= '\x1c' && character <= '\x1f'
}

func hasDuplicateSnowflake(values []Snowflake) bool {
	seen := make(map[Snowflake]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}

func supportedChannelType(value int32) bool { return value == 0 || value == 11 }

func validPermission(value Permission) bool { return value >= 0 && int64(value) <= math.MaxInt64 }
