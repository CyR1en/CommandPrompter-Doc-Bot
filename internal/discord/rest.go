package discord

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/errgroup"
)

const (
	discordAPIBaseURL       = "https://discord.com/api/v10/"
	discordUserAgent        = "ref0/1.0"
	defaultRESTTimeout      = 10 * time.Second
	defaultRESTMaxServers   = 100
	defaultRESTMaxBodyBytes = int64(2_000_000)
	maximumRESTTokenBytes   = 16_384
	maximumRESTChannels     = 1_000
	maximumRESTRoles        = 500

	permissionAdministrator Permission = 1 << 3
	allPermissions          Permission = math.MaxInt64
)

type APIError struct{ message string }

func (err *APIError) Error() string { return err.message }

func apiError(message string) error { return &APIError{message: message} }

type Identity struct {
	ApplicationID Snowflake
	BotUserID     Snowflake
	Username      string
	AvatarHash    *string
}

type ServerMetadata struct {
	ID       Snowflake
	Name     string
	IconHash *string
	Owner    bool
}

type RoleMetadata struct {
	ID       Snowflake
	Name     string
	Position int32
}

type ChannelMetadata struct {
	ID                      Snowflake
	ServerID                Snowflake
	ParentID                *Snowflake
	Name                    string
	ChannelType             int32
	Position                int32
	EffectiveBotPermissions Permission
	EveryoneCanView         bool
	ViewerRoleIDs           []Snowflake
	ViewerUserIDs           []Snowflake
	AudienceOverwriteSHA256 [32]byte
}

type ServerSnapshot struct {
	Server   ServerMetadata
	Channels []ChannelMetadata
	Roles    []RoleMetadata
}

type LiveDeliveryState struct {
	CallerRoleIDs map[Snowflake]struct{}
	Listen        ChannelCheck
	Destination   ChannelCheck
}

type RESTOptions struct {
	BaseURL          string
	Transport        http.RoundTripper
	Timeout          time.Duration
	MaxServers       int
	MaxResponseBytes int64
}

type RESTClient struct {
	baseURL          *url.URL
	client           *http.Client
	maxServers       int
	maxResponseBytes int64
}

func NewRESTClient(options RESTOptions) (*RESTClient, error) {
	rawBase := options.BaseURL
	if rawBase == "" {
		rawBase = discordAPIBaseURL
	}
	base, err := url.Parse(strings.TrimRight(rawBase, "/") + "/")
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, errors.New("Discord API base URL is invalid")
	}
	if base.Scheme != "https" && options.Transport == nil {
		return nil, errors.New("Discord API must use HTTPS")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultRESTTimeout
	}
	maxServers := options.MaxServers
	if maxServers == 0 {
		maxServers = defaultRESTMaxServers
	}
	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = defaultRESTMaxBodyBytes
	}
	if timeout < 0 || maxServers < 0 || maxResponseBytes < 0 {
		return nil, errors.New("Discord REST limits must be positive")
	}
	transport := options.Transport
	if transport == nil {
		dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
		transport = &http.Transport{
			Proxy:                  nil,
			DialContext:            dialer.DialContext,
			ForceAttemptHTTP2:      true,
			TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12},
			TLSHandshakeTimeout:    timeout,
			ResponseHeaderTimeout:  timeout,
			ExpectContinueTimeout:  time.Second,
			MaxResponseHeaderBytes: 64 * 1024,
		}
	}
	return &RESTClient{
		baseURL: base,
		client: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxServers:       maxServers,
		maxResponseBytes: maxResponseBytes,
	}, nil
}

func (client *RESTClient) Close() {
	if client == nil || client.client == nil {
		return
	}
	if closer, ok := client.client.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (client *RESTClient) ValidateToken(ctx context.Context, token string) (Identity, error) {
	group, groupCtx := errgroup.WithContext(ctx)
	var user, application any
	group.Go(func() error {
		value, err := client.request(groupCtx, http.MethodGet, "users/@me", token, nil)
		user = value
		return err
	})
	group.Go(func() error {
		value, err := client.request(groupCtx, http.MethodGet, "oauth2/applications/@me", token, nil)
		application = value
		return err
	})
	if err := group.Wait(); err != nil {
		return Identity{}, err
	}
	userMap, ok := user.(map[string]any)
	if !ok {
		return Identity{}, apiError("Discord token does not identify a bot user.")
	}
	bot, ok := userMap["bot"].(bool)
	if !ok || !bot {
		return Identity{}, apiError("Discord token does not identify a bot user.")
	}
	applicationMap, ok := application.(map[string]any)
	if !ok {
		return Identity{}, apiError("Discord identity response is invalid.")
	}
	applicationID, err := jsonSnowflake(applicationMap["id"])
	if err != nil {
		return Identity{}, apiError("Discord identity response is invalid.")
	}
	botUserID, err := jsonSnowflake(userMap["id"])
	if err != nil {
		return Identity{}, apiError("Discord identity response is invalid.")
	}
	username, ok := userMap["username"].(string)
	if !ok || !utf8.ValidString(username) || username == "" || utf8.RuneCountInString(username) > 255 {
		return Identity{}, apiError("Discord identity response is invalid.")
	}
	avatar, err := optionalJSONString(userMap["avatar"])
	if err != nil {
		return Identity{}, apiError("Discord identity response is invalid.")
	}
	return Identity{ApplicationID: applicationID, BotUserID: botUserID, Username: username, AvatarHash: avatar}, nil
}

func (client *RESTClient) RefreshServers(ctx context.Context, token string, botUserID Snowflake) ([]ServerSnapshot, error) {
	if _, err := ParseSnowflake(string(botUserID)); err != nil {
		return nil, err
	}
	value, err := client.request(ctx, http.MethodGet, "users/@me/guilds", token, nil)
	if err != nil {
		return nil, err
	}
	servers, ok := value.([]any)
	if !ok || len(servers) > client.maxServers {
		return nil, apiError("Discord server list exceeds its bound.")
	}
	result := make([]ServerSnapshot, 0, len(servers))
	for _, server := range servers {
		snapshot, snapshotErr := client.serverSnapshot(ctx, token, botUserID, server)
		if snapshotErr != nil {
			return nil, snapshotErr
		}
		result = append(result, snapshot)
	}
	return result, nil
}

func (client *RESTClient) RefreshDelivery(
	ctx context.Context,
	token string,
	botUserID Snowflake,
	serverID Snowflake,
	userID Snowflake,
	listenChannelID Snowflake,
	destinationID Snowflake,
) (LiveDeliveryState, error) {
	for _, value := range []Snowflake{botUserID, serverID, userID, listenChannelID, destinationID} {
		if _, err := ParseSnowflake(string(value)); err != nil {
			return LiveDeliveryState{}, err
		}
	}
	group, groupCtx := errgroup.WithContext(ctx)
	var serverValue any
	var memberValue any
	group.Go(func() error {
		var requestErr error
		serverValue, requestErr = client.request(groupCtx, http.MethodGet,
			"guilds/"+string(serverID), token, nil)
		return requestErr
	})
	group.Go(func() error {
		var err error
		memberValue, err = client.request(groupCtx, http.MethodGet,
			"guilds/"+string(serverID)+"/members/"+string(userID), token, nil)
		return err
	})
	if err := group.Wait(); err != nil {
		return LiveDeliveryState{}, err
	}
	snapshot, err := client.serverSnapshot(ctx, token, botUserID, serverValue)
	if err != nil {
		return LiveDeliveryState{}, err
	}
	member, ok := memberValue.(map[string]any)
	if !ok {
		return LiveDeliveryState{}, apiError("Discord member metadata is invalid.")
	}
	roles, err := jsonSnowflakeSet(member["roles"], true)
	if err != nil {
		return LiveDeliveryState{}, apiError("Discord member metadata is invalid.")
	}
	result := LiveDeliveryState{CallerRoleIDs: roles}
	foundListen, foundDestination := false, false
	if snapshot.Server.ID != serverID {
		return LiveDeliveryState{}, apiError("Discord delivery destination is unavailable.")
	}
	for _, channel := range snapshot.Channels {
		check := ChannelCheck{
			ServerID: serverID, ChannelID: channel.ID, ParentID: channel.ParentID, ChannelType: channel.ChannelType,
			EffectiveBotPermissions: channel.EffectiveBotPermissions,
			EveryoneCanView:         channel.EveryoneCanView,
			ViewerRoleIDs:           append([]Snowflake(nil), channel.ViewerRoleIDs...),
			ViewerUserIDs:           append([]Snowflake(nil), channel.ViewerUserIDs...),
			AudienceOverwriteSHA256: channel.AudienceOverwriteSHA256,
		}
		if channel.ID == listenChannelID {
			result.Listen, foundListen = check, true
		}
		if channel.ID == destinationID {
			result.Destination, foundDestination = check, true
		}
	}
	if !foundListen || !foundDestination {
		return LiveDeliveryState{}, apiError("Discord delivery destination is unavailable.")
	}
	return result, nil
}

func (client *RESTClient) SendTestMessage(ctx context.Context, token string, channelID Snowflake, content string) (Snowflake, error) {
	if _, err := ParseSnowflake(string(channelID)); err != nil {
		return "", err
	}
	normalized := strings.TrimFunc(content, pythonWhitespace)
	if !utf8.ValidString(normalized) || normalized == "" || utf8.RuneCountInString(normalized) > 2_000 {
		return "", errors.New("Discord test message is invalid")
	}
	value, err := client.request(ctx, http.MethodPost, "channels/"+string(channelID)+"/messages", token,
		map[string]any{"content": normalized, "allowed_mentions": map[string]any{"parse": []string{}}})
	if err != nil {
		return "", err
	}
	response, ok := value.(map[string]any)
	if !ok {
		return "", apiError("Discord message response is invalid.")
	}
	id, err := jsonSnowflake(response["id"])
	if err != nil {
		return "", apiError("Discord message response is invalid.")
	}
	return id, nil
}

func (client *RESTClient) RegisterAskCommand(ctx context.Context, token string, applicationID, serverID Snowflake) (Snowflake, error) {
	if _, err := ParseSnowflake(string(applicationID)); err != nil {
		return "", err
	}
	if _, err := ParseSnowflake(string(serverID)); err != nil {
		return "", err
	}
	payload := map[string]any{
		"name": "ask", "description": "Ask the configured Agent", "type": 1,
		"contexts": []int{0}, "integration_types": []int{0},
		"options": []map[string]any{{
			"type": 3, "name": "question", "description": "Question to answer",
			"required": true, "max_length": 2_000,
		}},
	}
	value, err := client.request(ctx, http.MethodPost,
		"applications/"+string(applicationID)+"/guilds/"+string(serverID)+"/commands", token, payload)
	if err != nil {
		return "", err
	}
	response, ok := value.(map[string]any)
	if !ok {
		return "", apiError("Discord command response is invalid.")
	}
	id, err := jsonSnowflake(response["id"])
	if err != nil {
		return "", apiError("Discord command response is invalid.")
	}
	return id, nil
}

func (client *RESTClient) serverSnapshot(ctx context.Context, token string, botUserID Snowflake, raw any) (ServerSnapshot, error) {
	serverMap, ok := raw.(map[string]any)
	if !ok {
		return ServerSnapshot{}, apiError("Discord server response is invalid.")
	}
	serverID, idErr := jsonSnowflake(serverMap["id"])
	name, nameOK := serverMap["name"].(string)
	icon, iconErr := optionalJSONString(serverMap["icon"])
	owner, ownerOK := serverMap["owner"].(bool)
	if !ownerOK {
		owner = false
	}
	if idErr != nil || !nameOK || iconErr != nil {
		return ServerSnapshot{}, apiError("Discord server response is invalid.")
	}
	server := ServerMetadata{ID: serverID, Name: name, IconHash: icon, Owner: owner}

	paths := []string{
		"guilds/" + string(serverID) + "/channels",
		"guilds/" + string(serverID) + "/threads/active",
		"guilds/" + string(serverID) + "/roles",
		"guilds/" + string(serverID) + "/members/" + string(botUserID),
	}
	values := make([]any, len(paths))
	group, groupCtx := errgroup.WithContext(ctx)
	for index := range paths {
		index := index
		group.Go(func() error {
			value, requestErr := client.request(groupCtx, http.MethodGet, paths[index], token, nil)
			values[index] = value
			return requestErr
		})
	}
	if err := group.Wait(); err != nil {
		return ServerSnapshot{}, err
	}
	rawChannels, channelsOK := values[0].([]any)
	activeThreads, threadsOK := values[1].(map[string]any)
	rawRoles, rolesOK := values[2].([]any)
	member, memberOK := values[3].(map[string]any)
	var threads []any
	threadListOK := threadsOK
	if threadListOK {
		if rawThreads, exists := activeThreads["threads"]; exists {
			threads, threadListOK = rawThreads.([]any)
		} else {
			threads = []any{}
		}
	}
	if !channelsOK || !threadsOK || !rolesOK || !memberOK || !threadListOK {
		return ServerSnapshot{}, apiError("Discord server metadata is invalid.")
	}
	if len(rawChannels)+len(threads) > maximumRESTChannels || len(rawRoles) > maximumRESTRoles {
		return ServerSnapshot{}, apiError("Discord server metadata exceeds its bound.")
	}
	rolePermissions, err := discordRolePermissions(rawRoles)
	if err != nil {
		return ServerSnapshot{}, err
	}
	memberRoles, err := jsonSnowflakeSet(member["roles"], true)
	if err != nil {
		return ServerSnapshot{}, apiError("Discord server metadata is invalid.")
	}
	basePermissions := rolePermissions[serverID]
	for roleID := range memberRoles {
		basePermissions |= rolePermissions[roleID]
	}
	channelByID := make(map[Snowflake]map[string]any, len(rawChannels))
	for _, value := range rawChannels {
		channel, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, idErr := jsonSnowflake(channel["id"])
		if idErr == nil {
			channelByID[id] = channel
		}
	}
	allChannels := append(slices.Clone(rawChannels), threads...)
	channels := make([]ChannelMetadata, 0, len(allChannels))
	for _, value := range allChannels {
		channel, ok := value.(map[string]any)
		if !ok {
			continue
		}
		channelType, typeErr := jsonInt32(channel["type"])
		if typeErr != nil || !supportedChannelType(channelType) {
			continue
		}
		effective := channel
		if channelType == 11 {
			if parent, parentErr := optionalJSONSnowflake(channel["parent_id"]); parentErr == nil && parent != nil {
				if parentChannel, found := channelByID[*parent]; found {
					effective = parentChannel
				}
			}
		}
		overwrites := effective["permission_overwrites"]
		if overwrites == nil {
			overwrites = []any{}
		}
		botPermissions, permissionErr := channelPermissions(basePermissions, overwrites, serverID, memberRoles, &botUserID)
		if permissionErr != nil {
			return ServerSnapshot{}, permissionErr
		}
		everyonePermissions, permissionErr := channelPermissions(rolePermissions[serverID], overwrites, serverID, nil, nil)
		if permissionErr != nil {
			return ServerSnapshot{}, permissionErr
		}
		viewerRoleIDs := make([]Snowflake, 0, len(rolePermissions))
		for roleID, permissions := range rolePermissions {
			if roleID == serverID {
				continue
			}
			viewerPermissions, viewerErr := channelPermissions(rolePermissions[serverID]|permissions, overwrites, serverID, map[Snowflake]struct{}{roleID: {}}, nil)
			if viewerErr != nil {
				return ServerSnapshot{}, viewerErr
			}
			if viewerPermissions&PermissionViewChannel != 0 {
				viewerRoleIDs = append(viewerRoleIDs, roleID)
			}
		}
		sortSnowflakes(viewerRoleIDs)
		viewerUserIDs, viewerErr := explicitViewerUserIDs(overwrites)
		if viewerErr != nil {
			return ServerSnapshot{}, viewerErr
		}
		audienceDigest, digestErr := audienceOverwriteDigest(overwrites)
		if digestErr != nil {
			return ServerSnapshot{}, digestErr
		}
		id, idErr := jsonSnowflake(channel["id"])
		parent, parentErr := optionalJSONSnowflake(channel["parent_id"])
		channelName, channelNameOK := channel["name"].(string)
		position, positionErr := jsonInt32Default(channel["position"], 0)
		if idErr != nil || parentErr != nil || !channelNameOK || positionErr != nil {
			return ServerSnapshot{}, apiError("Discord channel metadata is invalid.")
		}
		if position < 0 {
			position = 0
		}
		channels = append(channels, ChannelMetadata{
			ID: id, ServerID: serverID, ParentID: parent, Name: channelName,
			ChannelType: channelType, Position: position,
			EffectiveBotPermissions: botPermissions,
			EveryoneCanView:         everyonePermissions&PermissionViewChannel != 0,
			ViewerRoleIDs:           viewerRoleIDs, ViewerUserIDs: viewerUserIDs,
			AudienceOverwriteSHA256: audienceDigest,
		})
	}
	roles := make([]RoleMetadata, 0, len(rawRoles))
	for _, value := range rawRoles {
		role, ok := value.(map[string]any)
		if !ok {
			continue
		}
		id, idErr := jsonSnowflake(role["id"])
		roleName, roleNameOK := role["name"].(string)
		position, positionErr := jsonInt32Default(role["position"], 0)
		if idErr != nil || !roleNameOK || positionErr != nil {
			return ServerSnapshot{}, apiError("Discord role metadata is invalid.")
		}
		if position < 0 {
			position = 0
		}
		roles = append(roles, RoleMetadata{ID: id, Name: roleName, Position: position})
	}
	slices.SortFunc(channels, func(left, right ChannelMetadata) int {
		if left.Position != right.Position {
			return int(left.Position - right.Position)
		}
		return compareSnowflakes(left.ID, right.ID)
	})
	slices.SortFunc(roles, func(left, right RoleMetadata) int {
		if left.Position != right.Position {
			return int(left.Position - right.Position)
		}
		return compareSnowflakes(left.ID, right.ID)
	})
	return ServerSnapshot{Server: server, Channels: channels, Roles: roles}, nil
}

func (client *RESTClient) request(ctx context.Context, method, path, token string, payload any) (any, error) {
	if token == "" || len(token) > maximumRESTTokenBytes || strings.IndexFunc(token, pythonWhitespace) >= 0 {
		return nil, apiError("Discord bot token is invalid.")
	}
	reference, err := url.Parse(path)
	if err != nil || reference.IsAbs() || reference.Host != "" || strings.HasPrefix(path, "/") {
		return nil, apiError("Discord API request failed.")
	}
	var body io.Reader
	if payload != nil {
		encoded, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, apiError("Discord API request failed.")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL.ResolveReference(reference).String(), body)
	if err != nil {
		return nil, apiError("Discord API request failed.")
	}
	request.Header.Set("Authorization", "Bot "+token)
	request.Header.Set("User-Agent", discordUserAgent)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, apiError("Discord API is unavailable.")
	}
	defer response.Body.Close()
	if response.ContentLength > client.maxResponseBytes {
		return nil, apiError("Discord API response exceeds its bound.")
	}
	content, err := io.ReadAll(io.LimitReader(response.Body, client.maxResponseBytes+1))
	if err != nil {
		return nil, apiError("Discord API is unavailable.")
	}
	if int64(len(content)) > client.maxResponseBytes {
		return nil, apiError("Discord API response exceeds its bound.")
	}
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return nil, apiError("Discord bot token was rejected.")
	case http.StatusForbidden:
		return nil, apiError("Discord bot lacks permission for this operation.")
	case http.StatusTooManyRequests:
		return nil, apiError("Discord API rate limit was reached.")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, apiError("Discord API request failed.")
	}
	if response.StatusCode == http.StatusNoContent {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, apiError("Discord API response is invalid.")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, apiError("Discord API response is invalid.")
	}
	return value, nil
}

func discordRolePermissions(raw []any) (map[Snowflake]Permission, error) {
	result := make(map[Snowflake]Permission, len(raw))
	for _, value := range raw {
		role, ok := value.(map[string]any)
		if !ok {
			return nil, apiError("Discord role metadata is invalid.")
		}
		id, idErr := jsonSnowflake(role["id"])
		permissions, permissionErr := jsonPermission(role["permissions"])
		if idErr != nil || permissionErr != nil {
			return nil, apiError("Discord role metadata is invalid.")
		}
		result[id] = permissions
	}
	return result, nil
}

func channelPermissions(base Permission, raw any, everyoneID Snowflake, roleIDs map[Snowflake]struct{}, memberID *Snowflake) (Permission, error) {
	if base&permissionAdministrator != 0 {
		return allPermissions, nil
	}
	overwrites, ok := raw.([]any)
	if !ok {
		return 0, apiError("Discord permission metadata is invalid.")
	}
	value := base
	for _, rawOverwrite := range overwrites {
		overwrite, overwriteOK := rawOverwrite.(map[string]any)
		if !overwriteOK {
			return 0, apiError("Discord permission metadata is invalid.")
		}
		id, idErr := jsonSnowflake(overwrite["id"])
		kind, kindErr := jsonInt32Default(overwrite["type"], -1)
		if idErr != nil || kindErr != nil {
			return 0, apiError("Discord permission metadata is invalid.")
		}
		if kind == 0 && id == everyoneID {
			var applyErr error
			value, applyErr = applyDiscordOverwrite(value, overwrite)
			if applyErr != nil {
				return 0, applyErr
			}
			break
		}
	}
	var roleAllow, roleDeny Permission
	var memberOverwrite map[string]any
	for _, rawOverwrite := range overwrites {
		overwrite, overwriteOK := rawOverwrite.(map[string]any)
		if !overwriteOK {
			return 0, apiError("Discord permission metadata is invalid.")
		}
		id, _ := jsonSnowflake(overwrite["id"])
		kind, _ := jsonInt32Default(overwrite["type"], -1)
		allow, allowErr := jsonPermissionDefault(overwrite["allow"], 0)
		deny, denyErr := jsonPermissionDefault(overwrite["deny"], 0)
		if allowErr != nil || denyErr != nil {
			return 0, apiError("Discord permission metadata is invalid.")
		}
		if kind == 0 {
			if _, exists := roleIDs[id]; exists {
				roleAllow |= allow
				roleDeny |= deny
			}
		} else if kind == 1 && memberID != nil && id == *memberID {
			memberOverwrite = overwrite
		}
	}
	value = (value &^ roleDeny) | roleAllow
	if memberOverwrite != nil {
		var applyErr error
		value, applyErr = applyDiscordOverwrite(value, memberOverwrite)
		if applyErr != nil {
			return 0, applyErr
		}
	}
	if value&PermissionViewChannel == 0 {
		return 0, nil
	}
	if value&PermissionSendMessages == 0 {
		value &^= PermissionEmbedLinks
	}
	return value, nil
}

func applyDiscordOverwrite(value Permission, overwrite map[string]any) (Permission, error) {
	allow, allowErr := jsonPermissionDefault(overwrite["allow"], 0)
	deny, denyErr := jsonPermissionDefault(overwrite["deny"], 0)
	if allowErr != nil || denyErr != nil {
		return 0, apiError("Discord permission metadata is invalid.")
	}
	return (value &^ deny) | allow, nil
}

func explicitViewerUserIDs(raw any) ([]Snowflake, error) {
	overwrites, ok := raw.([]any)
	if !ok {
		return nil, apiError("Discord permission metadata is invalid.")
	}
	unique := make(map[Snowflake]struct{})
	for _, rawOverwrite := range overwrites {
		overwrite, ok := rawOverwrite.(map[string]any)
		if !ok {
			return nil, apiError("Discord permission metadata is invalid.")
		}
		kind, kindErr := jsonInt32Default(overwrite["type"], -1)
		allow, allowErr := jsonPermissionDefault(overwrite["allow"], 0)
		if kindErr != nil || allowErr != nil {
			return nil, apiError("Discord permission metadata is invalid.")
		}
		if kind != 1 || allow&PermissionViewChannel == 0 {
			continue
		}
		id, idErr := jsonSnowflake(overwrite["id"])
		if idErr != nil {
			return nil, apiError("Discord permission metadata is invalid.")
		}
		unique[id] = struct{}{}
	}
	result := make([]Snowflake, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	sortSnowflakes(result)
	return result, nil
}

type audienceOverwriteTuple struct {
	Type  int32  `json:"type"`
	ID    string `json:"id"`
	Allow int64  `json:"allow"`
	Deny  int64  `json:"deny"`
}

func audienceOverwriteDigest(raw any) ([32]byte, error) {
	overwrites, ok := raw.([]any)
	if !ok {
		return [32]byte{}, apiError("Discord permission metadata is invalid.")
	}
	tuples := make([]audienceOverwriteTuple, 0, len(overwrites))
	for _, rawOverwrite := range overwrites {
		overwrite, ok := rawOverwrite.(map[string]any)
		if !ok {
			return [32]byte{}, apiError("Discord permission metadata is invalid.")
		}
		id, idErr := jsonSnowflake(overwrite["id"])
		kind, kindErr := jsonInt32Default(overwrite["type"], -1)
		allow, allowErr := jsonPermissionDefault(overwrite["allow"], 0)
		deny, denyErr := jsonPermissionDefault(overwrite["deny"], 0)
		if idErr != nil || kindErr != nil || allowErr != nil || denyErr != nil || kind < 0 || kind > 1 {
			return [32]byte{}, apiError("Discord permission metadata is invalid.")
		}
		allow &= PermissionViewChannel
		deny &= PermissionViewChannel
		if allow == 0 && deny == 0 {
			continue
		}
		tuples = append(tuples, audienceOverwriteTuple{
			Type: kind, ID: string(id), Allow: int64(allow), Deny: int64(deny),
		})
	}
	slices.SortFunc(tuples, func(left, right audienceOverwriteTuple) int {
		if left.Type != right.Type {
			return int(left.Type - right.Type)
		}
		if compared := compareSnowflakes(Snowflake(left.ID), Snowflake(right.ID)); compared != 0 {
			return compared
		}
		if left.Allow != right.Allow {
			if left.Allow < right.Allow {
				return -1
			}
			return 1
		}
		if left.Deny < right.Deny {
			return -1
		}
		if left.Deny > right.Deny {
			return 1
		}
		return 0
	})
	canonical, err := json.Marshal(tuples)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}

func jsonSnowflake(value any) (Snowflake, error) {
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case json.Number:
		raw = typed.String()
	default:
		return "", errors.New("snowflake is invalid")
	}
	return ParseSnowflake(raw)
}

func optionalJSONSnowflake(value any) (*Snowflake, error) {
	if value == nil {
		return nil, nil
	}
	id, err := jsonSnowflake(value)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func optionalJSONString(value any) (*string, error) {
	if value == nil {
		return nil, nil
	}
	result, ok := value.(string)
	if !ok {
		return nil, errors.New("string is invalid")
	}
	return &result, nil
}

func jsonSnowflakeSet(value any, nilAsEmpty bool) (map[Snowflake]struct{}, error) {
	if value == nil && nilAsEmpty {
		return map[Snowflake]struct{}{}, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, errors.New("snowflake list is invalid")
	}
	result := make(map[Snowflake]struct{}, len(values))
	for _, value := range values {
		id, err := jsonSnowflake(value)
		if err != nil {
			return nil, err
		}
		result[id] = struct{}{}
	}
	return result, nil
}

func jsonPermission(value any) (Permission, error) {
	var raw string
	switch typed := value.(type) {
	case string:
		raw = typed
	case json.Number:
		raw = typed.String()
	default:
		return 0, errors.New("permission is invalid")
	}
	parsed, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || parsed < 0 {
		return 0, errors.New("permission is invalid")
	}
	return Permission(parsed), nil
}

func jsonPermissionDefault(value any, fallback Permission) (Permission, error) {
	if value == nil {
		return fallback, nil
	}
	return jsonPermission(value)
}

func jsonInt32(value any) (int32, error) { return jsonInt32Default(value, math.MinInt32) }

func jsonInt32Default(value any, fallback int32) (int32, error) {
	if value == nil {
		return fallback, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, fmt.Errorf("integer is invalid")
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 32)
	if err != nil {
		return 0, err
	}
	return int32(parsed), nil
}

func sortSnowflakes(values []Snowflake) {
	slices.SortFunc(values, compareSnowflakes)
}

func compareSnowflakes(left, right Snowflake) int {
	leftValue, _ := strconv.ParseUint(string(left), 10, 64)
	rightValue, _ := strconv.ParseUint(string(right), 10, 64)
	if leftValue < rightValue {
		return -1
	}
	if leftValue > rightValue {
		return 1
	}
	return 0
}
