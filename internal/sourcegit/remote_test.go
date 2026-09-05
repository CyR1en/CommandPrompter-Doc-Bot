package sourcegit

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/sourcefiles"
)

func TestNormalizeRepositoryURLMatchesClosedHTTPSGrammar(t *testing.T) {
	invalid := []string{
		"http://git.example/org/repo.git",
		"ssh://git.example/org/repo.git",
		"git@example:org/repo.git",
		"https://user:secret@git.example/org/repo.git",
		"https://git.example/../repo.git",
		"https://git.example/org/%2e%2e/repo.git",
		"https://git.example/org//repo.git",
		"https://git.example/org/repo.git?redirect=1",
		"https://git.example/org/repo.git#main",
		"https://git.example:0/org/repo.git",
		"https://bad_host.example/org/repo.git",
		"https://git.example./org/repo.git",
		"https://git.example/org/repo.git\nnext",
	}
	for _, raw := range invalid {
		t.Run(raw, func(t *testing.T) {
			_, err := NormalizeRepositoryURL(raw)
			assertRemoteCode(t, err, RemoteInvalidURL)
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error exposed URL content: %v", err)
			}
		})
	}

	remote, err := NormalizeRepositoryURL("https://Git.Example:443/org/repo.git/")
	if err != nil {
		t.Fatal(err)
	}
	if remote != (Remote{URL: "https://git.example/org/repo.git", Host: "git.example", Port: 443}) {
		t.Fatalf("normalized remote = %#v", remote)
	}
	remote, err = NormalizeRepositoryURL("https://[2001:db8::1]:8443/team/repository.git")
	if err != nil || remote.URL != "https://[2001:db8::1]:8443/team/repository.git" || remote.Host != "2001:db8::1" || remote.Port != 8443 {
		t.Fatalf("IPv6 remote = %#v, %v", remote, err)
	}
}

func TestResolverRejectsMixedMetadataAndRequiresPrivatePolicy(t *testing.T) {
	tests := []struct {
		name      string
		answers   []string
		allow     bool
		wantCode  RemoteFailureCode
		retryable bool
	}{
		{name: "mixed metadata", answers: []string{"8.8.8.8", "169.254.169.254"}, wantCode: RemotePolicyDenied},
		{name: "metadata despite private policy", answers: []string{"169.254.169.254"}, allow: true, wantCode: RemotePolicyDenied},
		{name: "private denied", answers: []string{"127.0.0.1"}, wantCode: RemotePolicyDenied},
		{name: "malformed DNS", answers: []string{"not-an-address"}, wantCode: RemoteDNS, retryable: true},
		{name: "scoped address", answers: []string{"fe80::1%en0"}, allow: true, wantCode: RemotePolicyDenied},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator := mustValidator(t, ValidatorOptions{
				Policy: NetworkPolicy{AllowPrivateAddresses: test.allow},
				Resolver: func(context.Context, string, int) ([]string, error) {
					return test.answers, nil
				},
			})
			_, err := validator.resolve(context.Background(), Remote{Host: "git.example", Port: 443})
			assertRemoteCode(t, err, test.wantCode)
			var remoteErr *RemoteError
			if !errors.As(err, &remoteErr) || remoteErr.Retryable != test.retryable {
				t.Fatalf("retryable = %v", err)
			}
		})
	}

	validator := mustValidator(t, ValidatorOptions{
		Policy: NetworkPolicy{AllowPrivateAddresses: true},
		Resolver: func(context.Context, string, int) ([]string, error) {
			return []string{"::ffff:127.0.0.1", "127.0.0.1"}, nil
		},
	})
	addresses, err := validator.resolve(context.Background(), Remote{Host: "git.example", Port: 443})
	if err != nil || len(addresses) != 1 || addresses[0].String() != "127.0.0.1" {
		t.Fatalf("canonical addresses = %v, %v", addresses, err)
	}
}

func TestInvalidBranchIsRejectedBeforeResolution(t *testing.T) {
	for _, branch := range []string{"-main", ".hidden", "topic.lock", "topic.", "@", "../main", "refs//main", "bad branch"} {
		t.Run(branch, func(t *testing.T) {
			called := false
			validator := mustValidator(t, ValidatorOptions{Resolver: func(context.Context, string, int) ([]string, error) {
				called = true
				return []string{"8.8.8.8"}, nil
			}})
			_, err := validator.ValidateBranch(context.Background(), "https://git.example/org/repo.git", branch, nil)
			assertRemoteCode(t, err, RemoteInvalidResponse)
			if called {
				t.Fatal("resolver ran for an invalid branch")
			}
		})
	}
}

func TestValidatorFailsClosedWithoutCurloptResolve(t *testing.T) {
	_, err := NewValidator(ValidatorOptions{CapabilityProbe: func(context.Context) error {
		return errors.New("unsupported")
	}})
	if err == nil || err.Error() != "Git must support http.curloptResolve" {
		t.Fatalf("capability error = %v", err)
	}

	directory := t.TempDir()
	gitPath := filepath.Join(directory, "git")
	t.Setenv("PATH", directory)
	writeExecutable(t, gitPath, "#!/bin/sh\nprintf '%s\\n' 'http.followRedirects'\n")
	if err = requireCurloptResolve(t.Context()); err == nil {
		t.Fatal("Git without http.curloptResolve was accepted")
	}
	writeExecutable(t, gitPath, "#!/bin/sh\nprintf '%s\\n' 'http.curloptResolve'\n")
	if err = requireCurloptResolve(t.Context()); err != nil {
		t.Fatalf("supported Git rejected: %v", err)
	}
}

func TestTransportPinsDNSClearsAmbientGitStateAndCleansAskpass(t *testing.T) {
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte("test CA"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, err := NewCredential("operator", "repository-secret-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(credential.String(), "repository-secret-sentinel") {
		t.Fatal("credential string contains secret")
	}
	t.Setenv("HTTP_PROXY", "http://proxy.invalid")
	t.Setenv("GIT_CONFIG_SYSTEM", "/tmp/hostile-config")
	t.Setenv("GIT_SSH_COMMAND", "hostile-helper")
	validator := mustValidator(t, ValidatorOptions{
		Policy: NetworkPolicy{AllowPrivateAddresses: true},
		Resolver: func(_ context.Context, host string, port int) ([]string, error) {
			if host != "git.test" || port != 8443 {
				t.Fatalf("resolution target = %s:%d", host, port)
			}
			return []string{"127.0.0.1", "127.0.0.2"}, nil
		},
		CAFile: caFile,
	})
	var temporaryDirectory string
	err = validator.withTransport(context.Background(), "https://git.test:8443/org/repo.git", credential, func(transport gitTransport) error {
		temporaryDirectory = transport.cwd
		environment := environmentMap(transport.environment)
		for _, forbidden := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "HOME", "GIT_CONFIG_SYSTEM", "GIT_SSH_COMMAND"} {
			if _, exists := environment[forbidden]; exists {
				t.Fatalf("inherited forbidden environment %s", forbidden)
			}
		}
		for key, value := range map[string]string{
			"GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_NOSYSTEM": "1",
			"GIT_TERMINAL_PROMPT": "0", "GCM_INTERACTIVE": "never",
			"REF0_GIT_USERNAME": "operator", "REF0_GIT_SECRET": "repository-secret-sentinel",
			"GIT_SSL_CAINFO": caFile,
		} {
			if environment[key] != value {
				t.Fatalf("%s = %q", key, environment[key])
			}
		}
		askpass := environment["GIT_ASKPASS"]
		info, err := os.Stat(askpass)
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("askpass mode = %v, %v", info, err)
		}
		contents, err := os.ReadFile(askpass)
		if err != nil || strings.Contains(string(contents), "repository-secret-sentinel") {
			t.Fatalf("askpass contains secret or could not be read: %v", err)
		}
		command := exec.Command(askpass, "Password for repository")
		command.Env = append([]string(nil), transport.environment...)
		output, err := command.Output()
		if err != nil || string(output) != "repository-secret-sentinel\n" {
			t.Fatalf("askpass output = %q, %v", output, err)
		}
		wantResolve := "http.curloptResolve=git.test:8443:127.0.0.1"
		if !slices.Contains(transport.configuration, "http.followRedirects=false") || !slices.Contains(transport.configuration, "credential.helper=") || !slices.Contains(transport.configuration, wantResolve) {
			t.Fatalf("transport configuration = %#v", transport.configuration)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(temporaryDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary credential directory remains: %v", err)
	}
}

func TestValidateBranchUsesBoundedSanitizedGitExecution(t *testing.T) {
	directory := t.TempDir()
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+originalPath)
	gitPath := filepath.Join(directory, "git")
	validator := mustValidator(t, ValidatorOptions{
		Resolver: func(context.Context, string, int) ([]string, error) { return []string{"8.8.8.8"}, nil },
		Timeout:  100 * time.Millisecond,
	})

	writeExecutable(t, gitPath, "#!/bin/sh\nprintf '%s\\t%s\\n' '0123456789abcdef0123456789abcdef01234567' 'refs/heads/main'\n")
	evidence, err := validator.ValidateBranch(context.Background(), "https://git.example/org/repo.git", "main", nil)
	if err != nil || evidence.Commit != "0123456789abcdef0123456789abcdef01234567" || !evidence.TLSVerified || !evidence.AuthenticationSucceeded {
		t.Fatalf("evidence = %#v, %v", evidence, err)
	}

	credential, err := NewCredential("operator", "authentication-secret-sentinel")
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, gitPath, "#!/bin/sh\nprintf '%s\\n' 'fatal: Authentication failed for secret remote' >&2\nexit 1\n")
	_, err = validator.ValidateBranch(context.Background(), "https://git.example/org/repo.git", "main", credential)
	assertRemoteCode(t, err, RemoteAuthentication)
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("authentication error exposed command diagnostics: %v", err)
	}

	writeExecutable(t, gitPath, "#!/bin/sh\n/usr/bin/head -c 262145 /dev/zero\n")
	_, err = validator.ValidateBranch(context.Background(), "https://git.example/org/repo.git", "main", nil)
	assertRemoteCode(t, err, RemoteOutputTooLarge)

	writeExecutable(t, gitPath, "#!/bin/sh\n/bin/sleep 2\n")
	_, err = validator.ValidateBranch(context.Background(), "https://git.example/org/repo.git", "main", nil)
	assertRemoteCode(t, err, RemoteTimeout)
}

func TestValidatorUsesPinnedDNSVerifiedTLSAndEphemeralAuthentication(t *testing.T) {
	fixture := newLocalGitFixture(t)
	testGit(t, "--git-dir", fixture.remote, "update-server-info")
	certificate, caPEM := gitTestCertificate(t)
	var sniMu sync.Mutex
	var sniNames []string
	files := http.FileServer(http.Dir(filepath.Dir(fixture.remote)))
	handler := http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		sniMu.Lock()
		sniNames = append(sniNames, request.TLS.ServerName)
		sniMu.Unlock()
		username, password, ok := request.BasicAuth()
		if !ok || username != "testuser" || password != "testpass" {
			response.Header().Set("WWW-Authenticate", `Basic realm="git"`)
			response.WriteHeader(http.StatusUnauthorized)
			return
		}
		files.ServeHTTP(response, request)
	})
	server := httptest.NewUnstartedServer(handler)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	t.Cleanup(server.Close)
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	remoteURL := "https://git.test:" + portText + "/remote.git"
	validator := mustValidator(t, ValidatorOptions{
		Policy: NetworkPolicy{AllowPrivateAddresses: true},
		Resolver: func(_ context.Context, host string, port int) ([]string, error) {
			if host != "git.test" || strconv.Itoa(port) != portText {
				t.Fatalf("resolution target = %s:%d", host, port)
			}
			return []string{"127.0.0.1"}, nil
		},
		CAFile: caFile,
	})
	_, err = validator.ValidateBranch(context.Background(), remoteURL, "main", nil)
	assertRemoteCode(t, err, RemoteAuthentication)
	credential, err := NewCredential("testuser", "testpass")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := validator.ValidateBranch(context.Background(), remoteURL, "main", credential)
	if err != nil || evidence.Commit != fixture.commit || !evidence.TLSVerified || !evidence.AuthenticationSucceeded {
		t.Fatalf("branch evidence = %#v, %v", evidence, err)
	}
	commitEvidence, err := validator.ValidateCommit(context.Background(), remoteURL, strings.ToUpper(fixture.commit), credential)
	if err != nil || commitEvidence.Commit != fixture.commit {
		t.Fatalf("commit evidence = %#v, %v", commitEvidence, err)
	}
	store := newSourceStore(t)
	acquirer, err := NewAcquirer(store, validator, DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	branch, _ := NewBranchReference("main")
	snapshot, err := acquirer.Materialize(context.Background(), MaterializeRequest{
		SourceID: sourcefiles.ID{0: 81}, RevisionID: sourcefiles.ID{0: 82},
		RemoteURL: remoteURL, SelectedRef: branch, Credential: credential,
	})
	if err != nil || snapshot.Commit != fixture.commit {
		t.Fatalf("HTTPS snapshot = %#v, %v", snapshot, err)
	}
	mirror, err := store.MirrorPath(sourcefiles.ID{0: 81})
	if err != nil {
		t.Fatal(err)
	}
	assertTreeOmits(t, mirror, []byte("testpass"), []byte("testuser"), []byte(remoteURL), []byte(strings.ToLower(remoteURL)))
	sniMu.Lock()
	defer sniMu.Unlock()
	if len(sniNames) == 0 {
		t.Fatal("server received no TLS requests")
	}
	for _, name := range sniNames {
		if name != "git.test" {
			t.Fatalf("TLS SNI = %q", name)
		}
	}
}

func assertTreeOmits(t *testing.T, root string, forbidden ...[]byte) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			value, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, secret := range forbidden {
				if bytes.Contains(value, secret) {
					t.Fatalf("%s contains forbidden transport data", path)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGitCommandAllowlistRejectsUnexpectedSubcommand(t *testing.T) {
	_, err := executeGit(context.Background(), []string{"status"}, baseGitEnvironment(), t.TempDir(), 1024, time.Second, nil)
	if !errors.Is(err, errCommandDenied) {
		t.Fatalf("unexpected command error = %v", err)
	}
}

func gitTestCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	now := time.Now()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sourcegit test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "git.test"},
		DNSNames: []string{"git.test"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{serverDER, caDER}, PrivateKey: serverKey}
	return certificate, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
}

func mustValidator(t *testing.T, options ValidatorOptions) *Validator {
	t.Helper()
	validator, err := NewValidator(options)
	if err != nil {
		t.Fatal(err)
	}
	return validator
}

func assertRemoteCode(t *testing.T, err error, code RemoteFailureCode) {
	t.Helper()
	var remoteErr *RemoteError
	if !errors.As(err, &remoteErr) || remoteErr.Code != code {
		t.Fatalf("error = %v, want remote code %s", err, code)
	}
}

func environmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, value := range environment {
		parts := strings.SplitN(value, "=", 2)
		result[parts[0]] = parts[1]
	}
	return result
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
