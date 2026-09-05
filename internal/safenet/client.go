package safenet

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultModelTimeout = 10 * time.Second
	MinModelTimeout     = time.Second
	MaxModelTimeout     = time.Minute
)

var (
	errNetworkPolicy = errors.New("network policy denied connection")
	safeFailureBody  = []byte(`{"error":{"message":"Provider request failed.","type":"provider_error"}}`)
	forbiddenHeaders = map[string]struct{}{
		"connection": {}, "content-length": {}, "host": {}, "keep-alive": {},
		"te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {},
	}
)

type Resolver func(context.Context, string, int) ([]string, error)

type Response struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

type Client struct {
	base    *url.URL
	origin  string
	http    *http.Client
	headers http.Header
	timeout time.Duration
}

type ClientOptions struct {
	Headers   map[string]string
	Resolver  Resolver
	TLSConfig *tls.Config
	Timeout   time.Duration
}

func NewClient(rawBaseURL string, policy Policy, options ClientOptions) (*Client, error) {
	base, err := NormalizeBaseURL(rawBaseURL, policy)
	if err != nil {
		return nil, err
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = DefaultModelTimeout
	}
	if timeout < MinModelTimeout || timeout > MaxModelTimeout {
		return nil, errors.New("timeout is outside the admitted bound")
	}
	if err := validateHeaders(options.Headers); err != nil {
		return nil, err
	}
	tlsConfig := options.TLSConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		tlsConfig = tlsConfig.Clone()
		if tlsConfig.InsecureSkipVerify {
			return nil, errors.New("TLS certificate and hostname verification are required")
		}
	}
	tlsConfig.ServerName = base.Hostname()
	resolver := options.Resolver
	if resolver == nil {
		resolver = defaultResolver
	}
	requirePrivate := base.Scheme == "http"
	pinned := &pinnedDialer{
		policy: policy, requirePrivate: requirePrivate, resolver: resolver,
		connectTimeout: minDuration(3*time.Second, timeout),
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            pinned.DialContext,
		TLSClientConfig:        tlsConfig,
		DisableCompression:     true,
		ForceAttemptHTTP2:      false,
		MaxConnsPerHost:        4,
		MaxIdleConnsPerHost:    2,
		IdleConnTimeout:        15 * time.Second,
		TLSHandshakeTimeout:    minDuration(3*time.Second, timeout),
		ResponseHeaderTimeout:  timeout,
		ExpectContinueTimeout:  minDuration(time.Second, timeout),
		MaxResponseHeaderBytes: 64 << 10,
		TLSNextProto:           map[string]func(string, *tls.Conn) http.RoundTripper{},
	}
	return &Client{
		base:    base,
		origin:  originKey(base),
		timeout: timeout,
		headers: cloneHeaders(options.Headers),
		http: &http.Client{
			Transport: &originTransport{origin: originKey(base), delegate: transport},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (client *Client) CloseIdleConnections() {
	client.http.CloseIdleConnections()
}

func (client *Client) Exchange(
	ctx context.Context,
	method, path string,
	headers map[string]string,
	body []byte,
	maxResponseBytes int,
) (Response, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method != http.MethodGet && method != http.MethodPost {
		return Response{}, requestError(PolicyDenied)
	}
	relative, err := NormalizeRelativePath(path)
	if err != nil {
		return Response{}, err
	}
	if maxResponseBytes < 1 || maxResponseBytes > MaxBodyBytes || len(body) > MaxBodyBytes {
		return Response{}, requestError(ResponseTooLarge)
	}
	if err := validateHeaders(headers); err != nil {
		return Response{}, err
	}
	requestURL := *client.base
	requestURL.Path = strings.TrimSuffix(client.base.Path, "/") + "/" + relative
	requestURL.RawPath = ""
	requestContext, cancel := context.WithTimeout(ctx, client.timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, method, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return Response{}, requestError(InvalidURL)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	for name, values := range client.headers {
		request.Header[name] = append([]string(nil), values...)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if isJSONMediaType(request.Header.Get("Content-Type")) {
		if _, err := ParseBoundedJSON(body); err != nil {
			return Response{}, err
		}
	}
	response, err := client.http.Do(request)
	if err != nil {
		return Response{}, classifyNetworkError(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		return Response{}, &RequestError{Code: Redirect, HTTPStatus: response.StatusCode}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		safeHeaders := responseHeaders(response.Header)
		safeHeaders["content-type"] = "application/json"
		return Response{
			Status:  response.StatusCode,
			Headers: safeHeaders,
			Body:    append([]byte(nil), safeFailureBody...),
		}, nil
	}
	if response.ContentLength > int64(maxResponseBytes) {
		return Response{}, requestError(ResponseTooLarge)
	}
	received, err := io.ReadAll(io.LimitReader(response.Body, int64(maxResponseBytes)+1))
	if err != nil {
		return Response{}, classifyNetworkError(err)
	}
	if len(received) > maxResponseBytes {
		return Response{}, requestError(ResponseTooLarge)
	}
	secret := bearerSecret(request.Header.Get("Authorization"))
	if response.StatusCode >= 200 && response.StatusCode < 300 && secret != "" {
		received, err = sanitizeResponse(received, response.Header.Get("Content-Type"), request.Header.Get("Accept"), secret)
		if err != nil {
			return Response{}, err
		}
	}
	safeHeaders := responseHeaders(response.Header)
	return Response{Status: response.StatusCode, Headers: safeHeaders, Body: received}, nil
}

func (client *Client) GetJSON(ctx context.Context, path string) (any, int, error) {
	response, err := client.Exchange(ctx, http.MethodGet, path, nil, nil, MaxBodyBytes)
	if err != nil {
		return nil, 0, err
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, response.Status, &RequestError{
			Code: HTTPStatus, Retryable: response.Status == http.StatusTooManyRequests || response.Status >= 500,
			HTTPStatus: response.Status, RetryHeaders: map[string]string{"retry-after": response.Headers["retry-after"], "retry-after-ms": response.Headers["retry-after-ms"]},
		}
	}
	value, err := ParseBoundedJSON(response.Body)
	return value, response.Status, err
}

func (client *Client) PostJSON(ctx context.Context, path string, value any) (any, int, error) {
	body, err := marshalBoundedJSON(value)
	if err != nil {
		return nil, 0, err
	}
	response, err := client.Exchange(ctx, http.MethodPost, path, map[string]string{"Content-Type": "application/json"}, body, MaxBodyBytes)
	if err != nil {
		return nil, 0, err
	}
	if response.Status < 200 || response.Status >= 300 {
		return nil, response.Status, &RequestError{
			Code: HTTPStatus, Retryable: response.Status == http.StatusTooManyRequests || response.Status >= 500,
			HTTPStatus: response.Status, RetryHeaders: map[string]string{"retry-after": response.Headers["retry-after"], "retry-after-ms": response.Headers["retry-after-ms"]},
		}
	}
	decoded, err := ParseBoundedJSON(response.Body)
	return decoded, response.Status, err
}

type pinnedDialer struct {
	policy         Policy
	requirePrivate bool
	resolver       Resolver
	connectTimeout time.Duration
}

func (dialer *pinnedDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errNetworkPolicy
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		return nil, errNetworkPolicy
	}
	resolved, err := dialer.resolver(ctx, host, port)
	if err != nil {
		return nil, fmt.Errorf("resolve provider host: %w", err)
	}
	addresses := make([]string, 0, len(resolved))
	seen := map[string]struct{}{}
	for _, raw := range resolved {
		parsed := net.ParseIP(raw)
		if parsed == nil || strings.Contains(raw, "%") {
			return nil, errNetworkPolicy
		}
		canonical := parsed.String()
		if !AddressAllowed(canonical, dialer.policy, dialer.requirePrivate) {
			return nil, errNetworkPolicy
		}
		if _, exists := seen[canonical]; !exists {
			seen[canonical] = struct{}{}
			addresses = append(addresses, canonical)
		}
	}
	if len(addresses) == 0 {
		return nil, errNetworkPolicy
	}
	connectContext, cancel := context.WithTimeout(ctx, dialer.connectTimeout)
	defer cancel()
	networkDialer := &net.Dialer{}
	var lastError error
	for _, numeric := range addresses {
		connection, err := networkDialer.DialContext(connectContext, network, net.JoinHostPort(numeric, rawPort))
		if err == nil {
			return connection, nil
		}
		lastError = err
	}
	return nil, lastError
}

type originTransport struct {
	origin   string
	delegate http.RoundTripper
}

func (transport *originTransport) CloseIdleConnections() {
	if closer, ok := transport.delegate.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (transport *originTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if originKey(request.URL) != transport.origin || request.Host != "" && request.Host != request.URL.Host {
		return nil, errNetworkPolicy
	}
	if err := validateHeadersMap(request.Header); err != nil {
		return nil, err
	}
	if request.ContentLength > MaxBodyBytes {
		return nil, errNetworkPolicy
	}
	if len(request.TransferEncoding) != 0 {
		return nil, errNetworkPolicy
	}
	return transport.delegate.RoundTrip(request)
}

func defaultResolver(ctx context.Context, host string, _ int) ([]string, error) {
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(addresses))
	for index, address := range addresses {
		result[index] = address.String()
	}
	return result, nil
}

func validateHeaders(headers map[string]string) error {
	for name, value := range headers {
		lowered := strings.ToLower(name)
		if !validHeaderName(name) || strings.ContainsAny(value, "\r\n") {
			return requestError(InvalidURL)
		}
		if _, denied := forbiddenHeaders[lowered]; denied || strings.HasPrefix(lowered, "proxy-") {
			return requestError(PolicyDenied)
		}
	}
	return nil
}

func cloneHeaders(headers map[string]string) http.Header {
	cloned := make(http.Header, len(headers))
	for name, value := range headers {
		cloned.Set(name, value)
	}
	return cloned
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, character := range []byte(name) {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') &&
			!(character >= '0' && character <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(character)) {
			return false
		}
	}
	return true
}

func validateHeadersMap(headers http.Header) error {
	for name := range headers {
		lowered := strings.ToLower(name)
		if _, denied := forbiddenHeaders[lowered]; denied && lowered != "content-length" || strings.HasPrefix(lowered, "proxy-") {
			return errNetworkPolicy
		}
	}
	return nil
}

func originKey(value *url.URL) string {
	port := value.Port()
	if port == "" {
		if value.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	return strings.ToLower(value.Scheme) + "://" + strings.ToLower(value.Hostname()) + ":" + port
}

func classifyNetworkError(err error) error {
	if errors.Is(err, errNetworkPolicy) {
		return requestError(PolicyDenied)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &RequestError{Code: Timeout, Retryable: true}
	}
	var netError net.Error
	if errors.As(err, &netError) && netError.Timeout() {
		return &RequestError{Code: Timeout, Retryable: true}
	}
	return &RequestError{Code: Connection, Retryable: true}
}

func marshalBoundedJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > MaxBodyBytes {
		if len(encoded) > MaxBodyBytes {
			return nil, requestError(ResponseTooLarge)
		}
		return nil, requestError(InvalidJSON)
	}
	if _, err := ParseBoundedJSON(encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func isJSONMediaType(value string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func bearerSecret(value string) string {
	if len(value) > 7 && strings.EqualFold(value[:7], "bearer ") {
		return value[7:]
	}
	return ""
}

func sanitizeResponse(body []byte, contentType, accept, secret string) ([]byte, error) {
	if isJSONMediaType(contentType) {
		value, err := ParseBoundedJSON(body)
		if err != nil {
			return nil, err
		}
		redacted, err := redactJSON(value, secret)
		if err != nil {
			return nil, err
		}
		return marshalBoundedJSON(redacted)
	}
	if strings.EqualFold(strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]), "text/event-stream") || strings.Contains(strings.ToLower(accept), "text/event-stream") {
		return sanitizeSSE(body, secret)
	}
	return body, nil
}

func redactJSON(value any, secret string) (any, error) {
	switch typed := value.(type) {
	case string:
		return strings.ReplaceAll(typed, secret, "[REDACTED]"), nil
	case []any:
		for index := range typed {
			redacted, err := redactJSON(typed[index], secret)
			if err != nil {
				return nil, err
			}
			typed[index] = redacted
		}
	case map[string]any:
		rewritten := make(map[string]any, len(typed))
		for name, item := range typed {
			safeName := strings.ReplaceAll(name, secret, "[REDACTED]")
			if _, collision := rewritten[safeName]; collision {
				return nil, requestError(InvalidJSON)
			}
			redacted, err := redactJSON(item, secret)
			if err != nil {
				return nil, err
			}
			rewritten[safeName] = redacted
		}
		return rewritten, nil
	}
	return value, nil
}

func sanitizeSSE(body []byte, secret string) ([]byte, error) {
	if !utf8.Valid(body) {
		return nil, requestError(InvalidJSON)
	}
	normalized := strings.ReplaceAll(strings.ReplaceAll(string(body), "\r\n", "\n"), "\r", "\n")
	events := strings.Split(normalized, "\n\n")
	for eventIndex, event := range events {
		lines := strings.Split(event, "\n")
		data := []string{}
		nonData := []string{}
		for _, line := range lines {
			if line == "data" {
				data = append(data, "")
			} else if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(line[5:]))
			} else {
				nonData = append(nonData, line)
			}
		}
		if len(data) == 0 || strings.Join(data, "\n") == "[DONE]" {
			continue
		}
		value, err := ParseBoundedJSON([]byte(strings.Join(data, "\n")))
		if err != nil {
			return nil, err
		}
		redacted, err := redactJSON(value, secret)
		if err != nil {
			return nil, err
		}
		encoded, err := marshalBoundedJSON(redacted)
		if err != nil {
			return nil, err
		}
		events[eventIndex] = strings.Join(append(nonData, "data: "+string(encoded)), "\n")
	}
	rendered := []byte(strings.Join(events, "\n\n"))
	if len(rendered) > MaxBodyBytes {
		return nil, requestError(ResponseTooLarge)
	}
	return rendered, nil
}

func responseHeaders(headers http.Header) map[string]string {
	safe := map[string]string{}
	for _, name := range []string{"Content-Type", "Retry-After", "Retry-After-Ms", "X-Request-Id"} {
		if value := headers.Get(name); value != "" {
			safe[strings.ToLower(name)] = value
		}
	}
	return safe
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}
