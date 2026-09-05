package discord

import (
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/agents"
)

func TestRestrictedBindingRequiresPrivateAudienceAllowlistAndPermissions(t *testing.T) {
	value := policyTestBinding(ReplyThread, true)
	listen, reply := policyChecks(value, false, BasePermissions|ThreadPermissions)
	if err := ValidateBindingPolicy(value, AccessRestricted, listen, reply); err != nil {
		t.Fatal(err)
	}
	_, publicReply := policyChecks(value, true, BasePermissions|ThreadPermissions)
	if err := ValidateBindingPolicy(value, AccessRestricted, listen, publicReply); !errors.Is(err, ErrPolicy) {
		t.Fatalf("public reply error = %v", err)
	}
	withoutAllowlist := policyTestBinding(ReplySameChannel, false)
	noAllowListen, noAllowReply := policyChecks(withoutAllowlist, false, BasePermissions)
	if err := ValidateBindingPolicy(withoutAllowlist, AccessRestricted, noAllowListen, noAllowReply); !errors.Is(err, ErrPolicy) {
		t.Fatalf("allowlist error = %v", err)
	}
	_, missingThread := policyChecks(value, false, BasePermissions)
	if err := ValidateBindingPolicy(value, AccessRestricted, listen, missingThread); !errors.Is(err, ErrPolicy) {
		t.Fatalf("thread permission error = %v", err)
	}
}

func TestPublicBindingCanUseEveryoneWithoutAllowlist(t *testing.T) {
	value := policyTestBinding(ReplySameChannel, false)
	listen, reply := policyChecks(value, true, BasePermissions)
	if err := ValidateBindingPolicy(value, AccessPublic, listen, reply); err != nil {
		t.Fatal(err)
	}
}

func TestActualThreadDestinationsRequireThreadPermissions(t *testing.T) {
	for _, policy := range []ReplyPolicy{ReplySameChannel, ReplySelectedChannel, ReplyThread} {
		t.Run(string(policy), func(t *testing.T) {
			binding := policyTestBinding(policy, true)
			listen, reply := policyChecks(binding, false, BasePermissions)
			reply.ChannelType = 11
			if policy == ReplySameChannel || policy == ReplyThread {
				listen.ChannelType = 11
			}
			if err := ValidateBindingPolicy(binding, AccessRestricted, listen, reply); !errors.Is(err, ErrPolicy) {
				t.Fatalf("base-only thread destination error = %v", err)
			}
			reply.EffectiveBotPermissions |= PermissionSendMessagesInThread
			if policy == ReplySameChannel || policy == ReplyThread {
				listen.EffectiveBotPermissions |= PermissionSendMessagesInThread
			}
			if err := ValidateBindingPolicy(binding, AccessRestricted, listen, reply); err != nil {
				t.Fatalf("thread-capable destination error = %v", err)
			}
		})
	}
}

func TestInstallationURLContainsOnlyRequiredAuthority(t *testing.T) {
	value, err := InstallationURL(Snowflake("123"), true)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Scheme != "https" || parsed.Host != "discord.com" ||
		query.Get("client_id") != "123" || query.Get("scope") != "bot applications.commands" ||
		query.Get("permissions") != "309237730304" || query.Get("integration_type") != "0" ||
		len(query) != 4 {
		t.Fatalf("installation URL = %s", value)
	}
}

func TestTriggerSetAndSnowflakeValidation(t *testing.T) {
	binding := Binding{Triggers: []TriggerType{TriggerMention, TriggerSlashCommand}}
	if !binding.HasTrigger(TriggerMention) || !binding.HasTrigger(TriggerSlashCommand) || binding.HasTrigger(TriggerType("OTHER")) {
		t.Fatal("trigger set is wrong")
	}
	for _, value := range []string{"1", "18446744073709551615"} {
		if _, err := ParseSnowflake(value); err != nil {
			t.Errorf("valid snowflake %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "0", "01", "-1", "１２３", "18446744073709551616"} {
		if _, err := ParseSnowflake(value); err == nil {
			t.Errorf("invalid snowflake accepted: %q", value)
		}
	}
}

func TestBindingConfigurationRejectsDuplicateOrMismatchedAudience(t *testing.T) {
	config := policyTestBinding(ReplySelectedChannel, true)
	candidate := BindingConfiguration{
		ConnectionID: config.ConnectionID, ServerID: config.ServerID,
		ListenChannelID: config.ListenChannelID, AgentID: config.AgentID,
		Triggers: config.Triggers, ReplyPolicy: config.ReplyPolicy,
		ReplyChannelID: config.ReplyChannelID, AllowedRoleIDs: []Snowflake{"200", "200"},
		RatePolicy: DefaultRatePolicy(),
	}
	if err := ValidateBindingConfiguration(candidate); err == nil {
		t.Fatal("duplicate allowlist was accepted")
	}
	candidate.AllowedRoleIDs = []Snowflake{"200"}
	candidate.ReplyChannelID = nil
	if err := ValidateBindingConfiguration(candidate); err == nil {
		t.Fatal("selected reply without channel was accepted")
	}
}

func policyTestBinding(replyPolicy ReplyPolicy, allow bool) Binding {
	replyID := (*Snowflake)(nil)
	if replyPolicy == ReplySelectedChannel {
		value := Snowflake("102")
		replyID = &value
	}
	roles := []Snowflake{}
	if allow {
		roles = []Snowflake{"200"}
	}
	return Binding{
		ID: BindingID{1}, ConnectionID: ConnectionID{2}, ServerID: "100",
		ListenChannelID: "101", AgentID: agents.AgentID{3},
		Triggers: []TriggerType{TriggerMention, TriggerSlashCommand}, ReplyPolicy: replyPolicy, ReplyChannelID: replyID,
		AllowedRoleIDs: roles, AllowedUserIDs: []Snowflake{}, RatePolicy: DefaultRatePolicy(),
		Health: BindingDraft, Version: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func policyChecks(value Binding, everyone bool, permissions Permission) (ChannelCheck, ChannelCheck) {
	listen := ChannelCheck{
		ServerID: value.ServerID, ChannelID: value.ListenChannelID,
		ChannelType: 0, EffectiveBotPermissions: BasePermissions,
	}
	replyID := value.ListenChannelID
	if value.ReplyChannelID != nil {
		replyID = *value.ReplyChannelID
	}
	reply := ChannelCheck{
		ServerID: value.ServerID, ChannelID: replyID, ChannelType: 0,
		EffectiveBotPermissions: permissions, EveryoneCanView: everyone,
	}
	return listen, reply
}
