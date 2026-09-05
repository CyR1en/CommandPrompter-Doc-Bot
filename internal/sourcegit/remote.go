package sourcegit

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/safenet"
)

const (
	defaultRemoteTimeout = 15 * time.Second
	gitCapabilityTimeout = 5 * time.Second
	askpassProgram       = "#!/bin/sh\ncase \"$1\" in\n  *sername*) printf '%s\\n' \"$REF0_GIT_USERNAME\" ;;\n  *) printf '%s\\n' \"$REF0_GIT_SECRET\" ;;\nesac\n"
)

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

type NetworkPolicy struct {
	AllowPrivateAddresses bool
}

type Resolver func(context.Context, string, int) ([]string, error)

type Credential struct {
	Username string
	secret   string
}

func NewCredential(username, secret string) (*Credential, error) {
	if username == "" || username != strings.TrimSpace(username) || utf8.RuneCountInString(username) > 255 {
		return nil, errors.New("repository username is invalid")
	}
	for _, character := range username {
		if character < 33 || character == 127 {
			return nil, errors.New("repository username is invalid")
		}
	}
	if secret == "" {
		return nil, errors.New("repository secret must not be empty")
	}
	return &Credential{Username: username, secret: secret}, nil
}

func (credential Credential) String() string {
	return "GitCredential(" + credential.Username + ", <redacted>)"
}

func (credential Credential) GoString() string { return credential.String() }

type Remote struct {
	URL  string
	Host string
	Port int
}

type ValidationEvidence struct {
	Commit                  string
	TLSVerified             bool
	AuthenticationSucceeded bool
}

type ValidatorOptions struct {
	Policy          NetworkPolicy
	Resolver        Resolver
	Timeout         time.Duration
	CAFile          string
	CapabilityProbe func(context.Context) error
}

type Validator struct {
	policy   NetworkPolicy
	resolver Resolver
	timeout  time.Duration
	caFile   string
}

func NewValidator(options ValidatorOptions) (*Validator, error) {
	probe := options.CapabilityProbe
	if probe == nil {
		probe = requireCurloptResolve
	}
	probeContext, cancel := context.WithTimeout(context.Background(), gitCapabilityTimeout)
	defer cancel()
	if err := probe(probeContext); err != nil {
		return nil, errors.New("Git must support http.curloptResolve")
	}
	timeout := options.Timeout
	if timeout == 0 {
		timeout = defaultRemoteTimeout
	}
	if timeout <= 0 || timeout > 30*time.Second {
		return nil, errors.New("Git timeout must be between 0 and 30 seconds")
	}
	if options.CAFile != "" {
		info, err := os.Stat(options.CAFile)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("Git CA file does not exist")
		}
	}
	resolver := options.Resolver
	if resolver == nil {
		resolver = defaultAddressResolver
	}
	return &Validator{
		policy: options.Policy, resolver: resolver, timeout: timeout, caFile: options.CAFile,
	}, nil
}

func NormalizeRepositoryURL(raw string) (Remote, error) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" || strings.ContainsAny(candidate, "?#") || strings.IndexFunc(candidate, func(r rune) bool {
		return r < 32 || r == 127
	}) >= 0 {
		return Remote{}, remoteError(RemoteInvalidURL)
	}
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Opaque != "" || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Remote{}, remoteError(RemoteInvalidURL)
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || strings.HasSuffix(host, ".") || len([]byte(host)) > 253 || strings.Contains(host, "%") || strings.IndexFunc(host, func(r rune) bool {
		return r > unicode.MaxASCII
	}) >= 0 {
		return Remote{}, remoteError(RemoteInvalidURL)
	}
	port := 443
	if rawPort := parsed.Port(); rawPort != "" {
		port, err = strconv.Atoi(rawPort)
		if err != nil || port < 1 || port > 65535 {
			return Remote{}, remoteError(RemoteInvalidURL)
		}
	} else if strings.LastIndex(parsed.Host, ":") >= 0 && !strings.HasPrefix(parsed.Host, "[") {
		if net.ParseIP(parsed.Host) == nil && strings.Contains(parsed.Host, ":") {
			return Remote{}, remoteError(RemoteInvalidURL)
		}
	}
	path := parsed.Path
	if path == "" || path == "/" || strings.Contains(path, `\`) || strings.Contains(candidate, "%") {
		return Remote{}, remoteError(RemoteInvalidURL)
	}
	path = strings.TrimSuffix(path, "/")
	segments := strings.Split(path, "/")
	if len(segments) < 2 || segments[0] != "" {
		return Remote{}, remoteError(RemoteInvalidURL)
	}
	for _, segment := range segments[1:] {
		if segment == "" || segment == "." || segment == ".." {
			return Remote{}, remoteError(RemoteInvalidURL)
		}
	}
	renderedHost := host
	if address, parseErr := netip.ParseAddr(host); parseErr == nil {
		if address.Is6() {
			renderedHost = "[" + host + "]"
		}
	} else {
		for _, label := range strings.Split(host, ".") {
			if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return Remote{}, remoteError(RemoteInvalidURL)
			}
			for _, character := range label {
				if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
					return Remote{}, remoteError(RemoteInvalidURL)
				}
			}
		}
	}
	authority := renderedHost
	if port != 443 {
		authority += ":" + strconv.Itoa(port)
	}
	return Remote{URL: "https://" + authority + path, Host: host, Port: port}, nil
}

type gitTransport struct {
	remote        Remote
	configuration []string
	environment   []string
	cwd           string
}

func (transport gitTransport) command(arguments ...string) []string {
	command := make([]string, 0, len(transport.configuration)+len(arguments))
	command = append(command, transport.configuration...)
	return append(command, arguments...)
}

type transportProvider interface {
	withTransport(context.Context, string, *Credential, func(gitTransport) error) error
}

func (validator *Validator) withTransport(ctx context.Context, rawURL string, credential *Credential, operation func(gitTransport) error) error {
	remote, err := NormalizeRepositoryURL(rawURL)
	if err != nil {
		return err
	}
	addresses, err := validator.resolve(ctx, remote)
	if err != nil {
		return err
	}
	directory, err := os.MkdirTemp("", "ref0-git-askpass-")
	if err != nil {
		return remoteError(RemoteConnection, true)
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o700); err != nil {
		return remoteError(RemoteConnection, true)
	}
	askpass := filepath.Join(directory, "askpass")
	if err := os.WriteFile(askpass, []byte(askpassProgram), 0o700); err != nil {
		return remoteError(RemoteConnection, true)
	}
	if err := os.Chmod(askpass, 0o700); err != nil {
		return remoteError(RemoteConnection, true)
	}
	username, secret := "", ""
	if credential != nil {
		username, secret = credential.Username, credential.secret
	}
	environment := append(baseGitEnvironment(),
		"GCM_INTERACTIVE=never",
		"GIT_ASKPASS="+askpass,
		"REF0_GIT_USERNAME="+username,
		"REF0_GIT_SECRET="+secret,
	)
	if validator.caFile != "" {
		environment = append(environment, "GIT_SSL_CAINFO="+validator.caFile)
	}
	pinnedAddress := addresses[0].String()
	if addresses[0].Is6() {
		pinnedAddress = "[" + pinnedAddress + "]"
	}
	configuration := []string{
		"-c", "protocol.allow=never",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ext.allow=never",
		"-c", "http.followRedirects=false",
		"-c", "http.maxRequests=1",
		"-c", "http.sslVerify=true",
		"-c", "credential.helper=",
		"-c", "core.hooksPath=/dev/null",
		"-c", "submodule.recurse=false",
		"-c", fmt.Sprintf("http.curloptResolve=%s:%d:%s", remote.Host, remote.Port, pinnedAddress),
	}
	return operation(gitTransport{remote: remote, configuration: configuration, environment: environment, cwd: directory})
}

func (validator *Validator) resolve(ctx context.Context, remote Remote) ([]netip.Addr, error) {
	answers, err := validator.resolver(ctx, remote.Host, remote.Port)
	if err != nil {
		var safe *RemoteError
		if errors.As(err, &safe) {
			return nil, safe
		}
		return nil, remoteError(RemoteDNS, true)
	}
	resolved := make([]netip.Addr, 0, len(answers))
	seen := map[netip.Addr]struct{}{}
	for _, raw := range answers {
		if strings.Contains(raw, "%") {
			return nil, remoteError(RemotePolicyDenied)
		}
		address, err := netip.ParseAddr(raw)
		if err != nil {
			return nil, remoteError(RemoteDNS, true)
		}
		address = address.Unmap()
		if !safenet.AddressAllowed(address.String(), safenet.Policy{AllowPrivateAddresses: validator.policy.AllowPrivateAddresses}, false) {
			return nil, remoteError(RemotePolicyDenied)
		}
		if _, exists := seen[address]; !exists {
			seen[address] = struct{}{}
			resolved = append(resolved, address)
		}
	}
	if len(resolved) == 0 {
		return nil, remoteError(RemoteDNS, true)
	}
	return resolved, nil
}

func (validator *Validator) ValidateBranch(ctx context.Context, rawURL, branch string, credential *Credential) (ValidationEvidence, error) {
	if !validBranch(branch) {
		return ValidationEvidence{}, remoteError(RemoteInvalidResponse)
	}
	reference := "refs/heads/" + branch
	var result commandResult
	err := validator.withTransport(ctx, rawURL, credential, func(transport gitTransport) error {
		var executeErr error
		result, executeErr = executeGit(ctx, transport.command("ls-remote", "--refs", transport.remote.URL, reference), transport.environment, transport.cwd, maximumGitDiagnosticBytes, validator.timeout, nil)
		return validator.mapExecutionError(executeErr)
	})
	if err != nil {
		return ValidationEvidence{}, err
	}
	if result.exitCode != 0 {
		code := classifyRemoteFailure(result.stderr, credential)
		return ValidationEvidence{}, remoteError(code, code == RemoteConnection)
	}
	commit, err := parseRemoteReference(result.stdout, reference)
	if err != nil {
		return ValidationEvidence{}, err
	}
	return ValidationEvidence{Commit: commit, TLSVerified: true, AuthenticationSucceeded: true}, nil
}

func (validator *Validator) ValidateCommit(ctx context.Context, rawURL, commit string, credential *Credential) (ValidationEvidence, error) {
	normalized := strings.ToLower(commit)
	if !commitPattern.MatchString(normalized) {
		return ValidationEvidence{}, remoteError(RemoteInvalidResponse)
	}
	directory, err := os.MkdirTemp("", "ref0-git-commit-validation-")
	if err != nil {
		return ValidationEvidence{}, remoteError(RemoteConnection, true)
	}
	defer os.RemoveAll(directory)
	mirror := filepath.Join(directory, "validation.git")
	err = validator.withTransport(ctx, rawURL, credential, func(transport gitTransport) error {
		initArguments := []string{"init", "--bare"}
		if len(normalized) == 64 {
			initArguments = append(initArguments, "--object-format=sha256")
		}
		initArguments = append(initArguments, mirror)
		result, executeErr := executeGit(ctx, transport.command(initArguments...), transport.environment, transport.cwd, maximumGitDiagnosticBytes, validator.timeout, nil)
		if mapped := validator.mapExecutionError(executeErr); mapped != nil {
			return mapped
		}
		if result.exitCode != 0 {
			return remoteError(RemoteConnection, true)
		}
		result, executeErr = executeGit(ctx, transport.command("--git-dir", mirror, "fetch", "--quiet", "--force", "--prune", "--no-tags", "--no-recurse-submodules", "--no-write-fetch-head", transport.remote.URL, "+refs/heads/*:refs/heads/*"), transport.environment, transport.cwd, maximumGitDiagnosticBytes, validator.timeout, nil)
		if mapped := validator.mapExecutionError(executeErr); mapped != nil {
			return mapped
		}
		if result.exitCode != 0 {
			code := classifyRemoteFailure(result.stderr, credential)
			return remoteError(code, code == RemoteConnection)
		}
		result, executeErr = executeGit(ctx, transport.command("--git-dir", mirror, "cat-file", "-e", normalized+"^{commit}"), transport.environment, transport.cwd, maximumGitDiagnosticBytes, validator.timeout, nil)
		if mapped := validator.mapExecutionError(executeErr); mapped != nil {
			return mapped
		}
		if result.exitCode != 0 {
			return remoteError(RemoteRefNotFound)
		}
		return nil
	})
	if err != nil {
		return ValidationEvidence{}, err
	}
	return ValidationEvidence{Commit: normalized, TLSVerified: true, AuthenticationSucceeded: true}, nil
}

func (validator *Validator) mapExecutionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errCommandOutputLimit) {
		return remoteError(RemoteOutputTooLarge)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return remoteError(RemoteTimeout, true)
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return remoteError(RemoteConnection, true)
}

func parseRemoteReference(raw []byte, reference string) (string, error) {
	if !utf8.Valid(raw) {
		return "", remoteError(RemoteInvalidResponse)
	}
	for _, value := range raw {
		if value > unicode.MaxASCII {
			return "", remoteError(RemoteInvalidResponse)
		}
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return "", remoteError(RemoteRefNotFound)
	}
	if len(lines) != 1 {
		return "", remoteError(RemoteInvalidResponse)
	}
	parts := strings.SplitN(lines[0], "\t", 2)
	if len(parts) != 2 {
		parts = strings.Fields(lines[0])
	}
	if len(parts) != 2 || parts[1] != reference || !commitPattern.MatchString(parts[0]) {
		return "", remoteError(RemoteInvalidResponse)
	}
	return parts[0], nil
}

func classifyRemoteFailure(stderr []byte, credential *Credential) RemoteFailureCode {
	lowered := strings.ToLower(string(stderr))
	if strings.Contains(lowered, "authentication failed") || strings.Contains(lowered, "requested url returned error: 401") || credential != nil && strings.Contains(lowered, "could not read username") {
		return RemoteAuthentication
	}
	return RemoteConnection
}

func validBranch(branch string) bool {
	if branch == "" || branch != strings.TrimSpace(branch) || utf8.RuneCountInString(branch) > 255 || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.Contains(branch, "..") || strings.Contains(branch, "//") || strings.Contains(branch, `\`) || strings.Contains(branch, "@{") || strings.ContainsAny(branch, " ~^:?*[") || branch == "@" {
		return false
	}
	for _, character := range branch {
		if character < 32 || character == 127 {
			return false
		}
	}
	for _, segment := range strings.Split(branch, "/") {
		lowered := strings.ToLower(segment)
		if segment == "" || strings.HasPrefix(segment, ".") || strings.HasSuffix(segment, ".") || strings.HasSuffix(lowered, ".lock") {
			return false
		}
	}
	return true
}

func defaultAddressResolver(ctx context.Context, host string, _ int) ([]string, error) {
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
