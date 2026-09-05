package sources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/security"
	"golang.org/x/text/unicode/norm"
)

const (
	tinyFishServiceURL       = "https://api.fetch.tinyfish.ai/"
	tinyFishMinTimeout       = 150 * time.Second
	tinyFishMaxTimeout       = 600 * time.Second
	tinyFishDefaultTimeout   = 165 * time.Second
	tinyFishMaxBatch         = 10
	tinyFishPerURLTimeoutMS  = 110_000
	tinyFishMaxResponseBytes = 32 * 1024 * 1024
	tinyFishMaxJSONDepth     = 16
	tinyFishAPIKeyMaxBytes   = 512
)

type TinyFishFailureCode string

const (
	TinyFishValidation       TinyFishFailureCode = "validation"
	TinyFishAuth             TinyFishFailureCode = "auth"
	TinyFishUnprocessable    TinyFishFailureCode = "unprocessable"
	TinyFishRateLimited      TinyFishFailureCode = "rate_limited"
	TinyFishServer           TinyFishFailureCode = "server"
	TinyFishTimeout          TinyFishFailureCode = "timeout"
	TinyFishConnection       TinyFishFailureCode = "connection"
	TinyFishRedirect         TinyFishFailureCode = "redirect"
	TinyFishResponseTooLarge TinyFishFailureCode = "response_too_large"
	TinyFishInvalidResponse  TinyFishFailureCode = "invalid_response"
	TinyFishUnspecified      TinyFishFailureCode = "unspecified"
	TinyFishRobots           TinyFishFailureCode = "robots"
	TinyFishPolicy           TinyFishFailureCode = "policy"
	TinyFishLimit            TinyFishFailureCode = "limit"
	TinyFishStorage          TinyFishFailureCode = "storage"
	TinyFishContent          TinyFishFailureCode = "content"
)

var tinyFishSafeMessages = map[TinyFishFailureCode]string{
	TinyFishValidation:       "TinyFish fetch request was rejected as invalid.",
	TinyFishAuth:             "TinyFish fetch authentication failed.",
	TinyFishUnprocessable:    "TinyFish fetch could not process the request.",
	TinyFishRateLimited:      "TinyFish fetch rate limit was exceeded.",
	TinyFishServer:           "TinyFish fetch service failed.",
	TinyFishTimeout:          "TinyFish fetch request timed out.",
	TinyFishConnection:       "TinyFish fetch connection failed.",
	TinyFishRedirect:         "TinyFish fetch service redirect was rejected.",
	TinyFishResponseTooLarge: "TinyFish fetch response exceeded the size limit.",
	TinyFishInvalidResponse:  "TinyFish fetch response shape was invalid.",
	TinyFishUnspecified:      "TinyFish fetch request failed.",
	TinyFishRobots:           "TinyFish website robots policy denied the crawl.",
	TinyFishPolicy:           "TinyFish website URL is outside the allowed policy.",
	TinyFishLimit:            "TinyFish crawl exceeded its size or page limits.",
	TinyFishStorage:          "TinyFish snapshot could not be stored.",
	TinyFishContent:          "TinyFish crawl produced no usable content.",
}

type TinyFishError struct {
	Code       TinyFishFailureCode
	Retryable  bool
	HTTPStatus int
}

func (failure *TinyFishError) Error() string {
	if message, exists := tinyFishSafeMessages[failure.Code]; exists {
		return message
	}
	return tinyFishSafeMessages[TinyFishUnspecified]
}

func newTinyFishError(code TinyFishFailureCode, status int) *TinyFishError {
	retryable := code == TinyFishRateLimited || code == TinyFishServer || code == TinyFishTimeout || code == TinyFishConnection || code == TinyFishStorage
	return &TinyFishError{Code: code, Retryable: retryable, HTTPStatus: status}
}

type TinyFishPageResult struct {
	RequestedURL string
	FinalURL     *string
	Text         string
	Links        []string
}

type TinyFishPageError struct {
	RequestedURL string
	Code         TinyFishFailureCode
	HTTPStatus   int
}

type TinyFishBatchResult struct {
	Pages  []TinyFishPageResult
	Errors []TinyFishPageError
}

type TinyFishHTTPResponse struct {
	Status int
	Body   []byte
}

type TinyFishHTTPTransport interface {
	Post(context.Context, string, map[string]string, []byte, time.Duration) (TinyFishHTTPResponse, error)
}

type TinyFishBatchFetcher interface {
	Fetch(context.Context, []string, *security.SecretValue) (TinyFishBatchResult, error)
}

type TinyFishHTTPClientTransport struct {
	client *http.Client
}

func NewTinyFishHTTPClientTransport() *TinyFishHTTPClientTransport {
	transport := &http.Transport{
		Proxy:                  nil,
		DisableCompression:     true,
		MaxResponseHeaderBytes: 64 * 1024,
	}
	return &TinyFishHTTPClientTransport{client: &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

func (transport *TinyFishHTTPClientTransport) Post(ctx context.Context, rawURL string, headers map[string]string, body []byte, timeout time.Duration) (TinyFishHTTPResponse, error) {
	if transport == nil || transport.client == nil {
		return TinyFishHTTPResponse{}, errors.New("tinyfish transport is unavailable")
	}
	requestContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return TinyFishHTTPResponse{}, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := transport.client.Do(request)
	if err != nil {
		return TinyFishHTTPResponse{}, err
	}
	defer response.Body.Close()
	if response.ContentLength > tinyFishMaxResponseBytes {
		return TinyFishHTTPResponse{}, newTinyFishError(TinyFishResponseTooLarge, 0)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, tinyFishMaxResponseBytes+1))
	if err != nil {
		return TinyFishHTTPResponse{}, err
	}
	if len(responseBody) > tinyFishMaxResponseBytes {
		return TinyFishHTTPResponse{}, newTinyFishError(TinyFishResponseTooLarge, 0)
	}
	return TinyFishHTTPResponse{Status: response.StatusCode, Body: responseBody}, nil
}

type TinyFishFetchClient struct {
	transport TinyFishHTTPTransport
	timeout   time.Duration
}

func NewTinyFishFetchClient(transport TinyFishHTTPTransport, timeout time.Duration) (*TinyFishFetchClient, error) {
	if transport == nil {
		return nil, errors.New("tinyfish transport is required")
	}
	if timeout == 0 {
		timeout = tinyFishDefaultTimeout
	}
	if timeout < tinyFishMinTimeout || timeout > tinyFishMaxTimeout {
		return nil, errors.New("tinyfish timeout must be between 150 and 600 seconds")
	}
	return &TinyFishFetchClient{transport: transport, timeout: timeout}, nil
}

func (client *TinyFishFetchClient) Fetch(ctx context.Context, urls []string, apiKey *security.SecretValue) (TinyFishBatchResult, error) {
	requested, err := validateTinyFishURLs(urls)
	if err != nil {
		return TinyFishBatchResult{}, err
	}
	key, err := validateTinyFishAPIKey(apiKey)
	if err != nil {
		return TinyFishBatchResult{}, err
	}
	body, err := json.Marshal(struct {
		URLs            []string `json:"urls"`
		Format          string   `json:"format"`
		Links           bool     `json:"links"`
		TTL             int      `json:"ttl"`
		PerURLTimeoutMS int      `json:"per_url_timeout_ms"`
	}{URLs: requested, Format: "markdown", Links: true, TTL: 0, PerURLTimeoutMS: tinyFishPerURLTimeoutMS})
	if err != nil {
		return TinyFishBatchResult{}, newTinyFishError(TinyFishUnspecified, 0)
	}
	response, err := client.transport.Post(ctx, tinyFishServiceURL, map[string]string{
		"Accept":       "application/json",
		"Content-Type": "application/json",
		"X-API-Key":    key,
	}, body, client.timeout)
	if err != nil {
		var failure *TinyFishError
		if errors.As(err, &failure) {
			return TinyFishBatchResult{}, failure
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return TinyFishBatchResult{}, newTinyFishError(TinyFishTimeout, 0)
		}
		var network net.Error
		if errors.As(err, &network) && network.Timeout() {
			return TinyFishBatchResult{}, newTinyFishError(TinyFishTimeout, 0)
		}
		if errors.As(err, &network) {
			return TinyFishBatchResult{}, newTinyFishError(TinyFishConnection, 0)
		}
		return TinyFishBatchResult{}, newTinyFishError(TinyFishUnspecified, 0)
	}
	if response.Status >= 300 && response.Status < 400 {
		return TinyFishBatchResult{}, newTinyFishError(TinyFishRedirect, 0)
	}
	if response.Status != http.StatusOK {
		return TinyFishBatchResult{}, newTinyFishError(tinyFishHTTPFailure(response.Status), response.Status)
	}
	if len(response.Body) > tinyFishMaxResponseBytes {
		return TinyFishBatchResult{}, newTinyFishError(TinyFishResponseTooLarge, 0)
	}
	payload, err := parseBoundedTinyFishJSON(response.Body)
	if err != nil {
		return TinyFishBatchResult{}, err
	}
	return parseTinyFishPayload(payload, requested)
}

func validateTinyFishURLs(values []string) ([]string, error) {
	if len(values) < 1 || len(values) > tinyFishMaxBatch {
		return nil, errors.New("tinyfish fetch requires 1 to 10 URLs")
	}
	result := slices.Clone(values)
	seen := make(map[string]struct{}, len(result))
	for _, value := range result {
		if value == "" {
			return nil, errors.New("tinyfish fetch URLs must be non-empty strings")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("tinyfish fetch URLs must be unique")
		}
		seen[value] = struct{}{}
	}
	return result, nil
}

func validateTinyFishAPIKey(secret *security.SecretValue) (string, error) {
	if secret == nil {
		return "", errors.New("tinyfish API key is invalid")
	}
	value := secret.Reveal()
	if value == "" || strings.TrimSpace(value) == "" || len([]byte(value)) > tinyFishAPIKeyMaxBytes {
		return "", errors.New("tinyfish API key is invalid")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return "", errors.New("tinyfish API key is invalid")
		}
	}
	return value, nil
}

func tinyFishHTTPFailure(status int) TinyFishFailureCode {
	switch {
	case status == 400:
		return TinyFishValidation
	case status == 401:
		return TinyFishAuth
	case status == 422:
		return TinyFishUnprocessable
	case status == 429:
		return TinyFishRateLimited
	case status >= 500 && status <= 599:
		return TinyFishServer
	default:
		return TinyFishUnspecified
	}
}

var tinyFishPerURLErrors = map[string]TinyFishFailureCode{
	"timeout":                TinyFishTimeout,
	"rate_limited":           TinyFishRateLimited,
	"too_many_requests":      TinyFishRateLimited,
	"bad_request":            TinyFishValidation,
	"unauthorized":           TinyFishAuth,
	"forbidden":              TinyFishAuth,
	"payment_required":       TinyFishAuth,
	"unprocessable":          TinyFishUnprocessable,
	"unsupported_media_type": TinyFishUnprocessable,
	"internal_error":         TinyFishServer,
	"internal_server_error":  TinyFishServer,
	"server_error":           TinyFishServer,
	"bad_gateway":            TinyFishServer,
	"service_unavailable":    TinyFishServer,
	"gateway_timeout":        TinyFishServer,
}

func tinyFishPerURLFailure(value string, status int) TinyFishFailureCode {
	if status != 0 {
		mapped := tinyFishHTTPFailure(status)
		if mapped != TinyFishUnspecified {
			return mapped
		}
	}
	if mapped, exists := tinyFishPerURLErrors[strings.ToLower(value)]; exists {
		return mapped
	}
	return TinyFishUnspecified
}

func parseBoundedTinyFishJSON(raw []byte) (any, error) {
	if !utf8.Valid(raw) {
		return nil, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	type entry struct {
		value any
		depth int
	}
	pending := []entry{{value: value, depth: 1}}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current.depth > tinyFishMaxJSONDepth {
			return nil, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		switch value := current.value.(type) {
		case map[string]any:
			for _, item := range value {
				pending = append(pending, entry{value: item, depth: current.depth + 1})
			}
		case []any:
			for _, item := range value {
				pending = append(pending, entry{value: item, depth: current.depth + 1})
			}
		}
	}
	return value, nil
}

func parseTinyFishPayload(payload any, requested []string) (TinyFishBatchResult, error) {
	object, okay := payload.(map[string]any)
	if !okay {
		return TinyFishBatchResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	results, resultsOkay := object["results"].([]any)
	errorsValue, errorsOkay := object["errors"].([]any)
	if !resultsOkay || !errorsOkay {
		return TinyFishBatchResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	requestedSet := make(map[string]struct{}, len(requested))
	for _, value := range requested {
		requestedSet[value] = struct{}{}
	}
	pages := make(map[string]TinyFishPageResult)
	failures := make(map[string]TinyFishPageError)
	for _, raw := range results {
		page, err := parseTinyFishPage(raw, requestedSet)
		if err != nil {
			return TinyFishBatchResult{}, err
		}
		if _, duplicatePage := pages[page.RequestedURL]; duplicatePage {
			return TinyFishBatchResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		if _, duplicateFailure := failures[page.RequestedURL]; duplicateFailure {
			return TinyFishBatchResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		pages[page.RequestedURL] = page
	}
	for _, raw := range errorsValue {
		failure, err := parseTinyFishPageError(raw, requestedSet)
		if err != nil {
			return TinyFishBatchResult{}, err
		}
		if _, duplicatePage := pages[failure.RequestedURL]; duplicatePage {
			return TinyFishBatchResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		if _, duplicateFailure := failures[failure.RequestedURL]; duplicateFailure {
			return TinyFishBatchResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		failures[failure.RequestedURL] = failure
	}
	if len(pages)+len(failures) != len(requestedSet) {
		return TinyFishBatchResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	pageKeys := make([]string, 0, len(pages))
	for key := range pages {
		pageKeys = append(pageKeys, key)
	}
	slices.Sort(pageKeys)
	failureKeys := make([]string, 0, len(failures))
	for key := range failures {
		failureKeys = append(failureKeys, key)
	}
	slices.Sort(failureKeys)
	result := TinyFishBatchResult{Pages: make([]TinyFishPageResult, 0, len(pageKeys)), Errors: make([]TinyFishPageError, 0, len(failureKeys))}
	for _, key := range pageKeys {
		result.Pages = append(result.Pages, pages[key])
	}
	for _, key := range failureKeys {
		result.Errors = append(result.Errors, failures[key])
	}
	return result, nil
}

func parseTinyFishPage(raw any, requested map[string]struct{}) (TinyFishPageResult, error) {
	object, okay := raw.(map[string]any)
	if !okay {
		return TinyFishPageResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	requestedURL, urlOkay := object["url"].(string)
	format, formatOkay := object["format"].(string)
	text, textOkay := object["text"].(string)
	if !urlOkay || !formatOkay || format != "markdown" || !textOkay {
		return TinyFishPageResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	if _, exists := requested[requestedURL]; !exists {
		return TinyFishPageResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	var finalURL *string
	if value, exists := object["final_url"]; exists && value != nil {
		parsed, okay := value.(string)
		if !okay || parsed == "" {
			return TinyFishPageResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		finalURL = &parsed
	}
	links := make([]string, 0)
	if value, exists := object["links"]; exists {
		array, okay := value.([]any)
		if !okay {
			return TinyFishPageResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		for _, rawLink := range array {
			link, okay := rawLink.(string)
			if !okay {
				return TinyFishPageResult{}, newTinyFishError(TinyFishInvalidResponse, 0)
			}
			links = append(links, link)
		}
	}
	return TinyFishPageResult{RequestedURL: requestedURL, FinalURL: finalURL, Text: text, Links: links}, nil
}

func parseTinyFishPageError(raw any, requested map[string]struct{}) (TinyFishPageError, error) {
	object, okay := raw.(map[string]any)
	if !okay {
		return TinyFishPageError{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	requestedURL, urlOkay := object["url"].(string)
	message, messageOkay := object["error"].(string)
	if !urlOkay || !messageOkay || message == "" {
		return TinyFishPageError{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	if _, exists := requested[requestedURL]; !exists {
		return TinyFishPageError{}, newTinyFishError(TinyFishInvalidResponse, 0)
	}
	status := 0
	if value, exists := object["status"]; exists && value != nil {
		number, okay := value.(json.Number)
		if !okay {
			return TinyFishPageError{}, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		parsed, err := strconv.Atoi(number.String())
		if err != nil || parsed < 100 || parsed > 599 {
			return TinyFishPageError{}, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		status = parsed
	}
	return TinyFishPageError{RequestedURL: requestedURL, Code: tinyFishPerURLFailure(message, status), HTTPStatus: status}, nil
}

func (adapter *WebsiteSourceAdapter) validateTinyFish(ctx context.Context, request WebsiteRequest) (string, error) {
	if adapter.tinyfish == nil {
		return "", errors.New("tinyfish source adapter is unavailable")
	}
	origin, root, err := tinyFishRoot(request.RemoteURL)
	if err != nil {
		return "", tinyFishWebsiteError(err)
	}
	budget := newCrawlBudget(adapter.http, request.Limits)
	policy, err := adapter.tinyFishRobots(ctx, origin, budget)
	if err != nil {
		return "", tinyFishWebsiteError(err)
	}
	if !policy.canFetch(root) {
		return "", tinyFishWebsiteError(newTinyFishError(TinyFishRobots, 0))
	}
	batch, err := adapter.tinyfish.Fetch(ctx, []string{root}, request.TinyFishCredential)
	if err != nil {
		return "", tinyFishWebsiteError(err)
	}
	if len(batch.Errors) != 0 || len(batch.Pages) != 1 || batch.Pages[0].RequestedURL != root {
		return "", tinyFishWebsiteError(newTinyFishError(TinyFishInvalidResponse, 0))
	}
	final, err := requireTinyFishFinal(batch.Pages[0].FinalURL, root, origin)
	if err != nil {
		return "", tinyFishWebsiteError(err)
	}
	if !policy.canFetch(final) {
		return "", tinyFishWebsiteError(newTinyFishError(TinyFishRobots, 0))
	}
	content, err := normalizeTinyFishMarkdown(batch.Pages[0].Text, int64(request.Limits.MaxPageBytes))
	if err != nil {
		return "", tinyFishWebsiteError(err)
	}
	digest := stableWebsiteDigest(map[string][32]byte{final: sha256Bytes(content)})
	return hexDigest(digest), nil
}

func (adapter *WebsiteSourceAdapter) materializeTinyFish(ctx context.Context, request WebsiteRequest) (WebsiteSnapshot, error) {
	if adapter.tinyfish == nil {
		return WebsiteSnapshot{}, errors.New("tinyfish source adapter is unavailable")
	}
	if _, err := validateTinyFishAPIKey(request.TinyFishCredential); err != nil {
		return WebsiteSnapshot{}, err
	}
	origin, root, err := tinyFishRoot(request.RemoteURL)
	if err != nil {
		return WebsiteSnapshot{}, tinyFishWebsiteError(err)
	}
	budget := newCrawlBudget(adapter.http, request.Limits)
	policy, err := adapter.tinyFishRobots(ctx, origin, budget)
	if err != nil {
		return WebsiteSnapshot{}, tinyFishWebsiteError(err)
	}
	if !policy.canFetch(root) {
		return WebsiteSnapshot{}, tinyFishWebsiteError(newTinyFishError(TinyFishRobots, 0))
	}
	pages, err := adapter.tinyFishCrawl(ctx, root, origin, request.TinyFishCredential, policy, request.Limits)
	if err != nil {
		return WebsiteSnapshot{}, tinyFishWebsiteError(err)
	}
	if len(pages) == 0 {
		return WebsiteSnapshot{}, tinyFishWebsiteError(newTinyFishError(TinyFishContent, 0))
	}
	snapshot, err := adapter.publishWebsiteSnapshot(request, pages)
	if err != nil {
		var failure *WebsiteFailure
		if errors.As(err, &failure) {
			return WebsiteSnapshot{}, websiteFailure(string(TinyFishStorage), true)
		}
		return WebsiteSnapshot{}, err
	}
	return snapshot, nil
}

func tinyFishRoot(remoteURL string) (string, string, error) {
	origin, err := websiteOrigin(remoteURL)
	if err != nil {
		return "", "", newTinyFishError(TinyFishPolicy, 0)
	}
	root, err := canonicalWebsiteURL(remoteURL, origin, origin)
	if err != nil {
		return "", "", newTinyFishError(TinyFishPolicy, 0)
	}
	return origin, root, nil
}

func (adapter *WebsiteSourceAdapter) tinyFishRobots(ctx context.Context, origin string, budget *crawlBudget) (robotsPolicy, error) {
	response, _, err := adapter.getWithHeaders(ctx, origin+"/robots.txt", origin, map[string]string{
		"Accept":          "text/plain",
		"Accept-Encoding": "identity",
		"User-Agent":      websiteUserAgent,
	}, maxRobotsBytes, budget, nil)
	if err != nil {
		return robotsPolicy{}, err
	}
	if response.Status == http.StatusNotFound {
		return robotsPolicy{}, nil
	}
	if response.Status != http.StatusOK {
		failure := newTinyFishError(TinyFishRobots, 0)
		failure.Retryable = response.Status >= 500
		return robotsPolicy{}, failure
	}
	policy, _, err := parseRobots(response.Body)
	if err != nil {
		return robotsPolicy{}, newTinyFishError(TinyFishRobots, 0)
	}
	return policy, nil
}

func (adapter *WebsiteSourceAdapter) tinyFishCrawl(ctx context.Context, root, origin string, key *security.SecretValue, policy robotsPolicy, limits CrawlLimits) (map[string]crawledWebsitePage, error) {
	type queuedURL struct {
		URL   string
		Depth int
	}
	queue := []queuedURL{{URL: root}}
	enqueued := map[string]struct{}{root: {}}
	discoveredCap := min(limits.MaxPages*discoveredMultiplier, discoveredCeiling) + 1
	pages := make(map[string]crawledWebsitePage)
	var totalBytes int64
	for len(queue) > 0 && len(pages) < limits.MaxPages {
		remaining := limits.MaxPages - len(pages)
		batch := make([]queuedURL, 0, min(min(limits.Concurrency, remaining), tinyFishMaxBatch))
		for len(queue) > 0 && len(batch) < cap(batch) {
			item := queue[0]
			queue = queue[1:]
			if item.Depth <= limits.MaxDepth && policy.canFetch(item.URL) {
				batch = append(batch, item)
			}
		}
		if len(batch) == 0 {
			continue
		}
		requested := make([]string, 0, len(batch))
		depths := make(map[string]int, len(batch))
		for _, item := range batch {
			requested = append(requested, item.URL)
			depths[item.URL] = item.Depth
		}
		sortedRequested := slices.Clone(requested)
		slices.Sort(sortedRequested)
		result, err := adapter.tinyfish.Fetch(ctx, sortedRequested, key)
		if err != nil {
			return nil, err
		}
		if len(result.Errors) > 0 {
			slices.SortFunc(result.Errors, func(left, right TinyFishPageError) int { return strings.Compare(left.RequestedURL, right.RequestedURL) })
			return nil, newTinyFishError(result.Errors[0].Code, result.Errors[0].HTTPStatus)
		}
		byURL := make(map[string]TinyFishPageResult, len(result.Pages))
		for _, page := range result.Pages {
			byURL[page.RequestedURL] = page
		}
		if len(byURL) != len(requested) {
			return nil, newTinyFishError(TinyFishInvalidResponse, 0)
		}
		for _, requestedURL := range requested {
			item, exists := byURL[requestedURL]
			if !exists {
				return nil, newTinyFishError(TinyFishInvalidResponse, 0)
			}
			final, err := requireTinyFishFinal(item.FinalURL, requestedURL, origin)
			if err != nil {
				return nil, err
			}
			if !policy.canFetch(final) {
				return nil, newTinyFishError(TinyFishRobots, 0)
			}
			if final != requestedURL {
				if _, duplicate := pages[final]; duplicate {
					continue
				}
			}
			content, err := normalizeTinyFishMarkdown(item.Text, int64(limits.MaxPageBytes))
			if err != nil {
				return nil, err
			}
			totalBytes += int64(len(content))
			if totalBytes > limits.MaxTotalBytes {
				return nil, newTinyFishError(TinyFishLimit, 0)
			}
			if _, duplicate := pages[final]; duplicate {
				continue
			}
			links := boundedTinyFishLinks(item.Links, origin)
			pages[final] = crawledWebsitePage{URL: final, Content: content, ContentSHA: sha256Bytes(content), Links: links, Freshness: "fresh"}
			for _, link := range links {
				if len(enqueued) >= discoveredCap {
					break
				}
				if _, exists := enqueued[link]; !exists {
					enqueued[link] = struct{}{}
					queue = append(queue, queuedURL{URL: link, Depth: depths[requestedURL] + 1})
				}
			}
		}
	}
	return pages, nil
}

func requireTinyFishFinal(finalURL *string, requested, origin string) (string, error) {
	if finalURL == nil {
		return requested, nil
	}
	canonical, err := canonicalWebsiteURL(*finalURL, origin, origin)
	if err != nil {
		return "", newTinyFishError(TinyFishPolicy, 0)
	}
	return canonical, nil
}

func boundedTinyFishLinks(raw []string, origin string) []string {
	links := make([]string, 0, len(raw))
	for _, value := range raw {
		canonical, err := canonicalWebsiteURL(value, origin, origin)
		if err == nil {
			links = append(links, canonical)
		}
	}
	slices.Sort(links)
	links = slices.Compact(links)
	return links[:min(len(links), maxLinksPerPage)]
}

func normalizeTinyFishMarkdown(raw string, maxBytes int64) ([]byte, error) {
	for _, character := range raw {
		if character <= 8 || character == 11 || character >= 14 && character <= 31 || character == 127 {
			return nil, newTinyFishError(TinyFishContent, 0)
		}
	}
	value := strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	value = norm.NFC.String(value)
	lines := strings.Split(value, "\n")
	for index := range lines {
		lines[index] = strings.TrimRight(lines[index], " \t\v\f")
	}
	rendered := []byte(strings.Join(lines, "\n"))
	if len(rendered) == 0 || rendered[len(rendered)-1] != '\n' {
		rendered = append(rendered, '\n')
	}
	if len(rendered) < 1 || int64(len(rendered)) > maxBytes {
		return nil, newTinyFishError(TinyFishLimit, 0)
	}
	return rendered, nil
}

func tinyFishWebsiteError(err error) error {
	var tinyFish *TinyFishError
	if errors.As(err, &tinyFish) {
		return websiteFailure(string(tinyFish.Code), tinyFish.Retryable)
	}
	var website *WebsiteFailure
	if errors.As(err, &website) {
		code := TinyFishUnspecified
		switch website.Code {
		case WebsiteLimit:
			code = TinyFishLimit
		case WebsiteRobots:
			code = TinyFishRobots
		case WebsiteStorage:
			code = TinyFishStorage
		case WebsiteContent:
			code = TinyFishContent
		case WebsiteDNS, WebsiteHTTP, WebsiteTLS:
			code = TinyFishConnection
		case WebsiteSSRF, WebsiteRedirect:
			code = TinyFishPolicy
		}
		return websiteFailure(string(code), website.Retryable)
	}
	return err
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}

func stableWebsiteDigest(pages map[string][32]byte) [32]byte {
	urls := make([]string, 0, len(pages))
	for rawURL := range pages {
		urls = append(urls, rawURL)
	}
	sort.Strings(urls)
	digest := sha256.New()
	for _, rawURL := range urls {
		_, _ = digest.Write([]byte(rawURL))
		_, _ = digest.Write([]byte{0})
		value := pages[rawURL]
		_, _ = digest.Write(value[:])
	}
	var result [32]byte
	copy(result[:], digest.Sum(nil))
	return result
}

func hexDigest(value [32]byte) string {
	return hex.EncodeToString(value[:])
}
