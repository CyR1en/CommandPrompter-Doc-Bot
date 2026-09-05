package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

type discordRoundTripFunc func(*http.Request) (*http.Response, error)

func (function discordRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func discordJSONResponse(status int, value any) *http.Response {
	body, _ := json.Marshal(value)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

func newDiscordRESTTestClient(t *testing.T, transport http.RoundTripper, maxBytes int64) *RESTClient {
	t.Helper()
	client, err := NewRESTClient(RESTOptions{
		BaseURL: "https://discord.test/api/v10", Transport: transport,
		MaxResponseBytes: maxBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}

func TestRESTValidatesBotIdentityWithBoundedSecretHeaders(t *testing.T) {
	var mu sync.Mutex
	paths := make(map[string]int)
	client := newDiscordRESTTestClient(t, discordRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bot write-only-token" ||
			request.Header.Get("User-Agent") != discordUserAgent ||
			request.Header.Get("Accept-Encoding") != "identity" {
			t.Fatalf("headers = %v", request.Header)
		}
		mu.Lock()
		paths[request.URL.Path]++
		mu.Unlock()
		switch request.URL.Path {
		case "/api/v10/users/@me":
			return discordJSONResponse(200, map[string]any{
				"id": "1844674407370955161", "username": "ref0", "bot": true, "avatar": "hash",
			}), nil
		case "/api/v10/oauth2/applications/@me":
			return discordJSONResponse(200, map[string]any{"id": "1844674407370955162"}), nil
		default:
			return nil, errors.New("unexpected Discord path")
		}
	}), 1024)
	identity, err := client.ValidateToken(context.Background(), "write-only-token")
	if err != nil {
		t.Fatal(err)
	}
	if identity.ApplicationID != "1844674407370955162" || identity.BotUserID != "1844674407370955161" ||
		identity.Username != "ref0" || identity.AvatarHash == nil || *identity.AvatarHash != "hash" {
		t.Fatalf("identity = %+v", identity)
	}
	if paths["/api/v10/users/@me"] != 1 || paths["/api/v10/oauth2/applications/@me"] != 1 {
		t.Fatalf("paths = %v", paths)
	}
}

func TestRESTRefreshProjectsDeterministicAudienceAndThreadPermissions(t *testing.T) {
	const (
		serverID = "100"
		botID    = "200"
	)
	everyonePermissions := int64(BasePermissions)
	responses := map[string]any{
		"/api/v10/users/@me/guilds": []any{
			map[string]any{"id": serverID, "name": "Docs", "icon": nil, "owner": true},
		},
		"/api/v10/guilds/100/channels": []any{
			map[string]any{
				"id": "500", "name": "private-docs", "type": 0, "position": 2,
				"permission_overwrites": []any{
					map[string]any{"id": serverID, "type": 0, "allow": "0", "deny": "1024"},
					map[string]any{"id": "400", "type": 0, "allow": "1024", "deny": "0"},
					map[string]any{"id": "300", "type": 0, "allow": "1024", "deny": "0"},
					map[string]any{"id": "600", "type": 1, "allow": "1024", "deny": "0"},
				},
			},
			map[string]any{"id": "999", "name": "ignored", "type": 2},
		},
		"/api/v10/guilds/100/threads/active": map[string]any{"threads": []any{
			map[string]any{"id": "501", "parent_id": "500", "name": "question", "type": 11, "position": 1},
		}},
		"/api/v10/guilds/100/roles": []any{
			map[string]any{"id": serverID, "name": "@everyone", "permissions": json.Number("84992"), "position": 0},
			map[string]any{"id": "400", "name": "bot", "permissions": "0", "position": 2},
			map[string]any{"id": "300", "name": "reader", "permissions": "0", "position": 1},
		},
		"/api/v10/guilds/100/members/200": map[string]any{"roles": []any{"400"}},
	}
	if everyonePermissions != 84_992 {
		t.Fatalf("test permission fixture drifted: %d", everyonePermissions)
	}
	client := newDiscordRESTTestClient(t, discordRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		value, ok := responses[request.URL.Path]
		if !ok {
			return nil, errors.New("unexpected Discord path: " + request.URL.Path)
		}
		return discordJSONResponse(200, value), nil
	}), 64*1024)
	snapshots, err := client.RefreshServers(context.Background(), "token", botID)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].Server.ID != serverID || !snapshots[0].Server.Owner {
		t.Fatalf("snapshots = %+v", snapshots)
	}
	channels := snapshots[0].Channels
	if len(channels) != 2 || channels[0].ID != "501" || channels[1].ID != "500" {
		t.Fatalf("channels = %+v", channels)
	}
	for _, channel := range channels {
		if channel.EveryoneCanView || channel.EffectiveBotPermissions&BasePermissions != BasePermissions {
			t.Fatalf("projected channel = %+v", channel)
		}
		if strings.Join(testSnowflakeStrings(channel.ViewerRoleIDs), ",") != "300,400" ||
			strings.Join(testSnowflakeStrings(channel.ViewerUserIDs), ",") != "600" {
			t.Fatalf("audience projection = %+v", channel)
		}
	}
	if channels[0].ParentID == nil || *channels[0].ParentID != "500" {
		t.Fatalf("thread parent = %+v", channels[0].ParentID)
	}
	roles := snapshots[0].Roles
	if len(roles) != 3 || roles[0].ID != "100" || roles[1].ID != "300" || roles[2].ID != "400" {
		t.Fatalf("roles = %+v", roles)
	}
}

func TestRESTRefreshDeliveryTargetsOnlySelectedGuildAndSortsSnowflakesNumerically(t *testing.T) {
	overwrites := []any{
		map[string]any{"id": "100", "type": 0, "allow": "0", "deny": "1024"},
		map[string]any{"id": "2", "type": 0, "allow": "1024", "deny": "0"},
		map[string]any{"id": "10", "type": 0, "allow": "1024", "deny": "0"},
		map[string]any{"id": "20", "type": 0, "allow": "1024", "deny": "0"},
		map[string]any{"id": "600", "type": 1, "allow": "1024", "deny": "0"},
		map[string]any{"id": "601", "type": 1, "allow": "0", "deny": "1024"},
	}
	paths := map[string]any{
		"/api/v10/guilds/100":             map[string]any{"id": "100", "name": "Target", "icon": nil, "owner": false},
		"/api/v10/guilds/100/members/600": map[string]any{"roles": []any{"10", "2"}},
		"/api/v10/guilds/100/channels": []any{map[string]any{
			"id": "500", "name": "private-docs", "type": 0, "position": 0,
			"permission_overwrites": overwrites,
		}},
		"/api/v10/guilds/100/threads/active": map[string]any{"threads": []any{}},
		"/api/v10/guilds/100/roles": []any{
			map[string]any{"id": "100", "name": "@everyone", "permissions": json.Number("84992"), "position": 0},
			map[string]any{"id": "2", "name": "small", "permissions": "0", "position": 1},
			map[string]any{"id": "10", "name": "large", "permissions": "0", "position": 2},
			map[string]any{"id": "20", "name": "bot", "permissions": "0", "position": 3},
		},
		"/api/v10/guilds/100/members/200": map[string]any{"roles": []any{"20"}},
	}
	client := newDiscordRESTTestClient(t, discordRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		value, exists := paths[request.URL.Path]
		if !exists {
			return nil, errors.New("unrelated guild or unbounded endpoint requested: " + request.URL.Path)
		}
		return discordJSONResponse(200, value), nil
	}), 64*1024)
	state, err := client.RefreshDelivery(context.Background(), "token", "200", "100", "600", "500", "500")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := state.CallerRoleIDs["2"]; !ok || len(state.CallerRoleIDs) != 2 ||
		strings.Join(testSnowflakeStrings(state.Destination.ViewerRoleIDs), ",") != "2,10,20" ||
		strings.Join(testSnowflakeStrings(state.Destination.ViewerUserIDs), ",") != "600" {
		t.Fatalf("delivery state=%+v", state)
	}
	channels := paths["/api/v10/guilds/100/channels"].([]any)
	channels[0].(map[string]any)["permission_overwrites"] = overwrites[:len(overwrites)-1]
	expanded, err := client.RefreshDelivery(context.Background(), "token", "200", "100", "600", "500", "500")
	if err != nil {
		t.Fatal(err)
	}
	if expanded.Destination.EveryoneCanView != state.Destination.EveryoneCanView ||
		!slices.Equal(expanded.Destination.ViewerRoleIDs, state.Destination.ViewerRoleIDs) ||
		!slices.Equal(expanded.Destination.ViewerUserIDs, state.Destination.ViewerUserIDs) ||
		expanded.Destination.AudienceOverwriteSHA256 == state.Destination.AudienceOverwriteSHA256 {
		t.Fatalf("member-deny removal was not isolated to overwrite digest: before=%+v after=%+v", state.Destination, expanded.Destination)
	}
}

func TestRESTWritesSuppressMentionsAndUseExactCommandShape(t *testing.T) {
	seen := make(map[string]map[string]any)
	client := newDiscordRESTTestClient(t, discordRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			t.Fatal(err)
		}
		seen[request.URL.Path] = payload
		return discordJSONResponse(200, map[string]any{"id": "700"}), nil
	}), 64*1024)
	messageID, err := client.SendTestMessage(context.Background(), "token", "500", "  connected  ")
	if err != nil || messageID != "700" {
		t.Fatalf("message id=%q err=%v", messageID, err)
	}
	commandID, err := client.RegisterAskCommand(context.Background(), "token", "800", "100")
	if err != nil || commandID != "700" {
		t.Fatalf("command id=%q err=%v", commandID, err)
	}
	message := seen["/api/v10/channels/500/messages"]
	mentions, _ := message["allowed_mentions"].(map[string]any)
	parse, _ := mentions["parse"].([]any)
	if message["content"] != "connected" || len(parse) != 0 {
		t.Fatalf("message payload = %#v", message)
	}
	command := seen["/api/v10/applications/800/guilds/100/commands"]
	options, _ := command["options"].([]any)
	option, _ := options[0].(map[string]any)
	if command["name"] != "ask" || command["description"] != "Ask the configured Agent" ||
		option["name"] != "question" || option["max_length"] != json.Number("2000") {
		t.Fatalf("command payload = %#v", command)
	}
}

func TestRESTMapsFailuresWithoutRedirectsOrResponseLeakage(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		maximum int64
		want    string
	}{
		{name: "unauthorized", status: 401, body: `{"token":"secret"}`, maximum: 1024, want: "Discord bot token was rejected."},
		{name: "forbidden", status: 403, body: `{}`, maximum: 1024, want: "Discord bot lacks permission for this operation."},
		{name: "rate", status: 429, body: `{}`, maximum: 1024, want: "Discord API rate limit was reached."},
		{name: "redirect", status: 302, body: `{}`, maximum: 1024, want: "Discord API request failed."},
		{name: "overbound", status: 200, body: strings.Repeat("x", 20), maximum: 8, want: "Discord API response exceeds its bound."},
		{name: "invalid JSON", status: 200, body: `not-json`, maximum: 1024, want: "Discord API response is invalid."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			client := newDiscordRESTTestClient(t, discordRoundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				return &http.Response{StatusCode: test.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(test.body))}, nil
			}), test.maximum)
			_, err := client.SendTestMessage(context.Background(), "token", "500", "connected")
			if err == nil || err.Error() != test.want || strings.Contains(err.Error(), "secret") {
				t.Fatalf("err = %v", err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d", requests)
			}
		})
	}
	client := newDiscordRESTTestClient(t, discordRoundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("invalid token must not reach transport")
		return nil, nil
	}), 1024)
	_, err := client.SendTestMessage(context.Background(), "token with spaces", "500", "connected")
	if err == nil || err.Error() != "Discord bot token is invalid." {
		t.Fatalf("invalid token err = %v", err)
	}
}

func TestRESTRejectsUntrustedPlainHTTPBase(t *testing.T) {
	_, err := NewRESTClient(RESTOptions{BaseURL: "http://127.0.0.1/api/v10"})
	if err == nil || err.Error() != "Discord API must use HTTPS" {
		t.Fatalf("err = %v", err)
	}
}

func testSnowflakeStrings(values []Snowflake) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}
