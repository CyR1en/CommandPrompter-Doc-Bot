package safenet

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeBaseURL(t *testing.T) {
	public := Policy{}
	tests := map[string]string{
		"https://provider.example":         "https://provider.example/v1",
		"https://provider.example/":        "https://provider.example/v1",
		"https://provider.example/v1":      "https://provider.example/v1",
		"https://provider.example/openai/": "https://provider.example/openai",
		"https://provider.example:443/api": "https://provider.example/api",
	}
	for raw, expected := range tests {
		parsed, err := NormalizeBaseURL(raw, public)
		if err != nil || parsed.String() != expected {
			t.Fatalf("NormalizeBaseURL(%q)=%v, %v", raw, parsed, err)
		}
	}
	for _, raw := range []string{
		"ftp://provider.example", "https://user:sentinel@provider.example",
		"https://provider.example?target=elsewhere", "https://provider.example#fragment",
		"https://provider.example/a/../b", "https://provider.example/%2e%2e/metadata",
		"https://provider.example/%252e%252e/metadata", "https://provider.example/a%2fb",
		`https://provider.example/a\b`,
	} {
		_, err := NormalizeBaseURL(raw, public)
		if err == nil || strings.Contains(err.Error(), "sentinel") {
			t.Fatalf("unsafe URL %q: %v", raw, err)
		}
	}
	if _, err := NormalizeBaseURL("http://127.0.0.1", Policy{AllowPrivateAddresses: true}); errorCode(err) != PolicyDenied {
		t.Fatalf("plain HTTP error=%v", err)
	}
	parsed, err := NormalizeBaseURL("http://127.0.0.1", privateHTTPPolicy())
	if err != nil || parsed.String() != "http://127.0.0.1/v1" {
		t.Fatalf("private HTTP=%v, %v", parsed, err)
	}
}

func TestNormalizeRelativePath(t *testing.T) {
	if path, err := NormalizeRelativePath("chat/completions"); err != nil || path != "chat/completions" {
		t.Fatalf("path=%q err=%v", path, err)
	}
	for _, path := range []string{
		"/models", "//other.example/models", "https://other.example/models", "../models",
		"%2e%2e/models", "models?limit=1", "models#fragment", "models//nested",
	} {
		if _, err := NormalizeRelativePath(path); errorCode(err) != InvalidURL {
			t.Fatalf("path %q error=%v", path, err)
		}
	}
}

func TestAddressPolicyMatchesOracle(t *testing.T) {
	tests := []struct {
		address         string
		public, private bool
	}{
		{"8.8.8.8", true, true}, {"127.0.0.1", false, true},
		{"10.0.0.1", false, true}, {"172.16.0.1", false, true},
		{"192.168.1.1", false, true}, {"fd12::1", false, true},
		{"0.0.0.0", false, false}, {"224.0.0.1", false, false},
		{"192.0.2.1", false, false}, {"198.18.0.1", false, false},
		{"100.64.0.1", false, false}, {"169.254.169.254", false, false},
		{"168.63.129.16", false, false}, {"fd00:ec2::254", false, false},
		{"fe80::1", false, false},
	}
	for _, test := range tests {
		if got := AddressAllowed(test.address, Policy{}, false); got != test.public {
			t.Errorf("public %s=%t", test.address, got)
		}
		if got := AddressAllowed(test.address, Policy{AllowPrivateAddresses: true}, false); got != test.private {
			t.Errorf("private %s=%t", test.address, got)
		}
	}
}

func TestParseBoundedJSONAndCatalog(t *testing.T) {
	for _, body := range [][]byte{[]byte(`{"model":"allowed","model":"forged"}`), []byte(`{"value":NaN}`)} {
		if _, err := ParseBoundedJSON(body); errorCode(err) != InvalidJSON {
			t.Fatalf("body=%s err=%v", body, err)
		}
	}
	deep := strings.Repeat("[", 17) + `"bottom"` + strings.Repeat("]", 17)
	if _, err := ParseBoundedJSON([]byte(deep)); errorCode(err) != InvalidJSON {
		t.Fatalf("deep JSON err=%v", err)
	}
	payload, err := ParseBoundedJSON([]byte(`{"unrelated":{"raw":true},"data":[{"id":"model-a","unknown":{"future":1}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	models, err := ValidateModelCatalog(payload)
	if err != nil || len(models) != 1 || models[0]["id"] != "model-a" {
		t.Fatalf("models=%v err=%v", models, err)
	}
}

func TestClientLiveSafetyBoundary(t *testing.T) {
	secret := "provider-secret-sentinel"
	var requests atomic.Int32
	var receivedHost, receivedBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		receivedHost = request.Host
		body, _ := io.ReadAll(request.Body)
		receivedBody = string(body)
		switch request.URL.Path {
		case "/v1/redirect":
			writer.Header().Set("Location", "/v1/target")
			writer.WriteHeader(http.StatusFound)
		case "/v1/failure":
			writer.Header().Set("Content-Type", "text/plain")
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = writer.Write([]byte("upstream leaked provider-secret-sentinel"))
		case "/v1/large":
			writer.Header().Set("Content-Length", "2048")
			_, _ = writer.Write(make([]byte, 2048))
		default:
			writer.Header().Set("Content-Type", "text/event-stream")
			writer.Header().Set("X-Request-ID", "safe-id")
			_, _ = writer.Write([]byte("data: {\"value\":\"provider-secret-sentinel\"}\n\ndata: [DONE]\n\n"))
		}
	}))
	defer server.Close()
	_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
	resolverCalls := 0
	resolver := func(_ context.Context, host string, resolvedPort int) ([]string, error) {
		resolverCalls++
		if host != "provider.example" || fmt.Sprint(resolvedPort) != rawPort {
			t.Fatalf("resolution host=%s port=%d", host, resolvedPort)
		}
		return []string{"127.0.0.1"}, nil
	}
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	client, err := NewClient("http://provider.example:"+rawPort+"/v1", privateHTTPPolicy(), ClientOptions{Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	response, err := client.Exchange(context.Background(), http.MethodPost, "chat/completions", map[string]string{
		"Authorization": "Bearer " + secret, "Accept": "text/event-stream", "Content-Type": "application/json",
	}, []byte(`{"model":"test"}`), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if response.Status != 200 || response.Headers["content-type"] != "text/event-stream" ||
		strings.Contains(string(response.Body), secret) || !strings.Contains(string(response.Body), "[REDACTED]") {
		t.Fatalf("unsafe response=%+v", response)
	}
	if receivedHost != "provider.example:"+rawPort || receivedBody != `{"model":"test"}` || resolverCalls != 1 {
		t.Fatalf("host=%q body=%q resolverCalls=%d", receivedHost, receivedBody, resolverCalls)
	}
	if _, err := client.Exchange(context.Background(), http.MethodGet, "redirect", nil, nil, 1024); errorCode(err) != Redirect {
		t.Fatalf("redirect error=%v", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("redirect was followed, requests=%d", requests.Load())
	}
	failure, err := client.Exchange(context.Background(), http.MethodGet, "failure", map[string]string{
		"Authorization": "Bearer " + secret,
	}, nil, 1024)
	if err != nil || failure.Status != http.StatusBadGateway || strings.Contains(string(failure.Body), secret) ||
		!strings.Contains(string(failure.Body), "Provider request failed") || failure.Headers["content-type"] != "application/json" {
		t.Fatalf("unsafe failure=%+v err=%v", failure, err)
	}
	if _, err := client.Exchange(context.Background(), http.MethodGet, "large", nil, nil, 1024); errorCode(err) != ResponseTooLarge {
		t.Fatalf("large error=%v", err)
	}
}

func TestClientRejectsMixedAndReboundDNSBeforeDial(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte(`{}`))
	}))
	defer server.Close()
	_, rawPort, _ := net.SplitHostPort(server.Listener.Addr().String())
	answers := [][]string{{"127.0.0.1"}, {"169.254.169.254"}}
	resolver := func(context.Context, string, int) ([]string, error) {
		answer := answers[0]
		answers = answers[1:]
		return answer, nil
	}
	client, err := NewClient("http://provider.example:"+rawPort, privateHTTPPolicy(), ClientOptions{Resolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.GetJSON(context.Background(), "models"); err != nil {
		t.Fatal(err)
	}
	client.CloseIdleConnections()
	if _, _, err := client.GetJSON(context.Background(), "models"); errorCode(err) != PolicyDenied {
		t.Fatalf("rebound error=%v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests=%d", requests.Load())
	}

	mixed, err := NewClient("http://provider.example:"+rawPort, privateHTTPPolicy(), ClientOptions{Resolver: func(context.Context, string, int) ([]string, error) {
		return []string{"127.0.0.1", "169.254.169.254"}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := mixed.GetJSON(context.Background(), "models"); errorCode(err) != PolicyDenied {
		t.Fatalf("mixed error=%v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("mixed DNS dialed server")
	}
}

func TestClientRejectsUnsafeHeadersTLSAndTimeout(t *testing.T) {
	for _, name := range []string{"Host", "Connection", "Content-Length", "Proxy-Authorization", "Transfer-Encoding"} {
		_, err := NewClient("https://provider.example", Policy{}, ClientOptions{Headers: map[string]string{name: "sentinel"}})
		if errorCode(err) != PolicyDenied || strings.Contains(fmt.Sprint(err), "sentinel") {
			t.Fatalf("header %s err=%v", name, err)
		}
	}
	_, err := NewClient("https://provider.example", Policy{}, ClientOptions{TLSConfig: &tls.Config{InsecureSkipVerify: true}})
	if err == nil {
		t.Fatal("insecure TLS accepted")
	}
	for _, timeout := range []time.Duration{MinModelTimeout, MaxModelTimeout} {
		if _, err = NewClient("https://provider.example", Policy{}, ClientOptions{Timeout: timeout}); err != nil {
			t.Fatalf("boundary timeout %s rejected: %v", timeout, err)
		}
	}
	for _, timeout := range []time.Duration{MinModelTimeout - time.Millisecond, MaxModelTimeout + time.Millisecond} {
		if _, err = NewClient("https://provider.example", Policy{}, ClientOptions{Timeout: timeout}); err == nil {
			t.Fatalf("out-of-bound timeout %s accepted", timeout)
		}
	}
}

func TestClientEnforcesTheExactMinimumModelDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	client, err := NewClient(server.URL, privateHTTPPolicy(), ClientOptions{Timeout: MinModelTimeout})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, _, err = client.GetJSON(context.Background(), "wait")
	elapsed := time.Since(started)
	if errorCode(err) != Timeout || elapsed < 750*time.Millisecond || elapsed > 3*time.Second {
		t.Fatalf("deadline error=%v elapsed=%s", err, elapsed)
	}
}

func TestSanitizationRejectsKeyCollision(t *testing.T) {
	_, err := sanitizeResponse([]byte(`{"secret":"one","[REDACTED]":"two"}`), "application/json", "", "secret")
	if errorCode(err) != InvalidJSON {
		t.Fatalf("collision error=%v", err)
	}
}

func TestSafeErrorsHaveStableShape(t *testing.T) {
	err := &RequestError{Code: HTTPStatus, Retryable: true, HTTPStatus: 503}
	if err.Error() != "Provider returned an unsuccessful status." || !err.Retryable || err.HTTPStatus != 503 {
		t.Fatal(err)
	}
	var requestErrorValue *RequestError
	if !errors.As(err, &requestErrorValue) || !reflect.DeepEqual(err, requestErrorValue) {
		t.Fatal("RequestError does not unwrap through errors.As")
	}
}

func privateHTTPPolicy() Policy {
	return Policy{AllowPrivateAddresses: true, AllowPlainHTTP: true}
}

func errorCode(err error) FailureCode {
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		return requestErr.Code
	}
	return ""
}
