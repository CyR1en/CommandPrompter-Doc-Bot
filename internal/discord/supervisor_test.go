package discord

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/security"
)

type fakeGatewaySource struct {
	mu       sync.Mutex
	configs  []GatewayConfig
	owned    bool
	events   []string
	eventErr error
}

func (source *fakeGatewaySource) EnabledConnections(context.Context) ([]GatewayConfig, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return append([]GatewayConfig(nil), source.configs...), nil
}

func (source *fakeGatewaySource) AcquireOwnership(context.Context, ConnectionID) (OwnershipLease, error) {
	return &fakeOwnership{owned: source.owned}, nil
}

func (source *fakeGatewaySource) Connecting(context.Context, GatewayCapture) error {
	source.record("connecting")
	return nil
}

func (source *fakeGatewaySource) Ready(context.Context, GatewayCapture, time.Duration) error {
	source.record("ready")
	return nil
}

func (source *fakeGatewaySource) EventReceived(context.Context, GatewayCapture, time.Duration) error {
	source.record("event")
	return source.eventErr
}

func (source *fakeGatewaySource) Degraded(context.Context, GatewayCapture, string) error {
	source.record("degraded")
	return nil
}

func (source *fakeGatewaySource) record(event string) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.events = append(source.events, event)
}

type fakeOwnership struct{ owned bool }

func (ownership *fakeOwnership) Owned() bool                 { return ownership.owned }
func (ownership *fakeOwnership) Close(context.Context) error { return nil }

type fakeHandler struct {
	mentions, interactions int
}

func (handler *fakeHandler) HandleMention(context.Context, GatewayCapture, *discordgo.Session, *discordgo.MessageCreate, string) {
	handler.mentions++
}
func (handler *fakeHandler) HandleInteraction(context.Context, GatewayCapture, *discordgo.Session, *discordgo.InteractionCreate, string) {
	handler.interactions++
}

type fakeGateway struct {
	started chan string
	closed  chan struct{}
	once    sync.Once
}

func newFakeGateway() *fakeGateway {
	return &fakeGateway{started: make(chan string, 1), closed: make(chan struct{})}
}

func (gateway *fakeGateway) Run(ctx context.Context, config GatewayConfig, _ EventHandler, _ GatewaySource) error {
	gateway.started <- config.Token().Reveal()
	<-ctx.Done()
	return nil
}

func (gateway *fakeGateway) Close() error {
	gateway.once.Do(func() { close(gateway.closed) })
	return nil
}

func TestSupervisorRestartsOnlyConnectionWhoseCredentialIdentityChanged(t *testing.T) {
	firstID := ConnectionID{1}
	secondID := ConnectionID{2}
	source := &fakeGatewaySource{owned: true}
	created := map[ConnectionID][]*fakeGateway{}
	factory := func(id ConnectionID) Gateway {
		gateway := newFakeGateway()
		created[id] = append(created[id], gateway)
		return gateway
	}
	supervisor, err := NewSupervisor(source, &fakeHandler{}, time.Second, factory)
	if err != nil {
		t.Fatal(err)
	}
	source.configs = []GatewayConfig{
		gatewayConfig(t, firstID, "token-one", 1), gatewayConfig(t, secondID, "token-stable", 1),
	}
	if err := supervisor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if token := awaitString(t, created[firstID][0].started); token != "token-one" {
		t.Fatal(token)
	}
	if token := awaitString(t, created[secondID][0].started); token != "token-stable" {
		t.Fatal(token)
	}
	if err := supervisor.Reconcile(context.Background()); err != nil || len(created[firstID]) != 1 || len(created[secondID]) != 1 {
		t.Fatalf("stable reconcile: created=%v err=%v", created, err)
	}
	connectionGeneration := gatewayConfig(t, firstID, "token-one", 1)
	connectionGeneration.ConnectionVersion = 2
	source.configs = []GatewayConfig{connectionGeneration, gatewayConfig(t, secondID, "token-stable", 1)}
	if err := supervisor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if token := awaitString(t, created[firstID][1].started); token != "token-one" {
		t.Fatal(token)
	}
	awaitClosed(t, created[firstID][0].closed)
	source.configs = []GatewayConfig{
		gatewayConfigCredentialVersion(t, firstID, 2, credentials.ID{99}, "token-two", 1),
		gatewayConfig(t, secondID, "token-stable", 1),
	}
	if err := supervisor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if token := awaitString(t, created[firstID][2].started); token != "token-two" {
		t.Fatal(token)
	}
	awaitClosed(t, created[firstID][1].closed)
	select {
	case <-created[secondID][0].closed:
		t.Fatal("unchanged gateway was stopped")
	default:
	}
	source.configs = nil
	if err := supervisor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	awaitClosed(t, created[secondID][0].closed)
	awaitClosed(t, created[firstID][2].closed)
}

func TestGatewayEventFenceStopsStaleDispatch(t *testing.T) {
	source := &fakeGatewaySource{eventErr: ErrConflict}
	handler := &fakeHandler{}
	session, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	session.State.User = &discordgo.User{ID: "200"}
	dispatchGatewayMessage(context.Background(), testGatewayCapture(ConnectionID{1}), session,
		&discordgo.MessageCreate{Message: &discordgo.Message{
			GuildID: "100", ChannelID: "500", Content: "<@200> question", Author: &discordgo.User{ID: "300"},
		}}, handler, source)
	if handler.mentions != 0 || len(source.events) != 1 {
		t.Fatalf("stale event dispatched: mentions=%d events=%v", handler.mentions, source.events)
	}
}

func TestSupervisorRequiresDatabaseOwnershipAndBoundsConnections(t *testing.T) {
	source := &fakeGatewaySource{owned: false, configs: []GatewayConfig{gatewayConfig(t, ConnectionID{1}, "token", 1)}}
	gateway := newFakeGateway()
	supervisor, err := NewSupervisor(source, &fakeHandler{}, 100*time.Millisecond, func(ConnectionID) Gateway { return gateway })
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	select {
	case token := <-gateway.started:
		t.Fatalf("unowned gateway started with %q", token)
	default:
	}
	source.configs = make([]GatewayConfig, maximumGatewayConnections+1)
	for index := range source.configs {
		source.configs[index] = gatewayConfig(t, ConnectionID{byte(index + 1)}, "token", 1)
	}
	if err := supervisor.Reconcile(context.Background()); err == nil {
		t.Fatal("connection bound was not enforced")
	}
}

func TestGatewayConfigAndErrorsNeverRenderToken(t *testing.T) {
	sentinel := "discord-token-sentinel"
	config := gatewayConfig(t, ConnectionID{1}, sentinel, 1)
	for _, rendered := range []string{config.Token().String(), fmt.Sprintf("%v", config), fmt.Sprintf("%+v", config)} {
		if strings.Contains(rendered, sentinel) {
			t.Fatalf("token rendered in %q", rendered)
		}
	}
}

func TestMentionQuestion(t *testing.T) {
	for input, want := range map[string]string{
		"<@123> explain leases":  "explain leases",
		"hello <@!123> world":    "hello  world",
		"<@999> wrong recipient": "",
		"<@123>":                 "",
	} {
		if got := mentionQuestion(input, "123"); got != want {
			t.Errorf("mentionQuestion(%q)=%q want=%q", input, got, want)
		}
	}
}

func gatewayConfig(t *testing.T, id ConnectionID, token string, version int32) GatewayConfig {
	return gatewayConfigCredential(t, id, credentials.ID{id[0], 1}, token, version)
}

func gatewayConfigCredential(
	t *testing.T,
	id ConnectionID,
	credentialID credentials.ID,
	token string,
	version int32,
) GatewayConfig {
	return gatewayConfigCredentialVersion(t, id, 1, credentialID, token, version)
}

func gatewayConfigCredentialVersion(
	t *testing.T,
	id ConnectionID,
	connectionVersion int32,
	credentialID credentials.ID,
	token string,
	version int32,
) GatewayConfig {
	t.Helper()
	secret, err := security.NewSecretValue(token)
	if err != nil {
		t.Fatal(err)
	}
	return GatewayConfig{
		ID: id, ConnectionVersion: connectionVersion, CredentialID: credentialID,
		CredentialVersion: version, token: secret,
	}
}

func awaitString(t *testing.T, values <-chan string) string {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gateway start")
		return ""
	}
}

func awaitClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for gateway stop")
	}
}
