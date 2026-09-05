package discord

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/cyr1en/ref0/internal/agents"
)

const (
	unconfiguredInteractionMessage = "This command is not configured for this channel."
	changedBindingMessage          = "This channel binding changed while the answer was being prepared, so no answer was posted."
	unavailableReplyChannelMessage = "The configured reply channel is unavailable."
)

var answerLink = regexp.MustCompile(`\[([^\]]+)\]\((?:https?|repo|file)://[^)]+\)`)

type InvocationService interface {
	AuthorizeInvocation(context.Context, GatewayCapture, Snowflake, Snowflake, *Snowflake, Snowflake, map[Snowflake]struct{}, bool) (Invocation, error)
	ConsumeRate(context.Context, Binding, Snowflake) (bool, error)
}

type AgentExecutor interface {
	Execute(context.Context, agents.ExecuteRequest, agents.Authorizer) (agents.ExecuteResult, error)
}

type DiscordAnswerService interface {
	InvocationService
	ContextService
	DeliveryAuthorizer
}

type gatewaySession interface {
	Channel(string, ...discordgo.RequestOption) (*discordgo.Channel, error)
	ChannelTyping(string, ...discordgo.RequestOption) error
	ChannelMessageSendComplex(string, *discordgo.MessageSend, ...discordgo.RequestOption) (*discordgo.Message, error)
	MessageThreadStartComplex(string, string, *discordgo.ThreadStart, ...discordgo.RequestOption) (*discordgo.Channel, error)
	ThreadStartComplex(string, *discordgo.ThreadStart, ...discordgo.RequestOption) (*discordgo.Channel, error)
	InteractionRespond(*discordgo.Interaction, *discordgo.InteractionResponse, ...discordgo.RequestOption) error
	FollowupMessageCreate(*discordgo.Interaction, bool, *discordgo.WebhookParams, ...discordgo.RequestOption) (*discordgo.Message, error)
}

type AnswerHandler struct {
	service  DiscordAnswerService
	executor AgentExecutor
}

func NewAnswerHandler(service DiscordAnswerService, executor AgentExecutor) (*AnswerHandler, error) {
	if service == nil || executor == nil {
		return nil, fmt.Errorf("Discord answer dependencies are incomplete")
	}
	return &AnswerHandler{service: service, executor: executor}, nil
}

func (handler *AnswerHandler) HandleMention(
	ctx context.Context,
	capture GatewayCapture,
	session *discordgo.Session,
	event *discordgo.MessageCreate,
	question string,
) {
	if session == nil {
		return
	}
	handler.handleMention(ctx, capture, session, event, question)
}

func (handler *AnswerHandler) handleMention(
	ctx context.Context,
	capture GatewayCapture,
	session gatewaySession,
	event *discordgo.MessageCreate,
	question string,
) {
	if event == nil || event.Message == nil || event.Author == nil || event.GuildID == "" {
		return
	}
	routeChannelID, parentChannelID, ok := authorizationChannel(session, event.GuildID, event.ChannelID)
	if !ok {
		return
	}
	invocation, ok := handler.authorize(
		ctx, capture, event.GuildID, routeChannelID, parentChannelID, event.Author, event.Member, false,
	)
	if !ok {
		return
	}
	allowed, err := handler.service.ConsumeRate(ctx, invocation.Binding, Snowflake(event.Author.ID))
	if err != nil {
		return
	}
	if !allowed {
		content := fmt.Sprintf(
			"You're asking questions a bit too quickly. Please try again in %d second(s).",
			invocation.Binding.RatePolicy.WindowSeconds,
		)
		_, _ = session.ChannelMessageSendComplex(event.ChannelID, replyContent(event.Message, content))
		return
	}
	destination, ok := messageDestination(session, event, invocation.Binding)
	if !ok {
		return
	}
	_ = session.ChannelTyping(destination)
	execution, err := handler.execute(ctx, invocation, Snowflake(event.Author.ID), Snowflake(destination), question)
	if err != nil {
		return
	}
	permit := DeliveryPermit{
		Invocation: invocation, RunID: execution.RunID, DestinationID: Snowflake(destination),
	}
	reauthorize := func() error { return handler.service.ReauthorizeDelivery(ctx, permit) }
	result := citationSafe(execution, invocation.EffectiveAccess)
	if invocation.Binding.ReplyPolicy == ReplySameChannel {
		err = deliverReply(session, event.Message, result, reauthorize)
	} else {
		err = deliverChannel(session, destination, result, reauthorize)
	}
	if err == nil {
		_ = handler.service.AppendContext(ctx, invocationContextKey(invocation, Snowflake(event.Author.ID), Snowflake(destination)), question, result.Markdown)
	}
}

func (handler *AnswerHandler) HandleInteraction(
	ctx context.Context,
	capture GatewayCapture,
	session *discordgo.Session,
	event *discordgo.InteractionCreate,
	question string,
) {
	if session == nil {
		return
	}
	handler.handleInteraction(ctx, capture, session, event, question)
}

func (handler *AnswerHandler) handleInteraction(
	ctx context.Context,
	capture GatewayCapture,
	session gatewaySession,
	event *discordgo.InteractionCreate,
	question string,
) {
	if event == nil || event.Interaction == nil || event.GuildID == "" || event.ChannelID == "" {
		return
	}
	user, member := interactionUser(event.Interaction)
	if user == nil {
		return
	}
	routeChannelID, parentChannelID, ok := authorizationChannel(session, event.GuildID, event.ChannelID)
	if !ok {
		_ = respondInteraction(session, event.Interaction, unconfiguredInteractionMessage, true, false)
		return
	}
	invocation, ok := handler.authorize(
		ctx, capture, event.GuildID, routeChannelID, parentChannelID, user, member, true,
	)
	if !ok {
		_ = respondInteraction(session, event.Interaction, unconfiguredInteractionMessage, true, false)
		return
	}
	allowed, err := handler.service.ConsumeRate(ctx, invocation.Binding, Snowflake(user.ID))
	if err != nil {
		return
	}
	if !allowed {
		_ = respondInteraction(session, event.Interaction, RateLimitMessage, true, false)
		return
	}
	sameChannel := invocation.Binding.ReplyPolicy == ReplySameChannel
	ephemeral := invocation.EffectiveAccess == AccessRestricted && sameChannel
	if err := respondInteraction(session, event.Interaction, "", ephemeral, true); err != nil {
		return
	}
	destination, ok := interactionDestination(session, event.Interaction, invocation.Binding, user, member)
	if !ok {
		_ = followupContent(session, event.Interaction, unavailableReplyChannelMessage, true)
		return
	}
	_ = session.ChannelTyping(destination)
	execution, err := handler.execute(ctx, invocation, Snowflake(user.ID), Snowflake(destination), question)
	if err != nil {
		_ = followupContent(session, event.Interaction, FallbackMessage, true)
		return
	}
	permit := DeliveryPermit{
		Invocation: invocation, RunID: execution.RunID, DestinationID: Snowflake(destination),
	}
	reauthorize := func() error { return handler.service.ReauthorizeDelivery(ctx, permit) }
	result := citationSafe(execution, invocation.EffectiveAccess)
	if sameChannel {
		if err = deliverInteraction(session, event.Interaction, result, ephemeral, reauthorize); err == nil {
			_ = handler.service.AppendContext(ctx, invocationContextKey(invocation, Snowflake(user.ID), Snowflake(destination)), question, result.Markdown)
		} else if errors.Is(err, ErrConflict) {
			_ = followupContent(session, event.Interaction, changedBindingMessage, true)
		}
		return
	}
	if err := deliverChannel(session, destination, result, reauthorize); err != nil {
		if errors.Is(err, ErrConflict) {
			_ = followupContent(session, event.Interaction, changedBindingMessage, true)
		}
		return
	}
	if err = reauthorize(); err != nil {
		return
	}
	if err = followupContent(session, event.Interaction, "Answer posted in <#"+destination+">.", true); err != nil {
		return
	}
	_ = handler.service.AppendContext(ctx, invocationContextKey(invocation, Snowflake(user.ID), Snowflake(destination)), question, result.Markdown)
}

func authorizationChannel(session gatewaySession, guildID, eventChannelID string) (string, *Snowflake, bool) {
	channel, err := session.Channel(eventChannelID)
	if err != nil || channel == nil || channel.ID != eventChannelID || channel.GuildID != guildID {
		return "", nil, false
	}
	switch channel.Type {
	case discordgo.ChannelTypeGuildText:
		return eventChannelID, nil, true
	case discordgo.ChannelTypeGuildPublicThread:
		parentID, parseErr := ParseSnowflake(channel.ParentID)
		if parseErr != nil {
			return "", nil, false
		}
		return eventChannelID, &parentID, true
	default:
		return "", nil, false
	}
}

func (handler *AnswerHandler) authorize(
	ctx context.Context,
	capture GatewayCapture,
	guildID, channelID string,
	parentChannelID *Snowflake,
	user *discordgo.User,
	member *discordgo.Member,
	slash bool,
) (Invocation, bool) {
	server, err := ParseSnowflake(guildID)
	if err != nil || user == nil {
		return Invocation{}, false
	}
	channel, err := ParseSnowflake(channelID)
	if err != nil {
		return Invocation{}, false
	}
	userID, err := ParseSnowflake(user.ID)
	if err != nil {
		return Invocation{}, false
	}
	roles, ok := memberRoles(member, server)
	if !ok {
		return Invocation{}, false
	}
	invocation, err := handler.service.AuthorizeInvocation(
		ctx, capture, server, channel, parentChannelID, userID, roles, slash,
	)
	return invocation, err == nil
}

func memberRoles(member *discordgo.Member, guildID Snowflake) (map[Snowflake]struct{}, bool) {
	roles := map[Snowflake]struct{}{}
	if member == nil {
		return roles, true
	}
	for _, raw := range member.Roles {
		if raw == string(guildID) {
			continue
		}
		role, err := ParseSnowflake(raw)
		if err != nil {
			return nil, false
		}
		roles[role] = struct{}{}
	}
	return roles, true
}

func interactionUser(interaction *discordgo.Interaction) (*discordgo.User, *discordgo.Member) {
	if interaction.Member != nil && interaction.Member.User != nil {
		return interaction.Member.User, interaction.Member
	}
	return interaction.User, nil
}

func messageDestination(session gatewaySession, event *discordgo.MessageCreate, binding Binding) (string, bool) {
	switch binding.ReplyPolicy {
	case ReplySameChannel:
		return event.ChannelID, true
	case ReplySelectedChannel:
		if binding.ReplyChannelID == nil {
			return "", false
		}
		channel, err := session.Channel(string(*binding.ReplyChannelID))
		return string(*binding.ReplyChannelID), err == nil && channel != nil && channel.GuildID == event.GuildID
	case ReplyThread:
		channel, err := session.Channel(event.ChannelID)
		if err != nil || channel == nil {
			return "", false
		}
		if isThread(channel.Type) {
			return channel.ID, true
		}
		thread, err := session.MessageThreadStartComplex(event.ChannelID, event.ID, &discordgo.ThreadStart{
			Name: boundedThreadName(displayName(event.Author, event.Member)),
		})
		return channelID(thread, err)
	default:
		return "", false
	}
}

func interactionDestination(
	session gatewaySession,
	interaction *discordgo.Interaction,
	binding Binding,
	user *discordgo.User,
	member *discordgo.Member,
) (string, bool) {
	switch binding.ReplyPolicy {
	case ReplySameChannel:
		return interaction.ChannelID, true
	case ReplySelectedChannel:
		if binding.ReplyChannelID == nil {
			return "", false
		}
		channel, err := session.Channel(string(*binding.ReplyChannelID))
		return string(*binding.ReplyChannelID), err == nil && channel != nil && channel.GuildID == interaction.GuildID
	case ReplyThread:
		channel, err := session.Channel(interaction.ChannelID)
		if err != nil || channel == nil {
			return "", false
		}
		if isThread(channel.Type) {
			return channel.ID, true
		}
		thread, err := session.ThreadStartComplex(interaction.ChannelID, &discordgo.ThreadStart{
			Name: boundedThreadName(displayName(user, member)),
			Type: discordgo.ChannelTypeGuildPublicThread,
		})
		return channelID(thread, err)
	default:
		return "", false
	}
}

func channelID(channel *discordgo.Channel, err error) (string, bool) {
	return func() string {
		if channel == nil {
			return ""
		}
		return channel.ID
	}(), err == nil && channel != nil && channel.ID != ""
}

func isThread(channelType discordgo.ChannelType) bool {
	return channelType == discordgo.ChannelTypeGuildNewsThread ||
		channelType == discordgo.ChannelTypeGuildPublicThread ||
		channelType == discordgo.ChannelTypeGuildPrivateThread
}

func displayName(user *discordgo.User, member *discordgo.Member) string {
	if member != nil && strings.TrimSpace(member.Nick) != "" {
		return member.Nick
	}
	if user != nil && strings.TrimSpace(user.GlobalName) != "" {
		return user.GlobalName
	}
	if user != nil && strings.TrimSpace(user.Username) != "" {
		return user.Username
	}
	return "Discord user"
}

func boundedThreadName(name string) string {
	runes := []rune("Answer for " + name)
	if len(runes) > 100 {
		runes = runes[:100]
	}
	return string(runes)
}

func (handler *AnswerHandler) execute(
	ctx context.Context,
	invocation Invocation,
	userID Snowflake,
	destinationID Snowflake,
	question string,
) (agents.ExecuteResult, error) {
	messages, err := handler.service.LoadContext(ctx, invocationContextKey(invocation, userID, destinationID))
	if err != nil {
		return agents.ExecuteResult{}, err
	}
	messages = append(messages, agents.Message{Role: agents.RoleUser, Content: question})
	return handler.executor.Execute(ctx, agents.ExecuteRequest{
		Selector: "agent:" + invocation.AgentKey, Origin: agents.OriginDiscord,
		Subject: invocation.Subject, Messages: messages,
	}, invocation)
}

func invocationContextKey(invocation Invocation, userID Snowflake, destinationID Snowflake) ContextKey {
	return ContextKey{
		BindingID: invocation.Binding.ID, AgentID: invocation.Binding.AgentID,
		AgentVersionID: invocation.AgentVersionID, UserID: userID, DestinationID: destinationID,
	}
}

func citationSafe(result agents.ExecuteResult, access AccessPolicy) agents.ExecuteResult {
	if access == AccessRestricted {
		result.Markdown = answerLink.ReplaceAllString(result.Markdown, "$1")
	}
	if len(result.Citations) == 0 {
		return result
	}
	lines := make([]string, 0, len(result.Citations))
	for _, citation := range result.Citations {
		location := citation.Label
		if citation.Path != nil {
			location = *citation.Path
		}
		if citation.StartLine != nil {
			end := *citation.StartLine
			if citation.EndLine != nil {
				end = *citation.EndLine
			}
			lineRange := fmt.Sprintf("%d", *citation.StartLine)
			if end != *citation.StartLine {
				lineRange = fmt.Sprintf("%d-%d", *citation.StartLine, end)
			}
			location += ":" + lineRange
		}
		revision := ""
		if citation.SourceRevisionID != nil {
			revision = " · " + *citation.SourceRevisionID
		}
		if access == AccessPublic &&
			(strings.HasPrefix(citation.Resource, "https://") || strings.HasPrefix(citation.Resource, "http://")) {
			lines = append(lines, fmt.Sprintf("- [%s](%s) — %s%s", citation.Label, citation.Resource, location, revision))
		} else {
			lines = append(lines, fmt.Sprintf("- **%s** — %s%s", citation.Label, location, revision))
		}
	}
	result.Markdown = strings.TrimRight(result.Markdown, " \t\r\n") + "\n\n### Sources\n" + strings.Join(lines, "\n")
	return result
}

func replyContent(reference *discordgo.Message, content string) *discordgo.MessageSend {
	return &discordgo.MessageSend{Content: content, Reference: reference.Reference(), AllowedMentions: suppressedMentions()}
}

func deliverReply(
	session gatewaySession,
	reference *discordgo.Message,
	result agents.ExecuteResult,
	reauthorize func() error,
) error {
	embeds, err := resultEmbeds(result)
	if err != nil {
		return err
	}
	if len(embeds) == 0 {
		if err = reauthorize(); err != nil {
			return err
		}
		_, err = session.ChannelMessageSendComplex(reference.ChannelID, replyContent(reference, FallbackMessage))
		return err
	}
	previous := reference
	for _, embed := range embeds {
		if err = reauthorize(); err != nil {
			return err
		}
		message, sendErr := session.ChannelMessageSendComplex(reference.ChannelID, &discordgo.MessageSend{
			Embeds: []*discordgo.MessageEmbed{embed}, Reference: previous.Reference(),
			AllowedMentions: suppressedMentions(),
		})
		if sendErr != nil {
			return sendErr
		}
		previous = message
	}
	return nil
}

func deliverChannel(
	session gatewaySession,
	channelID string,
	result agents.ExecuteResult,
	reauthorize func() error,
) error {
	embeds, err := resultEmbeds(result)
	if err != nil {
		return err
	}
	if len(embeds) == 0 {
		if err = reauthorize(); err != nil {
			return err
		}
		_, err = session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Content: FallbackMessage, AllowedMentions: suppressedMentions(),
		})
		return err
	}
	for _, embed := range embeds {
		if err = reauthorize(); err != nil {
			return err
		}
		if _, err = session.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
			Embeds: []*discordgo.MessageEmbed{embed}, AllowedMentions: suppressedMentions(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func deliverInteraction(
	session gatewaySession,
	interaction *discordgo.Interaction,
	result agents.ExecuteResult,
	ephemeral bool,
	reauthorize func() error,
) error {
	embeds, err := resultEmbeds(result)
	if err != nil {
		return err
	}
	if len(embeds) == 0 {
		if err = reauthorize(); err != nil {
			return err
		}
		return followupContent(session, interaction, FallbackMessage, ephemeral)
	}
	flags := discordgo.MessageFlags(0)
	if ephemeral {
		flags = discordgo.MessageFlagsEphemeral
	}
	for _, embed := range embeds {
		if err = reauthorize(); err != nil {
			return err
		}
		if _, err = session.FollowupMessageCreate(interaction, false, &discordgo.WebhookParams{
			Embeds: []*discordgo.MessageEmbed{embed}, Flags: flags, AllowedMentions: suppressedMentions(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func resultEmbeds(result agents.ExecuteResult) ([]*discordgo.MessageEmbed, error) {
	if result.Status == agents.CompletionFailed || result.Markdown == "" {
		return nil, nil
	}
	return AnswerEmbeds(result.Markdown, time.Now())
}

func respondInteraction(session gatewaySession, interaction *discordgo.Interaction, content string, ephemeral, deferred bool) error {
	flags := discordgo.MessageFlags(0)
	if ephemeral {
		flags = discordgo.MessageFlagsEphemeral
	}
	responseType := discordgo.InteractionResponseChannelMessageWithSource
	if deferred {
		responseType = discordgo.InteractionResponseDeferredChannelMessageWithSource
	}
	return session.InteractionRespond(interaction, &discordgo.InteractionResponse{
		Type: responseType,
		Data: &discordgo.InteractionResponseData{Content: content, Flags: flags, AllowedMentions: suppressedMentions()},
	})
}

func followupContent(session gatewaySession, interaction *discordgo.Interaction, content string, ephemeral bool) error {
	flags := discordgo.MessageFlags(0)
	if ephemeral {
		flags = discordgo.MessageFlagsEphemeral
	}
	_, err := session.FollowupMessageCreate(interaction, false, &discordgo.WebhookParams{
		Content: content, Flags: flags, AllowedMentions: suppressedMentions(),
	})
	return err
}

func suppressedMentions() *discordgo.MessageAllowedMentions {
	return &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{}}
}
