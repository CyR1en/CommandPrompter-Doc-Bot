package discord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/cyr1en/ref0/internal/credentials"
)

const maximumGatewayConnections = 20

type GatewaySource interface {
	EnabledConnections(context.Context) ([]GatewayConfig, error)
	AcquireOwnership(context.Context, ConnectionID) (OwnershipLease, error)
	Connecting(context.Context, GatewayCapture) error
	Ready(context.Context, GatewayCapture, time.Duration) error
	EventReceived(context.Context, GatewayCapture, time.Duration) error
	Degraded(context.Context, GatewayCapture, string) error
}

var _ GatewaySource = (*Store)(nil)

type EventHandler interface {
	HandleMention(context.Context, GatewayCapture, *discordgo.Session, *discordgo.MessageCreate, string)
	HandleInteraction(context.Context, GatewayCapture, *discordgo.Session, *discordgo.InteractionCreate, string)
}

type Gateway interface {
	Run(context.Context, GatewayConfig, EventHandler, GatewaySource) error
	Close() error
}

type GatewayFactory func(ConnectionID) Gateway

type Supervisor struct {
	source       GatewaySource
	handler      EventHandler
	refreshEvery time.Duration
	factory      GatewayFactory
	running      map[ConnectionID]*gatewayRun
}

type gatewayRun struct {
	connectionVersion int32
	credentialID      credentials.ID
	credentialVersion int32
	cancel            context.CancelFunc
	gateway           Gateway
	done              chan struct{}
}

func NewSupervisor(
	source GatewaySource,
	handler EventHandler,
	refreshEvery time.Duration,
	factory GatewayFactory,
) (*Supervisor, error) {
	if source == nil || handler == nil {
		return nil, errors.New("Discord supervisor dependencies are incomplete")
	}
	if refreshEvery < 100*time.Millisecond || refreshEvery > time.Minute {
		return nil, errors.New("Discord supervisor refresh interval is invalid")
	}
	if factory == nil {
		factory = func(ConnectionID) Gateway { return &discordGateway{} }
	}
	return &Supervisor{
		source: source, handler: handler, refreshEvery: refreshEvery,
		factory: factory, running: map[ConnectionID]*gatewayRun{},
	}, nil
}

func (supervisor *Supervisor) Run(ctx context.Context) error {
	ticker := time.NewTicker(supervisor.refreshEvery)
	defer ticker.Stop()
	defer supervisor.stopAll()
	for {
		if err := supervisor.Reconcile(ctx); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (supervisor *Supervisor) Reconcile(ctx context.Context) error {
	configs, err := supervisor.source.EnabledConnections(ctx)
	if err != nil {
		return err
	}
	if len(configs) > maximumGatewayConnections {
		return errors.New("Discord connection count exceeds its configured bound")
	}
	desired := make(map[ConnectionID]GatewayConfig, len(configs))
	for _, config := range configs {
		if config.token == nil || config.ConnectionVersion <= 0 ||
			config.CredentialID == (credentials.ID{}) || config.CredentialVersion <= 0 {
			return errors.New("Discord gateway configuration is invalid")
		}
		desired[config.ID] = config
	}
	for id := range supervisor.running {
		if _, exists := desired[id]; !exists {
			supervisor.stop(id)
		}
	}
	for id, config := range desired {
		current := supervisor.running[id]
		if current != nil && current.connectionVersion == config.ConnectionVersion && current.credentialID == config.CredentialID &&
			current.credentialVersion == config.CredentialVersion && !closed(current.done) {
			continue
		}
		if current != nil {
			supervisor.stop(id)
		}
		runCtx, cancel := context.WithCancel(ctx)
		gateway := supervisor.factory(id)
		run := &gatewayRun{
			connectionVersion: config.ConnectionVersion,
			credentialID:      config.CredentialID, credentialVersion: config.CredentialVersion,
			cancel: cancel, gateway: gateway, done: make(chan struct{}),
		}
		supervisor.running[id] = run
		go supervisor.runGateway(runCtx, config, run)
	}
	return nil
}

func (supervisor *Supervisor) runGateway(ctx context.Context, config GatewayConfig, run *gatewayRun) {
	defer close(run.done)
	ownership, err := supervisor.source.AcquireOwnership(ctx, config.ID)
	if err != nil {
		_ = supervisor.source.Degraded(context.WithoutCancel(ctx), config.Capture(), "Discord gateway ownership failed.")
		return
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ownership.Close(closeCtx)
	}()
	if !ownership.Owned() {
		return
	}
	if err := supervisor.source.Connecting(ctx, config.Capture()); err != nil {
		return
	}
	if err := run.gateway.Run(ctx, config, supervisor.handler, supervisor.source); err != nil && ctx.Err() == nil {
		_ = supervisor.source.Degraded(context.WithoutCancel(ctx), config.Capture(), "Discord gateway connection failed.")
	}
}

func (supervisor *Supervisor) stop(id ConnectionID) {
	run := supervisor.running[id]
	delete(supervisor.running, id)
	if run == nil {
		return
	}
	run.cancel()
	_ = run.gateway.Close()
	select {
	case <-run.done:
	case <-time.After(5 * time.Second):
	}
}

func (supervisor *Supervisor) stopAll() {
	for id := range supervisor.running {
		supervisor.stop(id)
	}
}

type discordGateway struct {
	mu      sync.Mutex
	session *discordgo.Session
}

func (gateway *discordGateway) Run(
	ctx context.Context,
	config GatewayConfig,
	handler EventHandler,
	reporter GatewaySource,
) error {
	session, err := discordgo.New("Bot " + config.token.Reveal())
	if err != nil {
		return errors.New("initialize Discord gateway")
	}
	session.Identify.Intents = discordgo.IntentsGuilds | discordgo.IntentsGuildMessages
	session.AddHandler(func(session *discordgo.Session, _ *discordgo.Ready) {
		_ = reporter.Ready(context.WithoutCancel(ctx), config.Capture(), session.HeartbeatLatency())
	})
	session.AddHandler(func(session *discordgo.Session, event *discordgo.MessageCreate) {
		dispatchGatewayMessage(ctx, config.Capture(), session, event, handler, reporter)
	})
	session.AddHandler(func(session *discordgo.Session, event *discordgo.InteractionCreate) {
		dispatchGatewayInteraction(ctx, config.Capture(), session, event, handler, reporter)
	})
	gateway.mu.Lock()
	gateway.session = session
	gateway.mu.Unlock()
	if err := session.Open(); err != nil {
		gateway.mu.Lock()
		gateway.session = nil
		gateway.mu.Unlock()
		return errors.New("open Discord gateway")
	}
	<-ctx.Done()
	return gateway.Close()
}

func dispatchGatewayMessage(
	ctx context.Context,
	capture GatewayCapture,
	session *discordgo.Session,
	event *discordgo.MessageCreate,
	handler EventHandler,
	reporter GatewaySource,
) {
	if session == nil || reporter.EventReceived(context.WithoutCancel(ctx), capture, session.HeartbeatLatency()) != nil {
		return
	}
	if event == nil || event.Author == nil || event.Author.Bot || event.GuildID == "" || session.State.User == nil {
		return
	}
	question := mentionQuestion(event.Content, session.State.User.ID)
	if question != "" {
		handler.HandleMention(ctx, capture, session, event, question)
	}
}

func dispatchGatewayInteraction(
	ctx context.Context,
	capture GatewayCapture,
	session *discordgo.Session,
	event *discordgo.InteractionCreate,
	handler EventHandler,
	reporter GatewaySource,
) {
	if session == nil || reporter.EventReceived(context.WithoutCancel(ctx), capture, session.HeartbeatLatency()) != nil {
		return
	}
	if event == nil || event.Type != discordgo.InteractionApplicationCommand {
		return
	}
	data := event.ApplicationCommandData()
	if data.Name != "ask" {
		return
	}
	for _, option := range data.Options {
		if option.Name == "question" {
			if question := strings.TrimSpace(option.StringValue()); question != "" {
				handler.HandleInteraction(ctx, capture, session, event, question)
			}
			return
		}
	}
}

func (gateway *discordGateway) Close() error {
	gateway.mu.Lock()
	session := gateway.session
	gateway.session = nil
	gateway.mu.Unlock()
	if session == nil {
		return nil
	}
	return session.Close()
}

func mentionQuestion(content, userID string) string {
	plain := "<@" + userID + ">"
	nickname := "<@!" + userID + ">"
	if !strings.Contains(content, plain) && !strings.Contains(content, nickname) {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(content, nickname, ""), plain, ""))
}

func closed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}
