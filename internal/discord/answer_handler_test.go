package discord

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/cyr1en/ref0/internal/agents"
	"github.com/cyr1en/ref0/internal/credentials"
)

type appendedContext struct {
	key                 ContextKey
	user, assistantText string
}

func testGatewayCapture(id ConnectionID) GatewayCapture {
	return GatewayCapture{
		ConnectionID: id, ConnectionVersion: 1,
		CredentialID: credentials.ID{id[0], 1}, CredentialVersion: 1,
	}
}

type fakeInvocationService struct {
	invocation        Invocation
	authorizeErr      error
	allowed           bool
	deliveryErr       error
	deliveryErrors    []error
	context           []agents.Message
	loadErr           error
	appendErr         error
	authorizes        int
	consumes          int
	reauthorizes      int
	appended          []appendedContext
	deliveryPermit    DeliveryPermit
	authorizedChannel Snowflake
	authorizedParent  *Snowflake
	authorizedSlash   bool
}

func (service *fakeInvocationService) AuthorizeInvocation(
	_ context.Context, _ GatewayCapture, _ Snowflake, channelID Snowflake, parentID *Snowflake,
	_ Snowflake, _ map[Snowflake]struct{}, slash bool,
) (Invocation, error) {
	service.authorizes++
	service.authorizedChannel, service.authorizedParent, service.authorizedSlash = channelID, parentID, slash
	if service.authorizeErr != nil {
		return Invocation{}, service.authorizeErr
	}
	return service.invocation, nil
}

func (service *fakeInvocationService) ConsumeRate(context.Context, Binding, Snowflake) (bool, error) {
	service.consumes++
	return service.allowed, nil
}

func (service *fakeInvocationService) LoadContext(context.Context, ContextKey) ([]agents.Message, error) {
	return append([]agents.Message(nil), service.context...), service.loadErr
}

func (service *fakeInvocationService) AppendContext(_ context.Context, key ContextKey, user, assistant string) error {
	service.appended = append(service.appended, appendedContext{key: key, user: user, assistantText: assistant})
	return service.appendErr
}

func (service *fakeInvocationService) ReauthorizeDelivery(_ context.Context, permit DeliveryPermit) error {
	service.reauthorizes++
	service.deliveryPermit = permit
	if service.reauthorizes <= len(service.deliveryErrors) {
		return service.deliveryErrors[service.reauthorizes-1]
	}
	return service.deliveryErr
}

type fakeAgentExecutor struct {
	result     agents.ExecuteResult
	err        error
	request    agents.ExecuteRequest
	authorizer agents.Authorizer
	calls      int
}

func (executor *fakeAgentExecutor) Execute(
	_ context.Context, request agents.ExecuteRequest, authorizer agents.Authorizer,
) (agents.ExecuteResult, error) {
	executor.calls++
	executor.request = request
	executor.authorizer = authorizer
	return executor.result, executor.err
}

type sentMessage struct {
	channel string
	data    *discordgo.MessageSend
}

type fakeGatewaySession struct {
	channels      map[string]*discordgo.Channel
	typing        []string
	sent          []sentMessage
	responses     []*discordgo.InteractionResponse
	followups     []*discordgo.WebhookParams
	messageThread *discordgo.Channel
	plainThread   *discordgo.Channel
	sendErr       error
	followupErr   error
}

func (session *fakeGatewaySession) Channel(id string, _ ...discordgo.RequestOption) (*discordgo.Channel, error) {
	channel := session.channels[id]
	if channel == nil && session.channels == nil {
		return &discordgo.Channel{ID: id, GuildID: "300", Type: discordgo.ChannelTypeGuildText}, nil
	}
	if channel == nil {
		return nil, errors.New("missing channel")
	}
	return channel, nil
}

func (session *fakeGatewaySession) ChannelTyping(id string, _ ...discordgo.RequestOption) error {
	session.typing = append(session.typing, id)
	return nil
}

func (session *fakeGatewaySession) ChannelMessageSendComplex(id string, data *discordgo.MessageSend, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	session.sent = append(session.sent, sentMessage{channel: id, data: data})
	if session.sendErr != nil {
		return nil, session.sendErr
	}
	return &discordgo.Message{ID: "901", ChannelID: id, GuildID: "300"}, nil
}

func (session *fakeGatewaySession) MessageThreadStartComplex(string, string, *discordgo.ThreadStart, ...discordgo.RequestOption) (*discordgo.Channel, error) {
	if session.messageThread == nil {
		return nil, errors.New("thread unavailable")
	}
	return session.messageThread, nil
}

func (session *fakeGatewaySession) ThreadStartComplex(string, *discordgo.ThreadStart, ...discordgo.RequestOption) (*discordgo.Channel, error) {
	if session.plainThread == nil {
		return nil, errors.New("thread unavailable")
	}
	return session.plainThread, nil
}

func (session *fakeGatewaySession) InteractionRespond(_ *discordgo.Interaction, response *discordgo.InteractionResponse, _ ...discordgo.RequestOption) error {
	session.responses = append(session.responses, response)
	return nil
}

func (session *fakeGatewaySession) FollowupMessageCreate(_ *discordgo.Interaction, _ bool, data *discordgo.WebhookParams, _ ...discordgo.RequestOption) (*discordgo.Message, error) {
	session.followups = append(session.followups, data)
	if session.followupErr != nil {
		return nil, session.followupErr
	}
	return &discordgo.Message{ID: "950", ChannelID: "200", GuildID: "300"}, nil
}

func TestMentionExecutesCapturedAgentAndAppendsContextOnlyAfterDelivery(t *testing.T) {
	invocation := answerInvocation(AccessPublic, ReplySameChannel)
	service := &fakeInvocationService{
		invocation: invocation,
		allowed:    true,
		context: []agents.Message{
			{Role: agents.RoleUser, Content: "Earlier question"},
			{Role: agents.RoleAssistant, Content: "Earlier answer"},
		},
	}
	revision, path, start, end := "abc123", "docs/setup.md", 4, 7
	executor := &fakeAgentExecutor{result: agents.ExecuteResult{
		RunID: agents.RunID{9}, Status: agents.CompletionAnswered, Markdown: "Use the setup flow.",
		Citations: []agents.Citation{{
			Label: "Setup", Resource: "https://example.invalid/setup", SourceRevisionID: &revision,
			Path: &path, StartLine: &start, EndLine: &end,
		}},
	}}
	handler, err := NewAnswerHandler(service, executor)
	if err != nil {
		t.Fatal(err)
	}
	session := &fakeGatewaySession{}
	handler.handleMention(context.Background(), testGatewayCapture(ConnectionID{1}), session, mentionEvent(), "How?")

	if service.authorizes != 1 || service.consumes != 1 || service.reauthorizes != 1 || executor.calls != 1 {
		t.Fatalf("authorizes=%d consumes=%d reauthorizes=%d executes=%d", service.authorizes, service.consumes, service.reauthorizes, executor.calls)
	}
	if executor.request.Selector != "agent:helper" || executor.request.Origin != agents.OriginDiscord ||
		executor.request.Subject != "400" || len(executor.request.Messages) != 3 ||
		executor.request.Messages[2] != (agents.Message{Role: agents.RoleUser, Content: "How?"}) {
		t.Fatalf("execute request=%+v", executor.request)
	}
	authorizedInvocation, ok := executor.authorizer.(Invocation)
	if !ok || authorizedInvocation.Binding.ID != invocation.Binding.ID ||
		service.deliveryPermit.RunID != executor.result.RunID ||
		service.deliveryPermit.DestinationID != "200" {
		t.Fatalf("authorizer=%+v permit=%+v", executor.authorizer, service.deliveryPermit)
	}
	if len(session.typing) != 1 || session.typing[0] != "200" || len(session.sent) != 1 {
		t.Fatalf("typing=%v sent=%d", session.typing, len(session.sent))
	}
	message := session.sent[0].data
	if message.Reference == nil || message.Reference.MessageID != "100" || len(message.Embeds) != 1 ||
		message.AllowedMentions == nil || message.AllowedMentions.Parse == nil || len(message.AllowedMentions.Parse) != 0 ||
		!strings.Contains(message.Embeds[0].Description, "[Setup](https://example.invalid/setup)") ||
		!strings.Contains(message.Embeds[0].Description, "docs/setup.md:4-7 · abc123") {
		t.Fatalf("reply=%+v", message)
	}
	if len(service.appended) != 1 || service.appended[0].user != "How?" ||
		!strings.Contains(service.appended[0].assistantText, "Use the setup flow.") ||
		service.appended[0].key.AgentVersionID != invocation.AgentVersionID {
		t.Fatalf("appended=%+v", service.appended)
	}
}

func TestMultiChunkDeliveryReauthorizesEachChunkAndStopsOnMidDeliveryMutation(t *testing.T) {
	service := &fakeInvocationService{
		invocation: answerInvocation(AccessPublic, ReplySameChannel), allowed: true,
		deliveryErrors: []error{nil, ErrConflict},
	}
	executor := &fakeAgentExecutor{result: agents.ExecuteResult{
		RunID: agents.RunID{9}, Status: agents.CompletionAnswered, Markdown: strings.Repeat("verified text ", 2_000),
	}}
	handler, _ := NewAnswerHandler(service, executor)
	session := &fakeGatewaySession{}
	handler.handleMention(context.Background(), testGatewayCapture(ConnectionID{1}), session, mentionEvent(), "Question")
	if service.reauthorizes != 2 || len(session.sent) != 1 || len(service.appended) != 0 {
		t.Fatalf("reauthorizes=%d sent=%d appended=%d", service.reauthorizes, len(session.sent), len(service.appended))
	}
	if session.sent[0].data.AllowedMentions == nil || session.sent[0].data.AllowedMentions.Parse == nil ||
		len(session.sent[0].data.AllowedMentions.Parse) != 0 {
		t.Fatalf("allowed mentions=%+v", session.sent[0].data.AllowedMentions)
	}
}

func TestMentionSuppressesContextOnRateLimitReauthorizationOrSendFailure(t *testing.T) {
	invocation := answerInvocation(AccessPublic, ReplySameChannel)
	tests := []struct {
		name        string
		allowed     bool
		deliveryErr error
		sendErr     error
		wantExec    int
		wantSends   int
	}{
		{name: "rate limit", allowed: false, wantSends: 1},
		{name: "final reauthorization", allowed: true, deliveryErr: ErrConflict, wantExec: 1},
		{name: "delivery failure", allowed: true, sendErr: errors.New("send failed"), wantExec: 1, wantSends: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeInvocationService{invocation: invocation, allowed: test.allowed, deliveryErr: test.deliveryErr}
			executor := &fakeAgentExecutor{result: agents.ExecuteResult{
				RunID: agents.RunID{9}, Status: agents.CompletionAnswered, Markdown: "secret answer",
			}}
			handler, _ := NewAnswerHandler(service, executor)
			session := &fakeGatewaySession{sendErr: test.sendErr}
			handler.handleMention(context.Background(), testGatewayCapture(ConnectionID{1}), session, mentionEvent(), "Question")
			if executor.calls != test.wantExec || len(session.sent) != test.wantSends || len(service.appended) != 0 {
				t.Fatalf("executes=%d sent=%d appended=%d", executor.calls, len(session.sent), len(service.appended))
			}
		})
	}
}

func TestRestrictedInteractionIsDeferredDeliveredEphemerallyAndStripsLinks(t *testing.T) {
	invocation := answerInvocation(AccessRestricted, ReplySameChannel)
	service := &fakeInvocationService{invocation: invocation, allowed: true}
	executor := &fakeAgentExecutor{result: agents.ExecuteResult{
		RunID: agents.RunID{9}, Status: agents.CompletionAnswered,
		Markdown: "[private](https://private.invalid/page)",
	}}
	handler, _ := NewAnswerHandler(service, executor)
	session := &fakeGatewaySession{}
	handler.handleInteraction(context.Background(), testGatewayCapture(ConnectionID{1}), session, answerInteraction(), "Question")

	if len(session.responses) != 1 || session.responses[0].Type != discordgo.InteractionResponseDeferredChannelMessageWithSource ||
		session.responses[0].Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Fatalf("responses=%+v", session.responses)
	}
	if len(session.followups) != 1 || session.followups[0].Flags != discordgo.MessageFlagsEphemeral ||
		len(session.followups[0].Embeds) != 1 || strings.Contains(session.followups[0].Embeds[0].Description, "https://") ||
		session.followups[0].AllowedMentions == nil || session.followups[0].AllowedMentions.Parse == nil ||
		len(session.followups[0].AllowedMentions.Parse) != 0 || len(service.appended) != 1 {
		t.Fatalf("followups=%+v appended=%+v", session.followups, service.appended)
	}
}

func TestRefusedAndInsufficientEvidenceResultsStillDeliver(t *testing.T) {
	for _, test := range []struct {
		status   agents.CompletionStatus
		markdown string
	}{
		{agents.CompletionRefused, "I can’t help with that request."},
		{agents.CompletionInsufficientEvidence, "The configured knowledge bases do not contain enough verified evidence to answer that."},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			service := &fakeInvocationService{invocation: answerInvocation(AccessPublic, ReplySameChannel), allowed: true}
			executor := &fakeAgentExecutor{result: agents.ExecuteResult{RunID: agents.RunID{9}, Status: test.status, Markdown: test.markdown}}
			handler, _ := NewAnswerHandler(service, executor)
			session := &fakeGatewaySession{}
			handler.handleMention(context.Background(), testGatewayCapture(ConnectionID{1}), session, mentionEvent(), "Question")
			if len(session.sent) != 1 || len(session.sent[0].data.Embeds) != 1 ||
				!strings.Contains(session.sent[0].data.Embeds[0].Description, test.markdown) || len(service.appended) != 1 {
				t.Fatalf("sent=%+v appended=%+v", session.sent, service.appended)
			}
		})
	}
}

func TestEveryFallbackPathReauthorizesAndSuppressesMentions(t *testing.T) {
	result := agents.ExecuteResult{Status: agents.CompletionFailed}
	reference := mentionEvent().Message
	interaction := answerInteraction().Interaction
	tests := []struct {
		name    string
		deliver func(*fakeGatewaySession, func() error) error
		assert  func(*testing.T, *fakeGatewaySession)
	}{
		{
			name: "reply",
			deliver: func(session *fakeGatewaySession, check func() error) error {
				return deliverReply(session, reference, result, check)
			},
			assert: func(t *testing.T, session *fakeGatewaySession) {
				if len(session.sent) != 1 || session.sent[0].data.Content != FallbackMessage ||
					session.sent[0].data.AllowedMentions == nil || session.sent[0].data.AllowedMentions.Parse == nil {
					t.Fatalf("reply fallback=%+v", session.sent)
				}
			},
		},
		{
			name: "channel",
			deliver: func(session *fakeGatewaySession, check func() error) error {
				return deliverChannel(session, "200", result, check)
			},
			assert: func(t *testing.T, session *fakeGatewaySession) {
				if len(session.sent) != 1 || session.sent[0].data.Content != FallbackMessage ||
					session.sent[0].data.AllowedMentions == nil || session.sent[0].data.AllowedMentions.Parse == nil {
					t.Fatalf("channel fallback=%+v", session.sent)
				}
			},
		},
		{
			name: "interaction",
			deliver: func(session *fakeGatewaySession, check func() error) error {
				return deliverInteraction(session, interaction, result, true, check)
			},
			assert: func(t *testing.T, session *fakeGatewaySession) {
				if len(session.followups) != 1 || session.followups[0].Content != FallbackMessage ||
					session.followups[0].AllowedMentions == nil || session.followups[0].AllowedMentions.Parse == nil {
					t.Fatalf("interaction fallback=%+v", session.followups)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			checks := 0
			session := &fakeGatewaySession{}
			if err := test.deliver(session, func() error { checks++; return nil }); err != nil {
				t.Fatal(err)
			}
			if checks != 1 {
				t.Fatalf("reauthorizations=%d", checks)
			}
			test.assert(t, session)
		})
	}
}

func TestPublicThreadFollowupsAuthorizeExactThenParentAndReuseThreadContext(t *testing.T) {
	thread := &discordgo.Channel{
		ID: "201", GuildID: "300", ParentID: "200", Type: discordgo.ChannelTypeGuildPublicThread,
	}
	for _, test := range []struct {
		name  string
		slash bool
	}{
		{name: "mention"},
		{name: "slash", slash: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			invocation := answerInvocation(AccessPublic, ReplyThread)
			service := &fakeInvocationService{
				invocation: invocation, allowed: true,
				context: []agents.Message{
					{Role: agents.RoleUser, Content: "first question"},
					{Role: agents.RoleAssistant, Content: "first answer"},
				},
			}
			executor := &fakeAgentExecutor{result: agents.ExecuteResult{
				RunID: agents.RunID{9}, Status: agents.CompletionAnswered, Markdown: "follow-up answer",
			}}
			handler, _ := NewAnswerHandler(service, executor)
			session := &fakeGatewaySession{channels: map[string]*discordgo.Channel{"201": thread}}
			if test.slash {
				interaction := answerInteraction()
				interaction.ChannelID = "201"
				handler.handleInteraction(context.Background(), testGatewayCapture(ConnectionID{1}), session, interaction, "follow-up")
			} else {
				event := mentionEvent()
				event.ChannelID = "201"
				handler.handleMention(context.Background(), testGatewayCapture(ConnectionID{1}), session, event, "follow-up")
			}
			if service.authorizes != 1 || service.authorizedChannel != "201" || service.authorizedParent == nil ||
				*service.authorizedParent != "200" || service.authorizedSlash != test.slash {
				t.Fatalf("authorizes=%d channel=%s parent=%v slash=%v", service.authorizes, service.authorizedChannel, service.authorizedParent, service.authorizedSlash)
			}
			if len(executor.request.Messages) != 3 || executor.request.Messages[0].Content != "first question" ||
				executor.request.Messages[2].Content != "follow-up" || len(service.appended) != 1 ||
				service.appended[0].key.DestinationID != "201" ||
				service.appended[0].key.AgentVersionID != invocation.AgentVersionID {
				t.Fatalf("request=%+v appended=%+v", executor.request, service.appended)
			}
		})
	}
}

func TestUnauthorizedInteractionGetsOnlyGenericEphemeralResponse(t *testing.T) {
	service := &fakeInvocationService{authorizeErr: errors.New("database detail"), allowed: true}
	handler, _ := NewAnswerHandler(service, &fakeAgentExecutor{})
	session := &fakeGatewaySession{}
	handler.handleInteraction(context.Background(), testGatewayCapture(ConnectionID{1}), session, answerInteraction(), "Question")
	if len(session.responses) != 1 || session.responses[0].Data.Content != unconfiguredInteractionMessage ||
		session.responses[0].Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Fatalf("responses=%+v", session.responses)
	}
}

func TestCitationSafetyStripsLinksAndRendersRestrictedSourcesAsText(t *testing.T) {
	path := "private.md"
	result := citationSafe(agents.ExecuteResult{
		Markdown:  "Read [this](repo://source/private.md) and [that](https://private.invalid).",
		Citations: []agents.Citation{{Label: "Private", Resource: "https://private.invalid", Path: &path}},
	}, AccessRestricted)
	if strings.Contains(result.Markdown, "](repo://") || strings.Contains(result.Markdown, "](https://") ||
		!strings.Contains(result.Markdown, "- **Private** — private.md") {
		t.Fatalf("restricted markdown=%q", result.Markdown)
	}
}

func answerInvocation(access AccessPolicy, policy ReplyPolicy) Invocation {
	everyone := access == AccessPublic
	return Invocation{
		Binding: Binding{
			ID: BindingID{2}, ConnectionID: ConnectionID{1}, ServerID: "300", ListenChannelID: "200",
			AgentID: agents.AgentID{3}, Triggers: []TriggerType{TriggerMention}, ReplyPolicy: policy,
			AllowedRoleIDs: []Snowflake{"500"}, AllowedUserIDs: []Snowflake{},
			RatePolicy: RatePolicy{Requests: 5, WindowSeconds: 60}, Enabled: true, Health: BindingHealthy, Version: 1,
		},
		Trigger: TriggerMention, AgentKey: "helper", AgentVersionID: agents.VersionID{4},
		AgentResourceVersion: 2, EffectiveAccess: access,
		Corpus:  []InvocationCorpusMember{{Position: 0, KnowledgeBaseID: agents.KnowledgeBaseID{5}, KnowledgeBaseVersion: 3, AccessPolicy: access}},
		Subject: "400", ConnectionVersion: 7, CredentialID: credentials.ID{6}, CredentialVersion: 8,
		CapturedListen: ChannelCheck{
			ServerID: "300", ChannelID: "200", EffectiveBotPermissions: BasePermissions,
			EveryoneCanView: everyone, ViewerRoleIDs: []Snowflake{"300", "500"}, ViewerUserIDs: []Snowflake{},
		},
		CapturedReply: ChannelCheck{
			ServerID: "300", ChannelID: "200", EffectiveBotPermissions: BasePermissions,
			EveryoneCanView: everyone, ViewerRoleIDs: []Snowflake{"300", "500"}, ViewerUserIDs: []Snowflake{},
		},
	}
}

func mentionEvent() *discordgo.MessageCreate {
	return &discordgo.MessageCreate{Message: &discordgo.Message{
		ID: "100", ChannelID: "200", GuildID: "300",
		Author: &discordgo.User{ID: "400", Username: "reader"},
		Member: &discordgo.Member{Roles: []string{"500"}},
	}}
}

func answerInteraction() *discordgo.InteractionCreate {
	return &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID: "700", Token: "token", GuildID: "300", ChannelID: "200",
		Member: &discordgo.Member{User: &discordgo.User{ID: "400", Username: "reader"}, Roles: []string{"500"}},
	}}
}
