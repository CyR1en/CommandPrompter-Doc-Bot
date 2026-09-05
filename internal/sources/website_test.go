package sources

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/sourcefiles"
)

type websiteResolverFunc func(context.Context, string) ([]net.IPAddr, error)

func (function websiteResolverFunc) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return function(ctx, host)
}

type websiteTransportFunc func(context.Context, string, map[string]string, int64) (WebsiteHTTPResponse, error)

func (function websiteTransportFunc) Request(ctx context.Context, rawURL string, headers map[string]string, maxBytes int64) (WebsiteHTTPResponse, error) {
	return function(ctx, rawURL, headers, maxBytes)
}

func newSourceFileStore(t *testing.T) *sourcefiles.Store {
	t.Helper()
	base := t.TempDir()
	store, err := sourcefiles.NewStore(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(base, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o700)
			}
			return os.Chmod(path, 0o600)
		})
	})
	return store
}

func TestPinnedHTTPSPinsEveryRequestUsesSNIAndBypassesProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("https_proxy", "http://127.0.0.1:1")
	var (
		mu       sync.Mutex
		sniNames []string
		requests int
	)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests++
		mu.Unlock()
		response.Header().Set("Content-Type", "text/plain")
		_, _ = response.Write([]byte("bounded"))
	}))
	server.TLS = server.Config.TLSConfig
	if server.TLS == nil {
		server.TLS = new(tls.Config)
	}
	server.TLS.GetConfigForClient = func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
		mu.Lock()
		sniNames = append(sniNames, hello.ServerName)
		mu.Unlock()
		return nil, nil
	}
	server.StartTLS()
	defer server.Close()
	certificate := server.Certificate()
	if len(certificate.DNSNames) == 0 {
		t.Fatal("httptest certificate has no DNS identity")
	}
	host := certificate.DNSNames[0]
	port := server.Listener.Addr().(*net.TCPAddr).Port
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	lookups := 0
	dials := make([]string, 0, 2)
	transport, err := NewPinnedHTTPSTransport(PinnedHTTPSOptions{
		Timeout: 5 * time.Second,
		RootCAs: roots,
		Resolver: websiteResolverFunc(func(_ context.Context, requested string) ([]net.IPAddr, error) {
			lookups++
			if requested != host {
				t.Fatalf("resolved host = %q, want %q", requested, host)
			}
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		}),
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dials = append(dials, address)
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, requestErr := transport.Request(context.Background(), "https://"+net.JoinHostPort(host, strconv.Itoa(port))+"/guide", map[string]string{"Accept-Encoding": "identity"}, 1024)
		if requestErr != nil || result.Status != 200 || string(result.Body) != "bounded" {
			t.Fatalf("response=%+v err=%v", result, requestErr)
		}
	}
	if lookups != 2 || len(dials) != 2 || requests != 2 {
		t.Fatalf("lookups=%d dials=%v requests=%d", lookups, dials, requests)
	}
	for _, address := range dials {
		if address != net.JoinHostPort("93.184.216.34", strconv.Itoa(port)) {
			t.Fatalf("dial target = %q", address)
		}
	}
	if len(sniNames) != 2 || sniNames[0] != host || sniNames[1] != host {
		t.Fatalf("SNI names = %v", sniNames)
	}
}

func TestPinnedHTTPSRejectsMixedDNSBeforeDial(t *testing.T) {
	transport, err := NewPinnedHTTPSTransport(PinnedHTTPSOptions{
		Resolver: websiteResolverFunc(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}, {IP: net.ParseIP("127.0.0.1")}}, nil
		}),
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			t.Fatal("mixed DNS must be rejected before dialing")
			return nil, errors.New("unreachable")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.Request(context.Background(), "https://docs.example/", nil, 1024)
	var failure *WebsiteFailure
	if !errors.As(err, &failure) || failure.Code != WebsiteSSRF || failure.Retryable {
		t.Fatalf("failure = %#v, err=%v", failure, err)
	}
}

func TestDirectJSONIsSingleRequestStrictAndPythonDeterministic(t *testing.T) {
	store := newSourceFileStore(t)
	secret, _ := security.NewSecretValue("Bearer write-only")
	var calls []string
	transport := websiteTransportFunc(func(_ context.Context, rawURL string, headers map[string]string, maxBytes int64) (WebsiteHTTPResponse, error) {
		calls = append(calls, rawURL)
		if headers["Authorization"] != "Bearer write-only" || maxBytes != 1024 {
			t.Fatalf("headers=%v maxBytes=%d", headers, maxBytes)
		}
		return WebsiteHTTPResponse{Status: 200, Headers: map[string]string{"content-type": "application/vnd.docs+json"}, Body: []byte(`{"z":[2,1],"a":{"é":"<ok>"}}`)}, nil
	})
	adapter, err := NewWebsiteSourceAdapter(store, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	request := WebsiteRequest{
		SourceID:   testID(t, "10000000-0000-4000-8000-000000000001"),
		RevisionID: idPointerValue(testID(t, "20000000-0000-4000-8000-000000000002")),
		RemoteURL:  "https://docs.example/api",
		Credential: &WebsiteCredential{Header: "Authorization", Value: secret},
		Limits:     CrawlLimits{Concurrency: 1, RequestsPerSecond: 100, MaxPages: 1, MaxPageBytes: 1024, MaxTotalBytes: 2048, MaxDepth: 0},
		Mode:       DirectJSONAPI,
	}
	snapshot, err := adapter.Materialize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0] != request.RemoteURL || len(snapshot.Pages) != 1 || snapshot.FileCount != 2 {
		t.Fatalf("calls=%v snapshot=%+v", calls, snapshot)
	}
	root, err := store.ResolveArtifactKey(snapshot.ArtifactKey)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(snapshot.Pages[0].ContentPath)))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "\n\n", 2)
	const expected = "{\n  \"a\": {\n    \"\\u00e9\": \"<ok>\"\n  },\n  \"z\": [\n    2,\n    1\n  ]\n}\n"
	if len(parts) != 2 || parts[1] != expected {
		t.Fatalf("rendered JSON:\n%s", body)
	}
	for _, entry := range []string{"robots.txt", "sitemap.xml"} {
		if strings.Contains(strings.Join(calls, " "), entry) {
			t.Fatalf("direct mode requested %s", entry)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(root, "website-manifest.json"))
	if err != nil || bytes.Contains(manifest, []byte("write-only")) {
		t.Fatalf("manifest err=%v content=%s", err, manifest)
	}
}

func TestDirectJSONRejectsDuplicateDepthRedirectAndRenderedOverage(t *testing.T) {
	limits := CrawlLimits{Concurrency: 1, RequestsPerSecond: 100, MaxPages: 1, MaxPageBytes: 1024, MaxTotalBytes: 2048, MaxDepth: 0}
	tests := []struct {
		name   string
		status int
		body   []byte
		code   string
	}{
		{name: "duplicate", status: 200, body: []byte(`{"a":1,"a":2}`), code: WebsiteContent},
		{name: "depth", status: 200, body: []byte(strings.Repeat(`{"a":`, 40) + `1` + strings.Repeat(`}`, 40)), code: WebsiteLimit},
		{name: "redirect", status: 302, body: nil, code: WebsiteRedirect},
		{name: "rendered overage", status: 200, body: mustCompactJSON(t, map[string]any{"data": repeatValue("line"+strings.Repeat("x", 68), 13)}), code: WebsiteLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newSourceFileStore(t)
			adapter, _ := NewWebsiteSourceAdapter(store, websiteTransportFunc(func(context.Context, string, map[string]string, int64) (WebsiteHTTPResponse, error) {
				return WebsiteHTTPResponse{Status: test.status, Headers: map[string]string{"content-type": "application/json", "location": "https://docs.example/moved"}, Body: test.body}, nil
			}), nil)
			_, err := adapter.Materialize(context.Background(), WebsiteRequest{SourceID: testID(t, "10000000-0000-4000-8000-000000000001"), RevisionID: idPointerValue(testID(t, "20000000-0000-4000-8000-000000000002")), RemoteURL: "https://docs.example/api", Limits: limits, Mode: DirectJSONAPI})
			var failure *WebsiteFailure
			if !errors.As(err, &failure) || failure.Code != test.code {
				t.Fatalf("failure=%#v err=%v", failure, err)
			}
		})
	}
}

func TestBuiltinCrawlIsIncrementalBoundedAndSecretFree(t *testing.T) {
	store := newSourceFileStore(t)
	secretText := "Bearer website-sentinel"
	secret, _ := security.NewSecretValue(secretText)
	var mu sync.Mutex
	requests := make([]struct {
		URL     string
		Headers map[string]string
	}, 0)
	transport := websiteTransportFunc(func(_ context.Context, rawURL string, headers map[string]string, _ int64) (WebsiteHTTPResponse, error) {
		mu.Lock()
		requests = append(requests, struct {
			URL     string
			Headers map[string]string
		}{URL: rawURL, Headers: maps.Clone(headers)})
		mu.Unlock()
		switch rawURL {
		case "https://docs.example/robots.txt":
			return WebsiteHTTPResponse{Status: 200, Headers: map[string]string{"content-type": "text/plain"}, Body: []byte("User-agent: *\nAllow: /\nDisallow: /private\nSitemap: https://docs.example/sitemap.xml\n")}, nil
		case "https://docs.example/sitemap.xml":
			return WebsiteHTTPResponse{Status: 200, Headers: map[string]string{"content-type": "application/xml"}, Body: []byte("<urlset><url><loc>https://docs.example/guide</loc></url><url><loc>https://docs.example/private</loc></url></urlset>")}, nil
		case "https://docs.example/", "https://docs.example/guide":
			if headers["If-None-Match"] != "" {
				return WebsiteHTTPResponse{Status: 304}, nil
			}
			if rawURL == "https://docs.example/" {
				return WebsiteHTTPResponse{Status: 200, Headers: map[string]string{"content-type": "text/html; charset=utf-8", "etag": `"root"`}, Body: []byte("<html><head><title>Home</title></head><body>Welcome<a href='/guide'>Guide</a><a href='/private'>No</a></body></html>")}, nil
			}
			return WebsiteHTTPResponse{Status: 200, Headers: map[string]string{"content-type": "text/html", "etag": `"guide"`}, Body: []byte("<html><head><link rel='canonical' href='/guide'></head><body>Guide text</body></html>")}, nil
		default:
			return WebsiteHTTPResponse{}, errors.New("unexpected website URL: " + rawURL)
		}
	})
	adapter, _ := NewWebsiteSourceAdapter(store, transport, nil)
	firstRevision := testID(t, "20000000-0000-4000-8000-000000000002")
	request := WebsiteRequest{SourceID: testID(t, "10000000-0000-4000-8000-000000000001"), RevisionID: &firstRevision, RemoteURL: "https://docs.example/", Credential: &WebsiteCredential{Header: "Authorization", Value: secret}, Limits: CrawlLimits{Concurrency: 4, RequestsPerSecond: 100, MaxPages: 10, MaxPageBytes: 4096, MaxTotalBytes: 64 * 1024, MaxDepth: 2}, Mode: BuiltinCrawl}
	first, err := adapter.Materialize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	secondRevision := testID(t, "30000000-0000-4000-8000-000000000003")
	request.RevisionID, request.PreviousRevisionID = &secondRevision, &firstRevision
	second, err := adapter.Materialize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.NativeVersion != second.NativeVersion || first.Fingerprint != second.Fingerprint || len(first.Pages) != 2 || len(second.Pages) != 2 {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	for _, page := range second.Pages {
		if page.Freshness != "reused" || page.ReusedFromRevisionID == nil || *page.ReusedFromRevisionID != firstRevision {
			t.Fatalf("incremental page=%+v", page)
		}
	}
	for _, request := range requests {
		if request.Headers["Authorization"] != secretText {
			t.Fatalf("credential missing for %s: %v", request.URL, request.Headers)
		}
		if request.URL == "https://docs.example/private" {
			t.Fatal("robots-disallowed page was requested")
		}
	}
	for _, key := range []string{first.ArtifactKey, second.ArtifactKey} {
		root, resolveErr := store.ResolveArtifactKey(key)
		if resolveErr != nil {
			t.Fatal(resolveErr)
		}
		walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			content, readErr := os.ReadFile(path)
			if readErr == nil && strings.Contains(string(content), secretText) {
				t.Fatalf("credential persisted in %s", path)
			}
			return readErr
		})
		if walkErr != nil {
			t.Fatal(walkErr)
		}
	}
}

func idPointerValue(value jobs.UUID) *jobs.UUID { return &value }

func mustCompactJSON(t *testing.T, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func repeatValue(value string, count int) []string {
	result := make([]string, count)
	for index := range result {
		result[index] = value
	}
	return result
}
