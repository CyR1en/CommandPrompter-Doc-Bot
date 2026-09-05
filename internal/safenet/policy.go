package safenet

import (
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	MaxBodyBytes = 1_048_576
	MaxJSONDepth = 16
	MaxModels    = 10_000
)

type FailureCode string

const (
	InvalidURL       FailureCode = "invalid_url"
	PolicyDenied     FailureCode = "policy_denied"
	Timeout          FailureCode = "timeout"
	Connection       FailureCode = "connection"
	Redirect         FailureCode = "redirect"
	HTTPStatus       FailureCode = "http_status"
	ResponseTooLarge FailureCode = "response_too_large"
	InvalidJSON      FailureCode = "invalid_json"
)

var safeMessages = map[FailureCode]string{
	InvalidURL:       "Provider URL or request path is invalid.",
	PolicyDenied:     "Provider network policy denied the connection.",
	Timeout:          "Provider request timed out.",
	Connection:       "Provider connection failed.",
	Redirect:         "Provider returned a redirect.",
	HTTPStatus:       "Provider returned an unsuccessful status.",
	ResponseTooLarge: "Provider response exceeded the size limit.",
	InvalidJSON:      "Provider returned invalid JSON.",
}

type RequestError struct {
	Code         FailureCode
	Retryable    bool
	HTTPStatus   int
	RetryHeaders map[string]string
}

func (err *RequestError) Error() string {
	if message, exists := safeMessages[err.Code]; exists {
		return message
	}
	return "Provider request failed."
}

type Policy struct {
	AllowPrivateAddresses bool
	AllowPlainHTTP        bool
}

func (policy Policy) Validate() error {
	if policy.AllowPlainHTTP && !policy.AllowPrivateAddresses {
		return errors.New("plain HTTP requires private-address access")
	}
	return nil
}

var (
	metadataAddresses = map[netip.Addr]struct{}{
		netip.MustParseAddr("100.100.100.200"): {},
		netip.MustParseAddr("168.63.129.16"):   {},
		netip.MustParseAddr("169.254.169.254"): {},
		netip.MustParseAddr("fd00:ec2::254"):   {},
	}
	localPrefixes = []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("fc00::/7"),
	}
	nonPublicPrefixes = []netip.Prefix{
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
)

func AddressAllowed(raw string, policy Policy, requirePrivate bool) bool {
	if strings.Contains(raw, "%") {
		return false
	}
	address, err := netip.ParseAddr(raw)
	if err != nil {
		return false
	}
	address = address.Unmap()
	if _, blocked := metadataAddresses[address]; blocked {
		return false
	}
	local := inPrefixes(address, localPrefixes)
	if requirePrivate {
		return policy.AllowPrivateAddresses && local
	}
	if local {
		return policy.AllowPrivateAddresses
	}
	return address.IsGlobalUnicast() && !inPrefixes(address, nonPublicPrefixes)
}

func inPrefixes(address netip.Addr, prefixes []netip.Prefix) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func NormalizeBaseURL(raw string, policy Policy) (*url.URL, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	candidate := strings.TrimSpace(raw)
	if candidate == "" || strings.ContainsAny(candidate, "?#") {
		return nil, requestError(InvalidURL)
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return nil, requestError(InvalidURL)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return nil, requestError(InvalidURL)
	}
	if parsed.Scheme == "http" && !(policy.AllowPrivateAddresses && policy.AllowPlainHTTP) {
		return nil, requestError(PolicyDenied)
	}
	if parsed.Hostname() == "" || strings.IndexFunc(parsed.Hostname(), func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) >= 0 {
		return nil, requestError(InvalidURL)
	}
	if parsed.Port() != "" {
		port, portErr := strconv.Atoi(parsed.Port())
		if portErr != nil || port < 1 || port > 65535 {
			return nil, requestError(InvalidURL)
		}
	}
	if !safePath(parsed.EscapedPath(), true) {
		return nil, requestError(InvalidURL)
	}
	if parsed.Path == "" || parsed.Path == "/" {
		parsed.Path = "/v1"
		parsed.RawPath = ""
	}
	if (parsed.Scheme == "https" && parsed.Port() == "443") || (parsed.Scheme == "http" && parsed.Port() == "80") {
		parsed.Host = hostnameForURL(parsed.Hostname())
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	parsed.RawPath = strings.TrimSuffix(parsed.RawPath, "/")
	return parsed, nil
}

func NormalizeRelativePath(raw string) (string, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" || strings.HasPrefix(candidate, "/") || strings.ContainsAny(candidate, "?#") {
		return "", requestError(InvalidURL)
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.IsAbs() || parsed.Host != "" || !safePath("/"+parsed.EscapedPath(), false) {
		return "", requestError(InvalidURL)
	}
	return parsed.Path, nil
}

func safePath(raw string, allowRoot bool) bool {
	if raw == "" {
		return allowRoot
	}
	decoded := raw
	for {
		lowered := strings.ToLower(decoded)
		if strings.Contains(decoded, `\`) || strings.Contains(lowered, "%00") ||
			strings.Contains(lowered, "%2e") || strings.Contains(lowered, "%2f") ||
			strings.Contains(lowered, "%5c") || strings.IndexFunc(decoded, func(r rune) bool {
			return r < 32 || r == 127
		}) >= 0 {
			return false
		}
		next, err := url.PathUnescape(decoded)
		if err != nil {
			return false
		}
		if next == decoded {
			break
		}
		decoded = next
	}
	segments := strings.Split(decoded, "/")
	for index, segment := range segments {
		if segment == "." || segment == ".." {
			return false
		}
		if segment == "" && index != 0 && index != len(segments)-1 {
			return false
		}
	}
	return true
}

func hostnameForURL(hostname string) string {
	if strings.Contains(hostname, ":") {
		return "[" + hostname + "]"
	}
	return hostname
}

func requestError(code FailureCode) *RequestError {
	return &RequestError{Code: code}
}
