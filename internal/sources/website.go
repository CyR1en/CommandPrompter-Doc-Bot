package sources

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/sourcefiles"
)

const (
	websiteUserAgent      = "ref0-WebsiteSource/1.0"
	maxWebsiteRedirects   = 5
	maxRobotsBytes        = 512 * 1024
	maxSitemapBytes       = 5 * 1024 * 1024
	maxLinksPerPage       = 64
	discoveredMultiplier  = 4
	discoveredCeiling     = 2_000
	maxWebsiteRequestBody = 16 * 1024 * 1024
	maxJSONDepth          = 32
)

const (
	WebsiteDNS      = "dns"
	WebsiteSSRF     = "ssrf"
	WebsiteTLS      = "tls"
	WebsiteHTTP     = "http"
	WebsiteRedirect = "redirect"
	WebsiteRobots   = "robots"
	WebsiteContent  = "content"
	WebsiteLimit    = "limit"
	WebsiteStorage  = "storage"
)

func websiteFailure(code string, retryable bool) *WebsiteFailure {
	return &WebsiteFailure{Code: code, Retryable: retryable}
}

type WebsiteHTTPResponse struct {
	Status  int
	Headers map[string]string
	Body    []byte
}

type WebsiteHTTPTransport interface {
	Request(context.Context, string, map[string]string, int64) (WebsiteHTTPResponse, error)
}

type WebsiteResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type PinnedHTTPSOptions struct {
	Timeout     time.Duration
	Resolver    WebsiteResolver
	DialContext func(context.Context, string, string) (net.Conn, error)
	RootCAs     *x509.CertPool
}

type PinnedHTTPSTransport struct {
	timeout  time.Duration
	resolver WebsiteResolver
	dial     func(context.Context, string, string) (net.Conn, error)
	roots    *x509.CertPool
}

func NewPinnedHTTPSTransport(options PinnedHTTPSOptions) (*PinnedHTTPSTransport, error) {
	timeout := options.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	if timeout < time.Second || timeout > 120*time.Second {
		return nil, errors.New("website timeout is invalid")
	}
	resolver := options.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dial := options.DialContext
	if dial == nil {
		dialer := &net.Dialer{Timeout: timeout, KeepAlive: -1}
		dial = dialer.DialContext
	}
	return &PinnedHTTPSTransport{timeout: timeout, resolver: resolver, dial: dial, roots: options.RootCAs}, nil
}

func (transport *PinnedHTTPSTransport) Request(ctx context.Context, rawURL string, headers map[string]string, maxBytes int64) (WebsiteHTTPResponse, error) {
	if transport == nil || maxBytes <= 0 || maxBytes > maxWebsiteRequestBody {
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteHTTP, false)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteHTTP, false)
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteHTTP, false)
	}
	for name, value := range headers {
		if !validHTTPHeader(name, value) {
			return WebsiteHTTPResponse{}, websiteFailure(WebsiteHTTP, false)
		}
		request.Header.Set(name, value)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: transport.roots}
	httpTransport := &http.Transport{
		Proxy:                  nil,
		DisableCompression:     true,
		DisableKeepAlives:      true,
		ForceAttemptHTTP2:      false,
		TLSClientConfig:        tlsConfig,
		TLSHandshakeTimeout:    transport.timeout,
		ResponseHeaderTimeout:  transport.timeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: 64 * 1024,
	}
	httpTransport.DialContext = func(dialCtx context.Context, network, _ string) (net.Conn, error) {
		addresses, lookupErr := transport.resolver.LookupIPAddr(dialCtx, parsed.Hostname())
		if lookupErr != nil || len(addresses) == 0 {
			return nil, websiteFailure(WebsiteDNS, true)
		}
		unique := make([]netip.Addr, 0, len(addresses))
		seen := make(map[netip.Addr]struct{}, len(addresses))
		for _, answer := range addresses {
			address, parseErr := netip.ParseAddr(answer.IP.String())
			if parseErr != nil || !publicWebsiteAddress(address) {
				return nil, websiteFailure(WebsiteSSRF, false)
			}
			address = address.Unmap()
			if _, exists := seen[address]; !exists {
				seen[address] = struct{}{}
				unique = append(unique, address)
			}
		}
		if len(unique) == 0 {
			return nil, websiteFailure(WebsiteDNS, true)
		}
		return transport.dial(dialCtx, network, net.JoinHostPort(unique[0].String(), port))
	}
	client := &http.Client{
		Transport: httpTransport,
		Timeout:   transport.timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		var failure *WebsiteFailure
		if errors.As(err, &failure) {
			return WebsiteHTTPResponse{}, failure
		}
		if tlsFailure(err) {
			return WebsiteHTTPResponse{}, websiteFailure(WebsiteTLS, false)
		}
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteHTTP, true)
	}
	defer response.Body.Close()
	if encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding"))); encoding != "" && encoding != "identity" {
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteContent, false)
	}
	if response.ContentLength > maxBytes {
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteLimit, false)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteHTTP, true)
	}
	if int64(len(body)) > maxBytes {
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteLimit, false)
	}
	values := make(map[string]string, len(response.Header))
	for name, headers := range response.Header {
		if len(headers) > 0 {
			values[strings.ToLower(name)] = headers[len(headers)-1]
		}
	}
	return WebsiteHTTPResponse{Status: response.StatusCode, Headers: values, Body: body}, nil
}

func validHTTPHeader(name, value string) bool {
	if name == "" || strings.ContainsAny(value, "\r\n") {
		return false
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			return false
		}
	}
	return true
}

func tlsFailure(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var certificateInvalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var record tls.RecordHeaderError
	return errors.As(err, &unknownAuthority) || errors.As(err, &certificateInvalid) || errors.As(err, &hostname) || errors.As(err, &record) || strings.Contains(strings.ToLower(err.Error()), "tls")
}

var websiteNonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func publicWebsiteAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range websiteNonPublicPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

type WebsiteSourceAdapter struct {
	artifacts *sourcefiles.Store
	http      WebsiteHTTPTransport
	tinyfish  TinyFishBatchFetcher
}

func NewWebsiteSourceAdapter(artifacts *sourcefiles.Store, transport WebsiteHTTPTransport, tinyfish TinyFishBatchFetcher) (*WebsiteSourceAdapter, error) {
	if artifacts == nil || transport == nil {
		return nil, errors.New("website source dependencies are incomplete")
	}
	return &WebsiteSourceAdapter{artifacts: artifacts, http: transport, tinyfish: tinyfish}, nil
}

func (adapter *WebsiteSourceAdapter) Validate(ctx context.Context, request WebsiteRequest) (string, error) {
	if err := validateWebsiteRequest(request, false); err != nil {
		return "", err
	}
	switch request.Mode {
	case DirectJSONAPI:
		return adapter.validateJSON(ctx, request)
	case BuiltinCrawl:
		return adapter.validateCrawl(ctx, request)
	case TinyFishCrawl:
		return adapter.validateTinyFish(ctx, request)
	default:
		return "", errors.New("website acquisition mode is invalid")
	}
}

func (adapter *WebsiteSourceAdapter) Materialize(ctx context.Context, request WebsiteRequest) (WebsiteSnapshot, error) {
	if err := validateWebsiteRequest(request, true); err != nil {
		return WebsiteSnapshot{}, err
	}
	switch request.Mode {
	case DirectJSONAPI:
		return adapter.materializeJSON(ctx, request)
	case BuiltinCrawl:
		return adapter.materializeCrawl(ctx, request)
	case TinyFishCrawl:
		return adapter.materializeTinyFish(ctx, request)
	default:
		return WebsiteSnapshot{}, errors.New("website acquisition mode is invalid")
	}
}

func validateWebsiteRequest(request WebsiteRequest, materialize bool) error {
	if _, err := ParseWebsiteRemote(request.RemoteURL); err != nil {
		return err
	}
	if err := request.Limits.validate(); err != nil {
		return err
	}
	if materialize && request.RevisionID == nil {
		return errors.New("website revision is required")
	}
	if request.Credential != nil {
		if request.Credential.Value == nil || credentialHeader(&request.Credential.Header) != nil || strings.ContainsAny(request.Credential.Value.Reveal(), "\r\n") {
			return errors.New("website credential header is forbidden")
		}
	}
	if request.Mode == TinyFishCrawl {
		if request.Credential != nil || request.TinyFishCredential == nil {
			return errors.New("tinyfish credentials are invalid")
		}
	} else if request.TinyFishCredential != nil {
		return errors.New("tinyfish credential requires tinyfish acquisition")
	}
	if request.Mode == DirectJSONAPI && (request.Limits.MaxPages != 1 || request.Limits.MaxDepth != 0) {
		return errors.New("direct JSON API acquisition requires max_pages 1 and max_depth 0")
	}
	return nil
}

type crawlBudget struct {
	transport WebsiteHTTPTransport
	limits    CrawlLimits
	semaphore chan struct{}
	mu        sync.Mutex
	next      time.Time
	total     int64
}

func newCrawlBudget(transport WebsiteHTTPTransport, limits CrawlLimits) *crawlBudget {
	return &crawlBudget{transport: transport, limits: limits, semaphore: make(chan struct{}, limits.Concurrency)}
}

func (budget *crawlBudget) request(ctx context.Context, rawURL string, headers map[string]string, maxBytes int64) (WebsiteHTTPResponse, error) {
	select {
	case budget.semaphore <- struct{}{}:
	case <-ctx.Done():
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteHTTP, true)
	}
	defer func() { <-budget.semaphore }()
	if err := budget.wait(ctx); err != nil {
		return WebsiteHTTPResponse{}, err
	}
	response, err := budget.transport.Request(ctx, rawURL, headers, maxBytes)
	if err != nil {
		return WebsiteHTTPResponse{}, err
	}
	budget.mu.Lock()
	budget.total += int64(len(response.Body))
	exceeded := budget.total > budget.limits.MaxTotalBytes
	budget.mu.Unlock()
	if exceeded {
		return WebsiteHTTPResponse{}, websiteFailure(WebsiteLimit, false)
	}
	return response, nil
}

func (budget *crawlBudget) wait(ctx context.Context) error {
	budget.mu.Lock()
	now := time.Now()
	wait := time.Duration(0)
	if now.Before(budget.next) {
		wait = budget.next.Sub(now)
	}
	start := now.Add(wait)
	budget.next = start.Add(time.Second / time.Duration(budget.limits.RequestsPerSecond))
	budget.mu.Unlock()
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return websiteFailure(WebsiteHTTP, true)
	}
}

func websiteHeaders(credential *WebsiteCredential, accept string, conditional map[string]string) map[string]string {
	headers := map[string]string{
		"Accept":          accept,
		"Accept-Encoding": "identity",
		"User-Agent":      websiteUserAgent,
	}
	for name, value := range conditional {
		headers[name] = value
	}
	if credential != nil {
		headers[credential.Header] = credential.Value.Reveal()
	}
	return headers
}

func websiteOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" || parsed.User != nil {
		return "", errors.New("website URL must be HTTPS")
	}
	host := strings.ToLower(parsed.Hostname())
	rendered := host
	if strings.Contains(host, ":") {
		rendered = "[" + host + "]"
	}
	port := parsed.Port()
	if port != "" && port != "443" {
		rendered += ":" + port
	}
	return "https://" + rendered, nil
}

func canonicalWebsiteURL(raw, base, expectedOrigin string) (string, error) {
	baseURL, err := url.Parse(base)
	if err != nil {
		return "", errors.New("website base URL is invalid")
	}
	reference, err := url.Parse(raw)
	if err != nil || reference.User != nil || strings.Contains(raw, "\\") || hasControl(raw) {
		return "", errors.New("website URL leaves the configured origin")
	}
	joined := baseURL.ResolveReference(reference)
	origin, err := websiteOrigin(joined.String())
	if err != nil || origin != expectedOrigin || joined.User != nil {
		return "", errors.New("website URL leaves the configured origin")
	}
	cleaned := path.Clean(joined.Path)
	if cleaned == "." {
		cleaned = "/"
	}
	if strings.HasSuffix(joined.Path, "/") && cleaned != "/" {
		cleaned += "/"
	}
	joined.Scheme = "https"
	joined.Host = strings.TrimPrefix(expectedOrigin, "https://")
	joined.Path = cleaned
	joined.RawPath = ""
	joined.Fragment = ""
	if joined.RawQuery != "" {
		values, err := url.ParseQuery(joined.RawQuery)
		if err != nil {
			return "", errors.New("website query is invalid")
		}
		for key := range values {
			sort.Strings(values[key])
		}
		joined.RawQuery = values.Encode()
	}
	return joined.String(), nil
}

func contentType(response WebsiteHTTPResponse) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(response.Headers["content-type"], ";", 2)[0]))
}

func responseCharset(response WebsiteHTTPResponse) string {
	parts := strings.Split(response.Headers["content-type"], ";")
	for _, part := range parts[1:] {
		name, value, found := strings.Cut(strings.TrimSpace(part), "=")
		if found && strings.EqualFold(name, "charset") {
			return strings.Trim(strings.TrimSpace(value), "\"'")
		}
	}
	return "utf-8"
}

func isHTMLType(value string) bool {
	return value == "text/html" || value == "application/xhtml+xml"
}

func isXMLType(value string) bool {
	return value == "application/xml" || value == "text/xml" || value == "application/rss+xml" || value == "application/octet-stream"
}

func isJSONType(value string) bool {
	if value == "application/json" {
		return true
	}
	if !strings.HasPrefix(value, "application/") || !strings.HasSuffix(value, "+json") {
		return false
	}
	subtype := strings.TrimSuffix(strings.TrimPrefix(value, "application/"), "+json")
	return subtype != "" && !strings.Contains(subtype, "/")
}

type robotRule struct {
	path  string
	allow bool
}

type robotsPolicy struct {
	rules []robotRule
}

type robotsGroup struct {
	agents []string
	rules  []robotRule
}

func parseRobots(body []byte) (robotsPolicy, []string, error) {
	if !utf8.Valid(body) {
		return robotsPolicy{}, nil, websiteFailure(WebsiteRobots, false)
	}
	groups := make([]robotsGroup, 0)
	current := robotsGroup{}
	sitemaps := make([]string, 0)
	flush := func() {
		if len(current.agents) > 0 {
			groups = append(groups, current)
		}
		current = robotsGroup{}
	}
	for _, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(strings.SplitN(raw, "#", 2)[0])
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		name, value = strings.ToLower(strings.TrimSpace(name)), strings.TrimSpace(value)
		switch name {
		case "user-agent":
			if len(current.rules) > 0 {
				flush()
			}
			if value != "" {
				current.agents = append(current.agents, strings.ToLower(value))
			}
		case "allow", "disallow":
			if len(current.agents) > 0 && value != "" {
				current.rules = append(current.rules, robotRule{path: value, allow: name == "allow"})
			}
		case "sitemap":
			if value != "" {
				sitemaps = append(sitemaps, value)
			}
		}
	}
	flush()
	agent := strings.ToLower(websiteUserAgent)
	selected := make([]robotRule, 0)
	fallback := make([]robotRule, 0)
	for _, group := range groups {
		exact := false
		star := false
		for _, value := range group.agents {
			exact = exact || (value != "*" && strings.Contains(agent, value))
			star = star || value == "*"
		}
		if exact {
			selected = append(selected, group.rules...)
		} else if star {
			fallback = append(fallback, group.rules...)
		}
	}
	if len(selected) == 0 {
		selected = fallback
	}
	return robotsPolicy{rules: selected}, sitemaps, nil
}

func (policy robotsPolicy) canFetch(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	target := parsed.EscapedPath()
	if target == "" {
		target = "/"
	}
	if parsed.RawQuery != "" {
		target += "?" + parsed.RawQuery
	}
	best := -1
	allowed := true
	for _, rule := range policy.rules {
		pattern := rule.path
		if index := strings.IndexByte(pattern, '*'); index >= 0 {
			pattern = pattern[:index]
		}
		pattern = strings.TrimSuffix(pattern, "$")
		if strings.HasPrefix(target, pattern) && (len(pattern) > best || len(pattern) == best && rule.allow) {
			best, allowed = len(pattern), rule.allow
		}
	}
	return allowed
}

func (adapter *WebsiteSourceAdapter) get(ctx context.Context, rawURL, origin string, credential *WebsiteCredential, maxBytes int64, conditional map[string]string, budget *crawlBudget, policy *robotsPolicy) (WebsiteHTTPResponse, string, error) {
	return adapter.getWithHeaders(ctx, rawURL, origin, websiteHeaders(credential, "text/html,application/xhtml+xml,application/xml,text/xml;q=0.8,*/*;q=0.1", conditional), maxBytes, budget, policy)
}

func (adapter *WebsiteSourceAdapter) getWithHeaders(ctx context.Context, rawURL, origin string, headers map[string]string, maxBytes int64, budget *crawlBudget, policy *robotsPolicy) (WebsiteHTTPResponse, string, error) {
	current, err := canonicalWebsiteURL(rawURL, origin, origin)
	if err != nil {
		return WebsiteHTTPResponse{}, "", websiteFailure(WebsiteRedirect, false)
	}
	for hop := 0; hop <= maxWebsiteRedirects; hop++ {
		if policy != nil && !policy.canFetch(current) {
			return WebsiteHTTPResponse{}, "", websiteFailure(WebsiteRobots, false)
		}
		response, err := budget.request(ctx, current, headers, maxBytes)
		if err != nil {
			return WebsiteHTTPResponse{}, "", err
		}
		if !redirectStatus(response.Status) {
			return response, current, nil
		}
		location := response.Headers["location"]
		following, canonicalErr := canonicalWebsiteURL(location, current, origin)
		if location == "" || canonicalErr != nil {
			return WebsiteHTTPResponse{}, "", websiteFailure(WebsiteRedirect, false)
		}
		current = following
	}
	return WebsiteHTTPResponse{}, "", websiteFailure(WebsiteRedirect, false)
}

func redirectStatus(status int) bool {
	return status == 301 || status == 302 || status == 303 || status == 307 || status == 308
}

func (adapter *WebsiteSourceAdapter) robots(ctx context.Context, origin string, credential *WebsiteCredential, budget *crawlBudget) (robotsPolicy, []string, error) {
	response, _, err := adapter.get(ctx, origin+"/robots.txt", origin, credential, maxRobotsBytes, nil, budget, nil)
	if err != nil {
		return robotsPolicy{}, nil, err
	}
	if response.Status == http.StatusNotFound {
		return robotsPolicy{}, nil, nil
	}
	if response.Status != http.StatusOK {
		return robotsPolicy{}, nil, websiteFailure(WebsiteRobots, response.Status >= 500)
	}
	return parseRobots(response.Body)
}

func (adapter *WebsiteSourceAdapter) validateCrawl(ctx context.Context, request WebsiteRequest) (string, error) {
	origin, _ := websiteOrigin(request.RemoteURL)
	budget := newCrawlBudget(adapter.http, request.Limits)
	policy, _, err := adapter.robots(ctx, origin, request.Credential, budget)
	if err != nil {
		return "", err
	}
	if !policy.canFetch(request.RemoteURL) {
		return "", websiteFailure(WebsiteRobots, false)
	}
	response, finalURL, err := adapter.get(ctx, request.RemoteURL, origin, request.Credential, int64(request.Limits.MaxPageBytes), nil, budget, &policy)
	if err != nil {
		return "", err
	}
	if response.Status != http.StatusOK || !isHTMLType(contentType(response)) {
		return "", websiteFailure(WebsiteContent, false)
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(finalURL))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(response.Body)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

type previousWebsitePage struct {
	ContentPath  string   `json:"content_path"`
	ContentSHA   string   `json:"content_sha256"`
	ETag         *string  `json:"etag"`
	LastModified *string  `json:"last_modified"`
	Links        []string `json:"links"`
}

type previousWebsiteManifest struct {
	Pages []previousWebsiteManifestPage `json:"pages"`
}

type previousWebsiteManifestPage struct {
	CanonicalURL string   `json:"canonical_url"`
	ContentPath  string   `json:"content_path"`
	ContentSHA   string   `json:"content_sha256"`
	ETag         *string  `json:"etag"`
	LastModified *string  `json:"last_modified"`
	Links        []string `json:"links"`
}

type crawledWebsitePage struct {
	URL                  string
	Content              []byte
	ContentSHA           [sha256.Size]byte
	Links                []string
	ETag                 *string
	LastModified         *string
	Freshness            string
	ReusedFromRevisionID *ID
}

func (adapter *WebsiteSourceAdapter) materializeCrawl(ctx context.Context, request WebsiteRequest) (WebsiteSnapshot, error) {
	origin, _ := websiteOrigin(request.RemoteURL)
	budget := newCrawlBudget(adapter.http, request.Limits)
	previous, previousRoot := adapter.previousPages(request.SourceID, request.PreviousRevisionID)
	policy, sitemapSeeds, err := adapter.robots(ctx, origin, request.Credential, budget)
	if err != nil {
		return WebsiteSnapshot{}, err
	}
	sitemapSeeds = append(sitemapSeeds, origin+"/sitemap.xml")
	sitemapURLs, err := adapter.sitemaps(ctx, origin, request.Credential, sitemapSeeds, request.Limits.MaxPages, budget)
	if err != nil {
		return WebsiteSnapshot{}, err
	}
	seeds := append([]string{request.RemoteURL}, sitemapURLs...)
	slices.Sort(seeds)
	seeds = slices.Compact(seeds)
	type queuedURL struct {
		URL   string
		Depth int
	}
	queue := make([]queuedURL, 0, len(seeds))
	enqueued := make(map[string]struct{}, len(seeds))
	for _, seed := range seeds {
		queue = append(queue, queuedURL{URL: seed})
		enqueued[seed] = struct{}{}
	}
	discoveredCap := min(request.Limits.MaxPages*discoveredMultiplier, discoveredCeiling) + 1
	if discoveredCap < 1 {
		discoveredCap = 1
	}
	seen := make(map[string]struct{})
	pages := make(map[string]crawledWebsitePage)
	var totalPageBytes int64
	for len(queue) > 0 && len(pages) < request.Limits.MaxPages {
		remaining := request.Limits.MaxPages - len(pages)
		batch := make([]queuedURL, 0, min(request.Limits.Concurrency, remaining))
		for len(queue) > 0 && len(batch) < cap(batch) {
			item := queue[0]
			queue = queue[1:]
			canonical, canonicalErr := canonicalWebsiteURL(item.URL, request.RemoteURL, origin)
			if canonicalErr != nil || item.Depth > request.Limits.MaxDepth {
				continue
			}
			if _, exists := seen[canonical]; exists {
				continue
			}
			seen[canonical] = struct{}{}
			item.URL = canonical
			batch = append(batch, item)
		}
		if len(batch) == 0 {
			continue
		}
		type result struct {
			page  *crawledWebsitePage
			depth int
			err   error
		}
		results := make([]result, len(batch))
		var wait sync.WaitGroup
		for index, item := range batch {
			wait.Add(1)
			go func() {
				defer wait.Done()
				if !policy.canFetch(item.URL) {
					results[index] = result{depth: item.Depth}
					return
				}
				page, pageErr := adapter.page(ctx, item.URL, origin, request.Credential, int64(request.Limits.MaxPageBytes), previous[item.URL], previousRoot, request.PreviousRevisionID, budget, policy)
				results[index] = result{page: page, depth: item.Depth, err: pageErr}
			}()
		}
		wait.Wait()
		for _, value := range results {
			if value.err != nil {
				return WebsiteSnapshot{}, value.err
			}
			if value.page == nil {
				continue
			}
			if _, exists := pages[value.page.URL]; exists {
				continue
			}
			totalPageBytes += int64(len(value.page.Content))
			if totalPageBytes > request.Limits.MaxTotalBytes {
				return WebsiteSnapshot{}, websiteFailure(WebsiteLimit, false)
			}
			pages[value.page.URL] = *value.page
			if value.depth < request.Limits.MaxDepth {
				for _, link := range value.page.Links[:min(len(value.page.Links), maxLinksPerPage)] {
					if len(enqueued) >= discoveredCap {
						break
					}
					if _, exists := enqueued[link]; !exists {
						enqueued[link] = struct{}{}
						queue = append(queue, queuedURL{URL: link, Depth: value.depth + 1})
					}
				}
			}
		}
	}
	if len(pages) == 0 {
		return WebsiteSnapshot{}, websiteFailure(WebsiteContent, false)
	}
	return adapter.publishWebsiteSnapshot(request, pages)
}

func (adapter *WebsiteSourceAdapter) page(ctx context.Context, rawURL, origin string, credential *WebsiteCredential, maxBytes int64, previous *previousWebsitePage, previousRoot string, previousRevisionID *ID, budget *crawlBudget, policy robotsPolicy) (*crawledWebsitePage, error) {
	conditional := make(map[string]string)
	if previous != nil && previous.ETag != nil {
		conditional["If-None-Match"] = *previous.ETag
	}
	if previous != nil && previous.LastModified != nil {
		conditional["If-Modified-Since"] = *previous.LastModified
	}
	response, finalURL, err := adapter.get(ctx, rawURL, origin, credential, maxBytes, conditional, budget, &policy)
	if err != nil {
		return nil, err
	}
	if response.Status == http.StatusNotModified && previous != nil && previousRoot != "" && previousRevisionID != nil {
		content, readErr := readPreviousFile(previousRoot, previous.ContentPath, maxBytes)
		if readErr != nil {
			return nil, readErr
		}
		marker := bytes.Index(content, []byte("\n\n"))
		if marker < 0 {
			return nil, websiteFailure(WebsiteStorage, false)
		}
		digest, decodeErr := hex.DecodeString(previous.ContentSHA)
		if decodeErr != nil || len(digest) != sha256.Size {
			return nil, websiteFailure(WebsiteStorage, false)
		}
		var contentSHA [sha256.Size]byte
		copy(contentSHA[:], digest)
		return &crawledWebsitePage{URL: finalURL, Content: content[marker+2:], ContentSHA: contentSHA, Links: slices.Clone(previous.Links), ETag: cloneString(previous.ETag), LastModified: cloneString(previous.LastModified), Freshness: "reused", ReusedFromRevisionID: cloneID(previousRevisionID)}, nil
	}
	if response.Status == http.StatusNotFound || response.Status == http.StatusGone {
		return nil, nil
	}
	if response.Status != http.StatusOK || !isHTMLType(contentType(response)) {
		return nil, nil
	}
	if !strings.EqualFold(responseCharset(response), "utf-8") && !strings.EqualFold(responseCharset(response), "utf8") {
		return nil, websiteFailure(WebsiteContent, false)
	}
	if !utf8.Valid(response.Body) {
		return nil, websiteFailure(WebsiteContent, false)
	}
	parsed := parseWebsiteHTML(string(response.Body))
	canonical := finalURL
	if parsed.Canonical != "" {
		canonical, err = canonicalWebsiteURL(parsed.Canonical, finalURL, origin)
		if err != nil {
			return nil, websiteFailure(WebsiteContent, false)
		}
	}
	if !policy.canFetch(canonical) {
		return nil, nil
	}
	links := make([]string, 0, len(parsed.Links))
	for _, raw := range parsed.Links {
		link, linkErr := canonicalWebsiteURL(raw, finalURL, origin)
		if linkErr == nil {
			links = append(links, link)
		}
	}
	slices.Sort(links)
	links = slices.Compact(links)
	text := normalizedHTMLText(parsed.Text)
	title := normalizedHTMLText(parsed.Title)
	var rendered string
	if title != "" {
		rendered = "# " + title + "\n\n" + text + "\n"
	} else {
		rendered = text + "\n"
	}
	content := []byte(rendered)
	digest := sha256.Sum256(content)
	return &crawledWebsitePage{URL: canonical, Content: content, ContentSHA: digest, Links: links, ETag: mapStringPointer(response.Headers, "etag"), LastModified: mapStringPointer(response.Headers, "last-modified"), Freshness: "fresh"}, nil
}

type parsedWebsiteHTML struct {
	Links     []string
	Canonical string
	Title     string
	Text      string
}

var (
	hiddenHTMLPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`),
		regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`),
		regexp.MustCompile(`(?is)<template\b[^>]*>.*?</template\s*>`),
		regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript\s*>`),
		regexp.MustCompile(`(?is)<svg\b[^>]*>.*?</svg\s*>`),
	}
	titleHTMLPattern = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)
	tagHTMLPattern   = regexp.MustCompile(`(?is)<[^>]+>`)
	blockHTMLPattern = regexp.MustCompile(`(?i)</?(?:br|p|div|section|article|li|h1|h2|h3|h4)\b[^>]*>`)
	linkHTMLPattern  = regexp.MustCompile(`(?is)<(?:a|link)\b[^>]*>`)
	attrHTMLPattern  = regexp.MustCompile(`(?is)([A-Za-z_:][A-Za-z0-9_:.-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)
)

func parseWebsiteHTML(raw string) parsedWebsiteHTML {
	value := raw
	for _, pattern := range hiddenHTMLPatterns {
		value = pattern.ReplaceAllString(value, "")
	}
	result := parsedWebsiteHTML{}
	if match := titleHTMLPattern.FindStringSubmatch(value); len(match) == 2 {
		result.Title = html.UnescapeString(tagHTMLPattern.ReplaceAllString(match[1], ""))
	}
	for _, tag := range linkHTMLPattern.FindAllString(value, -1) {
		attributes := make(map[string]string)
		for _, match := range attrHTMLPattern.FindAllStringSubmatch(tag, -1) {
			attribute := match[2]
			if attribute == "" {
				attribute = match[3]
			}
			if attribute == "" {
				attribute = match[4]
			}
			attributes[strings.ToLower(match[1])] = html.UnescapeString(attribute)
		}
		lowered := strings.ToLower(tag)
		if strings.HasPrefix(lowered, "<a") && attributes["href"] != "" {
			result.Links = append(result.Links, attributes["href"])
		}
		if strings.HasPrefix(lowered, "<link") && strings.EqualFold(attributes["rel"], "canonical") {
			result.Canonical = attributes["href"]
		}
	}
	value = blockHTMLPattern.ReplaceAllString(value, "\n")
	value = tagHTMLPattern.ReplaceAllString(value, "")
	result.Text = html.UnescapeString(value)
	return result
}

func normalizedHTMLText(raw string) string {
	lines := strings.Split(raw, "\n")
	values := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			values = append(values, strings.Join(fields, " "))
		}
	}
	return strings.TrimSpace(strings.Join(values, "\n"))
}

func (adapter *WebsiteSourceAdapter) sitemaps(ctx context.Context, origin string, credential *WebsiteCredential, seeds []string, maxPages int, budget *crawlBudget) ([]string, error) {
	pending := slices.Clone(seeds)
	seen := make(map[string]struct{})
	urls := make(map[string]struct{})
	for len(pending) > 0 && len(seen) < 100 && len(urls) < maxPages {
		raw := pending[0]
		pending = pending[1:]
		current, err := canonicalWebsiteURL(raw, origin, origin)
		if err != nil {
			continue
		}
		if _, exists := seen[current]; exists {
			continue
		}
		seen[current] = struct{}{}
		response, finalURL, err := adapter.get(ctx, current, origin, credential, maxSitemapBytes, nil, budget, nil)
		if err != nil {
			return nil, err
		}
		if response.Status == http.StatusNotFound || response.Status == http.StatusGone {
			continue
		}
		if response.Status != http.StatusOK || !isXMLType(contentType(response)) {
			continue
		}
		upper := bytes.ToUpper(response.Body)
		if bytes.Contains(upper, []byte("<!DOCTYPE")) || bytes.Contains(upper, []byte("<!ENTITY")) {
			return nil, websiteFailure(WebsiteContent, false)
		}
		kind, locations, err := parseSitemap(response.Body)
		if err != nil {
			return nil, websiteFailure(WebsiteContent, false)
		}
		if kind == "sitemapindex" {
			pending = append(pending, locations...)
		} else if kind == "urlset" {
			for _, location := range locations {
				canonical, canonicalErr := canonicalWebsiteURL(location, finalURL, origin)
				if canonicalErr == nil {
					urls[canonical] = struct{}{}
				}
			}
		}
	}
	values := make([]string, 0, len(urls))
	for value := range urls {
		values = append(values, value)
	}
	slices.Sort(values)
	return values[:min(len(values), maxPages)], nil
}

func parseSitemap(body []byte) (string, []string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(body))
	root := ""
	locations := make([]string, 0)
	insideLoc := false
	var text strings.Builder
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, err
		}
		switch value := token.(type) {
		case xml.StartElement:
			if root == "" {
				root = value.Name.Local
			}
			if value.Name.Local == "loc" {
				insideLoc = true
				text.Reset()
			}
		case xml.CharData:
			if insideLoc {
				text.Write(value)
			}
		case xml.EndElement:
			if value.Name.Local == "loc" && insideLoc {
				if location := strings.TrimSpace(text.String()); location != "" {
					locations = append(locations, location)
				}
				insideLoc = false
			}
		}
	}
	if root == "" {
		return "", nil, errors.New("sitemap has no root")
	}
	return root, locations, nil
}

func (adapter *WebsiteSourceAdapter) previousPages(sourceID ID, revisionID *ID) (map[string]*previousWebsitePage, string) {
	if revisionID == nil {
		return map[string]*previousWebsitePage{}, ""
	}
	root, err := adapter.artifacts.ResolveArtifactKey(ArtifactKey(sourceID, *revisionID))
	if err != nil {
		return map[string]*previousWebsitePage{}, ""
	}
	body, err := readPreviousFile(root, "website-manifest.json", maxSitemapBytes)
	if err != nil {
		return map[string]*previousWebsitePage{}, ""
	}
	var manifest previousWebsiteManifest
	if json.Unmarshal(body, &manifest) != nil {
		return map[string]*previousWebsitePage{}, ""
	}
	result := make(map[string]*previousWebsitePage, len(manifest.Pages))
	for _, page := range manifest.Pages {
		if page.CanonicalURL == "" || page.ContentPath == "" || len(page.ContentSHA) != sha256.Size*2 {
			return map[string]*previousWebsitePage{}, ""
		}
		value := previousWebsitePage{ContentPath: page.ContentPath, ContentSHA: page.ContentSHA, ETag: page.ETag, LastModified: page.LastModified, Links: page.Links}
		result[page.CanonicalURL] = &value
	}
	return result, root
}

func readPreviousFile(root, relative string, maxBytes int64) ([]byte, error) {
	if sourcefiles.ValidateSourcePath(relative) != nil {
		return nil, websiteFailure(WebsiteStorage, false)
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	info, err := os.Lstat(candidate)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, websiteFailure(WebsiteStorage, false)
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, websiteFailure(WebsiteStorage, false)
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return nil, websiteFailure(WebsiteStorage, false)
	}
	body, err := os.ReadFile(resolved)
	if err != nil || int64(len(body)) > maxBytes {
		return nil, websiteFailure(WebsiteStorage, false)
	}
	return body, nil
}

func mapStringPointer(values map[string]string, key string) *string {
	value, exists := values[key]
	if !exists {
		return nil
	}
	return &value
}

func (adapter *WebsiteSourceAdapter) validateJSON(ctx context.Context, request WebsiteRequest) (string, error) {
	response, finalURL, err := adapter.getJSON(ctx, request)
	if err != nil {
		return "", err
	}
	if _, err := renderJSONDocument(response.Body, int64(request.Limits.MaxPageBytes)); err != nil {
		return "", err
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(finalURL))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(response.Body)
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (adapter *WebsiteSourceAdapter) getJSON(ctx context.Context, request WebsiteRequest) (WebsiteHTTPResponse, string, error) {
	origin, _ := websiteOrigin(request.RemoteURL)
	current, err := canonicalWebsiteURL(request.RemoteURL, origin, origin)
	if err != nil {
		return WebsiteHTTPResponse{}, "", websiteFailure(WebsiteHTTP, false)
	}
	budget := newCrawlBudget(adapter.http, request.Limits)
	response, err := budget.request(ctx, current, websiteHeaders(request.Credential, "application/json, application/*+json;q=0.9, */*;q=0.1", nil), int64(request.Limits.MaxPageBytes))
	if err != nil {
		return WebsiteHTTPResponse{}, "", err
	}
	if redirectStatus(response.Status) {
		return WebsiteHTTPResponse{}, "", websiteFailure(WebsiteRedirect, false)
	}
	if response.Status != http.StatusOK {
		return WebsiteHTTPResponse{}, "", websiteFailure(WebsiteHTTP, response.Status >= 500)
	}
	if !isJSONType(contentType(response)) {
		return WebsiteHTTPResponse{}, "", websiteFailure(WebsiteContent, false)
	}
	return response, current, nil
}

func (adapter *WebsiteSourceAdapter) materializeJSON(ctx context.Context, request WebsiteRequest) (WebsiteSnapshot, error) {
	response, finalURL, err := adapter.getJSON(ctx, request)
	if err != nil {
		return WebsiteSnapshot{}, err
	}
	rendered, err := renderJSONDocument(response.Body, int64(request.Limits.MaxPageBytes))
	if err != nil {
		return WebsiteSnapshot{}, err
	}
	origin, _ := websiteOrigin(request.RemoteURL)
	canonical, err := canonicalWebsiteURL(finalURL, origin, origin)
	if err != nil {
		return WebsiteSnapshot{}, websiteFailure(WebsiteContent, false)
	}
	contentDigest := sha256.Sum256(rendered)
	native := sha256.New()
	_, _ = native.Write([]byte(canonical))
	_, _ = native.Write([]byte{0})
	_, _ = native.Write(rendered)
	var nativeDigest [sha256.Size]byte
	copy(nativeDigest[:], native.Sum(nil))
	page := crawledWebsitePage{URL: canonical, Content: rendered, ContentSHA: contentDigest, Freshness: "fresh"}
	return adapter.publishWebsiteSnapshotWithDigest(request, map[string]crawledWebsitePage{canonical: page}, nativeDigest)
}

func renderJSONDocument(body []byte, maxBytes int64) ([]byte, error) {
	if len(body) == 0 || int64(len(body)) > maxBytes {
		return nil, websiteFailure(WebsiteLimit, false)
	}
	if !utf8.Valid(body) {
		return nil, websiteFailure(WebsiteContent, false)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	value, err := decodeUniqueJSON(decoder, 0)
	if err != nil {
		var depth *jsonDepthError
		if errors.As(err, &depth) {
			return nil, websiteFailure(WebsiteLimit, false)
		}
		return nil, websiteFailure(WebsiteContent, false)
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return nil, websiteFailure(WebsiteContent, false)
	}
	var rendered bytes.Buffer
	if err := writeCanonicalJSON(&rendered, value, 0); err != nil {
		return nil, websiteFailure(WebsiteContent, false)
	}
	rendered.WriteByte('\n')
	if int64(rendered.Len()) > maxBytes {
		return nil, websiteFailure(WebsiteLimit, false)
	}
	return rendered.Bytes(), nil
}

type jsonDepthError struct{}

func (*jsonDepthError) Error() string { return "JSON exceeds depth limit" }

func decodeUniqueJSON(decoder *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, &jsonDepthError{}
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	switch value := token.(type) {
	case json.Delim:
		switch value {
		case '{':
			object := make(map[string]any)
			for decoder.More() {
				keyToken, keyErr := decoder.Token()
				key, okay := keyToken.(string)
				if keyErr != nil || !okay {
					return nil, errors.New("invalid JSON object")
				}
				if _, duplicate := object[key]; duplicate {
					return nil, errors.New("duplicate JSON object key")
				}
				item, itemErr := decodeUniqueJSON(decoder, depth+1)
				if itemErr != nil {
					return nil, itemErr
				}
				object[key] = item
			}
			if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim('}') {
				return nil, errors.New("invalid JSON object")
			}
			return object, nil
		case '[':
			array := make([]any, 0)
			for decoder.More() {
				item, itemErr := decodeUniqueJSON(decoder, depth+1)
				if itemErr != nil {
					return nil, itemErr
				}
				array = append(array, item)
			}
			if closing, closeErr := decoder.Token(); closeErr != nil || closing != json.Delim(']') {
				return nil, errors.New("invalid JSON array")
			}
			return array, nil
		default:
			return nil, errors.New("invalid JSON delimiter")
		}
	case string, bool, nil, json.Number:
		return value, nil
	default:
		return nil, errors.New("invalid JSON scalar")
	}
}

func writeCanonicalJSON(output *bytes.Buffer, value any, depth int) error {
	indent := strings.Repeat("  ", depth)
	nextIndent := strings.Repeat("  ", depth+1)
	switch value := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		output.WriteByte('{')
		if len(keys) > 0 {
			output.WriteByte('\n')
		}
		for index, key := range keys {
			output.WriteString(nextIndent)
			writeASCIIJSONString(output, key)
			output.WriteString(": ")
			if err := writeCanonicalJSON(output, value[key], depth+1); err != nil {
				return err
			}
			if index+1 < len(keys) {
				output.WriteByte(',')
			}
			output.WriteByte('\n')
		}
		if len(keys) > 0 {
			output.WriteString(indent)
		}
		output.WriteByte('}')
	case []any:
		output.WriteByte('[')
		if len(value) > 0 {
			output.WriteByte('\n')
		}
		for index, item := range value {
			output.WriteString(nextIndent)
			if err := writeCanonicalJSON(output, item, depth+1); err != nil {
				return err
			}
			if index+1 < len(value) {
				output.WriteByte(',')
			}
			output.WriteByte('\n')
		}
		if len(value) > 0 {
			output.WriteString(indent)
		}
		output.WriteByte(']')
	case string:
		writeASCIIJSONString(output, value)
	case bool:
		output.WriteString(strconv.FormatBool(value))
	case nil:
		output.WriteString("null")
	case json.Number:
		if _, err := strconv.ParseFloat(value.String(), 64); err != nil {
			return err
		}
		output.WriteString(value.String())
	default:
		return errors.New("unsupported JSON value")
	}
	return nil
}

func writeASCIIJSONString(output *bytes.Buffer, value string) {
	output.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"', '\\':
			output.WriteByte('\\')
			output.WriteRune(character)
		case '\b':
			output.WriteString(`\b`)
		case '\f':
			output.WriteString(`\f`)
		case '\n':
			output.WriteString(`\n`)
		case '\r':
			output.WriteString(`\r`)
		case '\t':
			output.WriteString(`\t`)
		default:
			if character < 0x20 || character > 0x7e {
				if character <= 0xffff {
					fmt.Fprintf(output, `\u%04x`, character)
				} else {
					high, low := utf16.EncodeRune(character)
					fmt.Fprintf(output, `\u%04x\u%04x`, high, low)
				}
			} else {
				output.WriteRune(character)
			}
		}
	}
	output.WriteByte('"')
}

func (adapter *WebsiteSourceAdapter) publishWebsiteSnapshot(request WebsiteRequest, pages map[string]crawledWebsitePage) (WebsiteSnapshot, error) {
	digest := sha256.New()
	urls := make([]string, 0, len(pages))
	for rawURL := range pages {
		urls = append(urls, rawURL)
	}
	slices.Sort(urls)
	for _, rawURL := range urls {
		_, _ = digest.Write([]byte(rawURL))
		_, _ = digest.Write([]byte{0})
		page := pages[rawURL]
		_, _ = digest.Write(page.ContentSHA[:])
	}
	var native [sha256.Size]byte
	copy(native[:], digest.Sum(nil))
	return adapter.publishWebsiteSnapshotWithDigest(request, pages, native)
}

func (adapter *WebsiteSourceAdapter) publishWebsiteSnapshotWithDigest(request WebsiteRequest, pages map[string]crawledWebsitePage, native [sha256.Size]byte) (WebsiteSnapshot, error) {
	nativeVersion := hex.EncodeToString(native[:])
	urls := make([]string, 0, len(pages))
	for rawURL := range pages {
		urls = append(urls, rawURL)
	}
	slices.Sort(urls)
	files := make([]sourcefiles.File, 0, len(pages)+1)
	captures := make([]PageCapture, 0, len(pages))
	manifestPages := make([]map[string]any, 0, len(pages))
	var byteCount int64
	for _, rawURL := range urls {
		page := pages[rawURL]
		pathDigest := sha256.Sum256([]byte(rawURL))
		contentPath := "pages/" + hex.EncodeToString(pathDigest[:]) + ".md"
		evidence := "web://" + request.SourceID.String() + "@" + nativeVersion + "/" + percentQuote(rawURL)
		content := append([]byte("Source URL: "+rawURL+"\nEvidence: "+evidence+"\n\n"), page.Content...)
		files = append(files, sourcefiles.File{Path: contentPath, Content: content})
		byteCount += int64(len(content))
		captures = append(captures, PageCapture{CanonicalURL: rawURL, ContentPath: contentPath, ContentSHA256: page.ContentSHA, EvidenceURI: evidence, Freshness: page.Freshness, ETag: cloneString(page.ETag), LastModified: cloneString(page.LastModified), ReusedFromRevisionID: cloneID(page.ReusedFromRevisionID)})
		links := slices.Clone(page.Links)
		if links == nil {
			links = []string{}
		}
		manifestPages = append(manifestPages, map[string]any{
			"canonical_url":           rawURL,
			"content_path":            contentPath,
			"content_sha256":          hex.EncodeToString(page.ContentSHA[:]),
			"evidence_uri":            evidence,
			"etag":                    page.ETag,
			"freshness":               page.Freshness,
			"last_modified":           page.LastModified,
			"links":                   links,
			"reused_from_revision_id": idString(page.ReusedFromRevisionID),
		})
	}
	manifest, err := json.Marshal(map[string]any{"native_version": nativeVersion, "pages": manifestPages, "root_url": request.RemoteURL})
	if err != nil {
		return WebsiteSnapshot{}, websiteFailure(WebsiteStorage, false)
	}
	files = append(files, sourcefiles.File{Path: "website-manifest.json", Content: manifest})
	byteCount += int64(len(manifest))
	stored, err := adapter.artifacts.StoreSnapshot(sourcefiles.ID(request.SourceID), sourcefiles.ID(*request.RevisionID), sourcefiles.Files(files...), nil)
	if err != nil {
		return WebsiteSnapshot{}, websiteFailure(WebsiteStorage, true)
	}
	return WebsiteSnapshot{NativeVersion: nativeVersion, ArtifactKey: stored.ArtifactKey, Fingerprint: native, FileCount: len(files), ByteCount: byteCount, Pages: captures}, nil
}

func percentQuote(value string) string {
	const digits = "0123456789ABCDEF"
	var result strings.Builder
	for _, character := range []byte(value) {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("-._~", rune(character)) {
			result.WriteByte(character)
		} else {
			result.WriteByte('%')
			result.WriteByte(digits[character>>4])
			result.WriteByte(digits[character&15])
		}
	}
	return result.String()
}
