package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/security"
)

type tinyFishTransportFunc func(context.Context, string, map[string]string, []byte, time.Duration) (TinyFishHTTPResponse, error)

func (function tinyFishTransportFunc) Post(ctx context.Context, rawURL string, headers map[string]string, body []byte, timeout time.Duration) (TinyFishHTTPResponse, error) {
	return function(ctx, rawURL, headers, body, timeout)
}

type tinyFishFetcherFunc func(context.Context, []string, *security.SecretValue) (TinyFishBatchResult, error)

func (function tinyFishFetcherFunc) Fetch(ctx context.Context, urls []string, secret *security.SecretValue) (TinyFishBatchResult, error) {
	return function(ctx, urls, secret)
}

func TestTinyFishRequestContractAndStableResponse(t *testing.T) {
	secret, _ := security.NewSecretValue("tf-sentinel-key-0001")
	var (
		gotURL     string
		gotHeaders map[string]string
		gotBody    []byte
		gotTimeout time.Duration
	)
	transport := tinyFishTransportFunc(func(_ context.Context, rawURL string, headers map[string]string, body []byte, timeout time.Duration) (TinyFishHTTPResponse, error) {
		gotURL, gotHeaders, gotBody, gotTimeout = rawURL, maps.Clone(headers), slices.Clone(body), timeout
		return TinyFishHTTPResponse{Status: 200, Body: []byte(`{"request_id":"volatile","results":[{"url":"https://docs.example/guide","format":"markdown","text":"Guide","links":[]},{"url":"https://docs.example/","final_url":"https://docs.example/final","format":"markdown","text":"# Home\n","links":["https://docs.example/guide"],"tokens_used":99}],"errors":[]}`)}, nil
	})
	client, err := NewTinyFishFetchClient(transport, 150*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	requested := []string{"https://docs.example/", "https://docs.example/guide"}
	result, err := client.Fetch(context.Background(), requested, secret)
	if err != nil {
		t.Fatal(err)
	}
	if gotURL != tinyFishServiceURL || gotTimeout != 150*time.Second {
		t.Fatalf("url=%q timeout=%s", gotURL, gotTimeout)
	}
	if len(gotHeaders) != 3 || gotHeaders["Accept"] != "application/json" || gotHeaders["Content-Type"] != "application/json" || gotHeaders["X-API-Key"] != secret.Reveal() {
		t.Fatalf("headers=%v", gotHeaders)
	}
	const expectedBody = `{"urls":["https://docs.example/","https://docs.example/guide"],"format":"markdown","links":true,"ttl":0,"per_url_timeout_ms":110000}`
	if string(gotBody) != expectedBody {
		t.Fatalf("body=%s", gotBody)
	}
	if len(result.Pages) != 2 || result.Pages[0].RequestedURL != requested[0] || result.Pages[1].RequestedURL != requested[1] || len(result.Errors) != 0 {
		t.Fatalf("result=%+v", result)
	}
}

func TestTinyFishClientFailsClosedWithSanitizedFiniteErrors(t *testing.T) {
	secret, _ := security.NewSecretValue("tf-secret-never-render")
	tests := []struct {
		name      string
		response  TinyFishHTTPResponse
		err       error
		code      TinyFishFailureCode
		retryable bool
	}{
		{name: "auth", response: TinyFishHTTPResponse{Status: 401, Body: []byte(secret.Reveal())}, code: TinyFishAuth},
		{name: "rate", response: TinyFishHTTPResponse{Status: 429}, code: TinyFishRateLimited, retryable: true},
		{name: "server", response: TinyFishHTTPResponse{Status: 503}, code: TinyFishServer, retryable: true},
		{name: "redirect", response: TinyFishHTTPResponse{Status: 302}, code: TinyFishRedirect},
		{name: "unknown exception", err: errors.New("vendor leaked " + secret.Reveal()), code: TinyFishUnspecified},
		{name: "invalid coverage", response: TinyFishHTTPResponse{Status: 200, Body: []byte(`{"results":[],"errors":[]}`)}, code: TinyFishInvalidResponse},
		{name: "duplicate coverage", response: TinyFishHTTPResponse{Status: 200, Body: []byte(`{"results":[{"url":"https://docs.example/","format":"markdown","text":"a"},{"url":"https://docs.example/","format":"markdown","text":"b"}],"errors":[]}`)}, code: TinyFishInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := NewTinyFishFetchClient(tinyFishTransportFunc(func(context.Context, string, map[string]string, []byte, time.Duration) (TinyFishHTTPResponse, error) {
				return test.response, test.err
			}), 150*time.Second)
			_, err := client.Fetch(context.Background(), []string{"https://docs.example/"}, secret)
			var failure *TinyFishError
			if !errors.As(err, &failure) || failure.Code != test.code || failure.Retryable != test.retryable {
				t.Fatalf("failure=%#v err=%v", failure, err)
			}
			if strings.Contains(err.Error(), secret.Reveal()) || strings.Contains(err.Error(), "vendor leaked") {
				t.Fatalf("unsafe error=%q", err)
			}
		})
	}
}

func TestTinyFishResponseDepthStatusAndBatchBounds(t *testing.T) {
	secret, _ := security.NewSecretValue("tf-valid-key")
	if client, err := NewTinyFishFetchClient(tinyFishTransportFunc(func(context.Context, string, map[string]string, []byte, time.Duration) (TinyFishHTTPResponse, error) {
		return TinyFishHTTPResponse{}, nil
	}), 149*time.Second); err == nil || client != nil {
		t.Fatal("sub-150-second timeout accepted")
	}
	client, _ := NewTinyFishFetchClient(tinyFishTransportFunc(func(context.Context, string, map[string]string, []byte, time.Duration) (TinyFishHTTPResponse, error) {
		deep := `"x"`
		for range 20 {
			deep = `{"a":` + deep + `}`
		}
		return TinyFishHTTPResponse{Status: 200, Body: []byte(deep)}, nil
	}), 150*time.Second)
	if _, err := client.Fetch(context.Background(), []string{"https://docs.example/"}, secret); tinyFishCode(err) != TinyFishInvalidResponse {
		t.Fatalf("deep response error=%v", err)
	}
	tooMany := make([]string, 11)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("https://docs.example/%d", index)
	}
	if _, err := client.Fetch(context.Background(), tooMany, secret); err == nil {
		t.Fatal("eleven URLs accepted")
	}
	statusPayload := []byte(`{"results":[],"errors":[{"url":"https://docs.example/","error":"vendor words","status":429}]}`)
	client, _ = NewTinyFishFetchClient(tinyFishTransportFunc(func(context.Context, string, map[string]string, []byte, time.Duration) (TinyFishHTTPResponse, error) {
		return TinyFishHTTPResponse{Status: 200, Body: statusPayload}, nil
	}), 150*time.Second)
	result, err := client.Fetch(context.Background(), []string{"https://docs.example/"}, secret)
	if err != nil || len(result.Errors) != 1 || result.Errors[0].Code != TinyFishRateLimited || result.Errors[0].HTTPStatus != 429 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestTinyFishWebsiteCrawlHonorsRobotsBFSNormalizationAndLayout(t *testing.T) {
	store := newSourceFileStore(t)
	apiKeyText := "tf-layout-secret"
	apiKey, _ := security.NewSecretValue(apiKeyText)
	var httpCalls []string
	httpTransport := websiteTransportFunc(func(_ context.Context, rawURL string, headers map[string]string, maxBytes int64) (WebsiteHTTPResponse, error) {
		httpCalls = append(httpCalls, rawURL)
		if rawURL != "https://docs.example/robots.txt" || headers["Accept"] != "text/plain" || maxBytes != maxRobotsBytes {
			t.Fatalf("robots request url=%s headers=%v max=%d", rawURL, headers, maxBytes)
		}
		return WebsiteHTTPResponse{Status: 200, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte("User-agent: *\nDisallow: /private\n")}, nil
	})
	var (
		mu      sync.Mutex
		batches [][]string
	)
	content := map[string]struct {
		text  string
		links []string
	}{
		"https://docs.example/":      {text: "Cafe\u0301  second\t \r\nthird", links: []string{"/guide", "/private", "https://elsewhere.example/no", "/guide"}},
		"https://docs.example/guide": {text: "Guide", links: nil},
	}
	fetcher := tinyFishFetcherFunc(func(_ context.Context, urls []string, secret *security.SecretValue) (TinyFishBatchResult, error) {
		if secret.Reveal() != apiKeyText {
			t.Fatal("wrong TinyFish credential")
		}
		mu.Lock()
		batches = append(batches, slices.Clone(urls))
		mu.Unlock()
		pages := make([]TinyFishPageResult, 0, len(urls))
		for _, rawURL := range urls {
			value, exists := content[rawURL]
			if !exists {
				t.Fatalf("unexpected TinyFish URL %q", rawURL)
			}
			pages = append(pages, TinyFishPageResult{RequestedURL: rawURL, Text: value.text, Links: value.links})
		}
		return TinyFishBatchResult{Pages: pages}, nil
	})
	adapter, _ := NewWebsiteSourceAdapter(store, httpTransport, fetcher)
	revisionID := testID(t, "20000000-0000-4000-8000-000000000002")
	request := WebsiteRequest{SourceID: testID(t, "10000000-0000-4000-8000-000000000001"), RevisionID: &revisionID, RemoteURL: "https://docs.example/", TinyFishCredential: apiKey, Limits: CrawlLimits{Concurrency: 8, RequestsPerSecond: 100, MaxPages: 5, MaxPageBytes: 1024, MaxTotalBytes: 4096, MaxDepth: 1}, Mode: TinyFishCrawl}
	snapshot, err := adapter.Materialize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(httpCalls) != 1 || len(snapshot.Pages) != 2 || snapshot.FileCount != 3 {
		t.Fatalf("http=%v snapshot=%+v", httpCalls, snapshot)
	}
	for _, batch := range batches {
		if len(batch) < 1 || len(batch) > 10 || !slices.IsSorted(batch) {
			t.Fatalf("batch=%v", batch)
		}
		for _, rawURL := range batch {
			if strings.Contains(rawURL, "private") || strings.Contains(rawURL, "elsewhere") {
				t.Fatalf("policy URL submitted: %s", rawURL)
			}
		}
	}
	root, err := store.ResolveArtifactKey(snapshot.ArtifactKey)
	if err != nil {
		t.Fatal(err)
	}
	pagePath := filepath.Join(root, filepath.FromSlash(snapshot.Pages[0].ContentPath))
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "Café  second\nthird\n") || strings.Contains(string(page), apiKeyText) {
		t.Fatalf("normalized page=%q", page)
	}
	manifest, _ := os.ReadFile(filepath.Join(root, "website-manifest.json"))
	if strings.Contains(string(manifest), apiKeyText) || strings.Contains(string(manifest), "request_id") {
		t.Fatalf("unsafe manifest=%s", manifest)
	}
}

func TestTinyFishWebsiteRejectsRobotsAndOffOriginFinal(t *testing.T) {
	secret, _ := security.NewSecretValue("tf-policy-key")
	for _, test := range []struct {
		name   string
		robots string
		final  *string
		code   string
		calls  int
	}{
		{name: "robots", robots: "User-agent: *\nDisallow: /\n", code: string(TinyFishRobots), calls: 0},
		{name: "off origin", robots: "User-agent: *\nAllow: /\n", final: stringPointerValue("https://elsewhere.example/moved"), code: string(TinyFishPolicy), calls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newSourceFileStore(t)
			calls := 0
			fetcher := tinyFishFetcherFunc(func(_ context.Context, urls []string, _ *security.SecretValue) (TinyFishBatchResult, error) {
				calls++
				return TinyFishBatchResult{Pages: []TinyFishPageResult{{RequestedURL: urls[0], FinalURL: test.final, Text: "content"}}}, nil
			})
			adapter, _ := NewWebsiteSourceAdapter(store, websiteTransportFunc(func(context.Context, string, map[string]string, int64) (WebsiteHTTPResponse, error) {
				return WebsiteHTTPResponse{Status: 200, Body: []byte(test.robots)}, nil
			}), fetcher)
			revisionID := testID(t, "20000000-0000-4000-8000-000000000002")
			_, err := adapter.Materialize(context.Background(), WebsiteRequest{SourceID: testID(t, "10000000-0000-4000-8000-000000000001"), RevisionID: &revisionID, RemoteURL: "https://docs.example/", TinyFishCredential: secret, Limits: CrawlLimits{Concurrency: 1, RequestsPerSecond: 100, MaxPages: 1, MaxPageBytes: 1024, MaxTotalBytes: 2048, MaxDepth: 0}, Mode: TinyFishCrawl})
			var failure *WebsiteFailure
			if !errors.As(err, &failure) || failure.Code != test.code || calls != test.calls {
				t.Fatalf("failure=%#v calls=%d err=%v", failure, calls, err)
			}
		})
	}
}

func TestTinyFishWebsiteFollowsSafeRobotsRedirects(t *testing.T) {
	secret, _ := security.NewSecretValue("tf-redirect-key")
	for _, test := range []struct {
		name      string
		location  string
		wantCode  TinyFishFailureCode
		wantFetch int
	}{
		{name: "same origin", location: "https://docs.example/guide/robots.txt", wantFetch: 1},
		{name: "off origin", location: "https://elsewhere.example/robots.txt", wantCode: TinyFishPolicy},
	} {
		t.Run(test.name, func(t *testing.T) {
			requests := make([]string, 0, 2)
			transport := websiteTransportFunc(func(_ context.Context, rawURL string, headers map[string]string, maxBytes int64) (WebsiteHTTPResponse, error) {
				requests = append(requests, rawURL)
				if headers["Accept"] != "text/plain" || headers["Accept-Encoding"] != "identity" || headers["User-Agent"] != websiteUserAgent || maxBytes != maxRobotsBytes {
					t.Fatalf("robots headers=%v max=%d", headers, maxBytes)
				}
				switch rawURL {
				case "https://docs.example/robots.txt":
					return WebsiteHTTPResponse{Status: 307, Headers: map[string]string{"location": test.location}}, nil
				case "https://docs.example/guide/robots.txt":
					return WebsiteHTTPResponse{Status: 200, Body: []byte("User-agent: *\nAllow: /\n")}, nil
				default:
					t.Fatalf("unexpected website request %q", rawURL)
					return WebsiteHTTPResponse{}, nil
				}
			})
			fetchCalls := 0
			fetcher := tinyFishFetcherFunc(func(_ context.Context, urls []string, _ *security.SecretValue) (TinyFishBatchResult, error) {
				fetchCalls++
				return TinyFishBatchResult{Pages: []TinyFishPageResult{{RequestedURL: urls[0], Text: "content"}}}, nil
			})
			adapter, _ := NewWebsiteSourceAdapter(newSourceFileStore(t), transport, fetcher)
			request := WebsiteRequest{
				SourceID:  testID(t, "10000000-0000-4000-8000-000000000001"),
				RemoteURL: "https://docs.example/guide", TinyFishCredential: secret,
				Limits: CrawlLimits{Concurrency: 1, RequestsPerSecond: 100, MaxPages: 1, MaxPageBytes: 1024, MaxTotalBytes: 2048, MaxDepth: 0},
				Mode:   TinyFishCrawl,
			}
			_, err := adapter.Validate(context.Background(), request)
			var failure *WebsiteFailure
			if test.wantCode == "" {
				if err != nil || !slices.Equal(requests, []string{"https://docs.example/robots.txt", test.location}) || fetchCalls != test.wantFetch {
					t.Fatalf("requests=%v fetch_calls=%d err=%v", requests, fetchCalls, err)
				}
			} else if !errors.As(err, &failure) || failure.Code != string(test.wantCode) || len(requests) != 1 || fetchCalls != 0 {
				t.Fatalf("failure=%#v requests=%v fetch_calls=%d err=%v", failure, requests, fetchCalls, err)
			}
		})
	}
}

func tinyFishCode(err error) TinyFishFailureCode {
	var failure *TinyFishError
	if errors.As(err, &failure) {
		return failure.Code
	}
	return ""
}

func stringPointerValue(value string) *string { return &value }

func successTinyFishPayload(t *testing.T, urls []string) []byte {
	t.Helper()
	results := make([]map[string]any, 0, len(urls))
	for _, rawURL := range urls {
		results = append(results, map[string]any{"url": rawURL, "format": "markdown", "text": "content", "links": []string{}})
	}
	body, err := json.Marshal(map[string]any{"results": results, "errors": []any{}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}
