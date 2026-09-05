package sources

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/cyr1en/ref0/internal/sourcegit"
	"golang.org/x/net/idna"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

type ID = jobs.UUID

func ParseID(raw string) (ID, error) {
	return jobs.ParseUUID(raw)
}

func NewID() (ID, error) {
	var id ID
	if _, err := rand.Read(id[:]); err != nil {
		return ID{}, err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}

type Kind string

const (
	Repository Kind = "REPOSITORY"
	Website    Kind = "WEBSITE"
)

type Privacy string

const (
	Public  Privacy = "PUBLIC"
	Private Privacy = "PRIVATE"
)

type Lifecycle string

const (
	Draft    Lifecycle = "DRAFT"
	Active   Lifecycle = "ACTIVE"
	Disabled Lifecycle = "DISABLED"
	Removed  Lifecycle = "REMOVED"
)

type Health string

const (
	Unknown   Health = "UNKNOWN"
	Healthy   Health = "HEALTHY"
	Unhealthy Health = "UNHEALTHY"
)

type RefKind string

const (
	Branch RefKind = "BRANCH"
	Commit RefKind = "COMMIT"
	Root   RefKind = "ROOT"
)

type AcquisitionMode string

const (
	BuiltinCrawl  AcquisitionMode = "BUILTIN_CRAWL"
	TinyFishCrawl AcquisitionMode = "TINYFISH_CRAWL"
	DirectJSONAPI AcquisitionMode = "DIRECT_JSON_API"
)

type SyncKind string

const (
	Validation      SyncKind = "VALIDATION"
	Synchronization SyncKind = "SYNC"
)

type SyncStatus string

const (
	SyncPending    SyncStatus = "PENDING"
	SyncRunning    SyncStatus = "RUNNING"
	SyncSucceeded  SyncStatus = "SUCCEEDED"
	SyncFailed     SyncStatus = "FAILED"
	SyncSuperseded SyncStatus = "SUPERSEDED"
)

var (
	ErrNotFound           = errors.New("source resource does not exist")
	ErrConflict           = errors.New("source operation conflicts with current state")
	ErrTransition         = errors.New("source lifecycle transition is invalid")
	commitPattern         = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	websiteVersionPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	headerPattern         = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,126}$`)
	pythonIDNA            = idna.New(idna.MapForLookup(), idna.Transitional(true), idna.StrictDomainName(true))
)

type ConflictError struct{ Message string }

func (err *ConflictError) Error() string        { return err.Message }
func (err *ConflictError) Is(target error) bool { return target == ErrConflict }

func conflict(message string) error { return &ConflictError{Message: message} }

type Name struct {
	Display string
	Key     string
}

func ParseName(raw string) (Name, error) {
	display := trimPythonWhitespace(norm.NFKC.String(raw))
	if display == "" || utf8.RuneCountInString(display) > 255 {
		return Name{}, errors.New("source name must contain 1 to 255 characters")
	}
	key := cases.Fold().String(display)
	if utf8.RuneCountInString(key) > 255 {
		return Name{}, errors.New("normalized source name must not exceed 255 characters")
	}
	return Name{Display: display, Key: key}, nil
}

type Remote struct {
	URL  string
	Host string
}

func ParseRepositoryRemote(raw string) (Remote, error) {
	if raw == "" || raw != trimPythonWhitespace(raw) || utf8.RuneCountInString(raw) > 2048 || hasControl(raw) {
		return Remote{}, errors.New("repository remote is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(parsed.Path, `\`) || strings.Contains(rawPath(parsed), "%") {
		return Remote{}, errors.New("repository remote is invalid")
	}
	host, renderedHost, err := normalizeRemoteHost(parsed.Hostname())
	if err != nil {
		return Remote{}, errors.New("repository remote is invalid")
	}
	port, err := normalizedPort(parsed, renderedHost)
	if err != nil {
		return Remote{}, errors.New("repository remote is invalid")
	}
	remotePath := strings.TrimRight(parsed.Path, "/")
	segments := strings.Split(strings.TrimPrefix(remotePath, "/"), "/")
	if !strings.HasPrefix(remotePath, "/") || remotePath == "" || slices.ContainsFunc(segments, func(segment string) bool { return segment == "" || segment == "." || segment == ".." }) {
		return Remote{}, errors.New("repository remote is invalid")
	}
	normalized := "https://" + renderedHost + port + remotePath
	remote, err := sourcegit.NormalizeRepositoryURL(normalized)
	if err != nil {
		return Remote{}, errors.New("repository remote is invalid")
	}
	return Remote{URL: remote.URL, Host: host}, nil
}

func ParseWebsiteRemote(raw string) (Remote, error) {
	if raw == "" || raw != trimPythonWhitespace(raw) || utf8.RuneCountInString(raw) > 2048 || hasControl(raw) {
		return Remote{}, errors.New("website URL is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || strings.Contains(parsed.Path, `\`) {
		return Remote{}, errors.New("website URL must be HTTPS without userinfo, query, or fragment")
	}
	host, renderedHost, err := normalizeRemoteHost(parsed.Hostname())
	if err != nil {
		return Remote{}, errors.New("website host is invalid")
	}
	port, err := normalizedPort(parsed, renderedHost)
	if err != nil {
		return Remote{}, errors.New("website URL port is invalid")
	}
	cleaned := path.Clean(rawPath(parsed))
	if cleaned == "." {
		cleaned = "/"
	}
	if !strings.HasPrefix(cleaned, "/") {
		return Remote{}, errors.New("website path is invalid")
	}
	if strings.HasSuffix(rawPath(parsed), "/") && cleaned != "/" {
		cleaned += "/"
	}
	return Remote{URL: "https://" + renderedHost + port + cleaned, Host: host}, nil
}

func rawPath(parsed *url.URL) string {
	if parsed.RawPath != "" {
		return parsed.RawPath
	}
	return parsed.Path
}

func normalizeRemoteHost(raw string) (string, string, error) {
	host := strings.ToLower(raw)
	if address := net.ParseIP(host); address != nil {
		rendered := host
		if strings.Contains(host, ":") {
			rendered = "[" + host + "]"
		}
		return host, rendered, nil
	}
	host, err := pythonIDNA.ToASCII(host)
	if err != nil {
		return "", "", err
	}
	host = strings.ToLower(host)
	if !validHost(host) {
		return "", "", errors.New("host is invalid")
	}
	return host, host, nil
}

func normalizedPort(parsed *url.URL, renderedHost string) (string, error) {
	port := parsed.Port()
	if port == "" {
		if parsed.Host != renderedHost && parsed.Host != "["+strings.Trim(renderedHost, "[]")+"]" && strings.LastIndex(parsed.Host, ":") >= 0 && !strings.Contains(parsed.Host, "]") {
			return "", errors.New("port is invalid")
		}
		return "", nil
	}
	value, err := strconv.Atoi(port)
	if err != nil || value < 1 || value > 65535 {
		return "", errors.New("port is invalid")
	}
	if value == 443 {
		return "", nil
	}
	return ":" + port, nil
}

func validHost(host string) bool {
	if host == "" || strings.HasSuffix(host, ".") || len(host) > 253 || strings.IndexFunc(host, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
		return false
	}
	if net.ParseIP(host) != nil {
		return true
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
				return false
			}
		}
	}
	return true
}

type Reference struct {
	Kind  RefKind
	Value string
}

func ParseReference(kind RefKind, raw string) (Reference, error) {
	if raw != trimPythonWhitespace(raw) {
		return Reference{}, errors.New("repository reference is invalid")
	}
	var (
		value sourcegit.Reference
		err   error
	)
	switch kind {
	case Branch:
		value, err = sourcegit.NewBranchReference(raw)
	case Commit:
		value, err = sourcegit.NewCommitReference(raw)
	default:
		err = errors.New("repository reference kind is invalid")
	}
	if err != nil {
		return Reference{}, errors.New("repository reference is invalid")
	}
	return Reference{Kind: kind, Value: value.Value()}, nil
}

func (reference Reference) gitReference() (sourcegit.Reference, error) {
	return func() (sourcegit.Reference, error) {
		switch reference.Kind {
		case Branch:
			return sourcegit.NewBranchReference(reference.Value)
		case Commit:
			return sourcegit.NewCommitReference(reference.Value)
		default:
			return sourcegit.Reference{}, errors.New("repository reference kind is invalid")
		}
	}()
}

type CrawlLimits struct {
	Concurrency       int
	RequestsPerSecond int
	MaxPages          int
	MaxPageBytes      int
	MaxTotalBytes     int64
	MaxDepth          int
}

func DefaultCrawlLimits() CrawlLimits {
	return CrawlLimits{Concurrency: 4, RequestsPerSecond: 4, MaxPages: 500, MaxPageBytes: 2 << 20, MaxTotalBytes: 100 << 20, MaxDepth: 3}
}

func (limits CrawlLimits) validate() error {
	if limits.Concurrency < 1 || limits.Concurrency > 16 {
		return errors.New("website concurrency must be between 1 and 16")
	}
	if limits.RequestsPerSecond < 1 || limits.RequestsPerSecond > 100 {
		return errors.New("website request rate must be between 1 and 100 per second")
	}
	if limits.MaxPages < 1 || limits.MaxPages > 10_000 {
		return errors.New("website page limit must be between 1 and 10000")
	}
	if limits.MaxPageBytes < 1_024 || limits.MaxPageBytes > 10<<20 {
		return errors.New("website page byte limit is invalid")
	}
	if limits.MaxTotalBytes < int64(limits.MaxPageBytes) || limits.MaxTotalBytes > 1<<30 {
		return errors.New("website total byte limit is invalid")
	}
	if limits.MaxDepth < 0 || limits.MaxDepth > 10 {
		return errors.New("website crawl depth must be between 0 and 10")
	}
	return nil
}

type RepositoryConfiguration struct {
	Name                Name
	Privacy             Privacy
	Remote              Remote
	Reference           Reference
	CredentialUsername  *string
	CredentialID        *ID
	IncludePatterns     []string
	ExcludePatterns     []string
	PollIntervalSeconds *int
}

func (config RepositoryConfiguration) normalize() (RepositoryConfiguration, error) {
	name, err := ParseName(config.Name.Display)
	if err != nil {
		return config, err
	}
	remote, err := ParseRepositoryRemote(config.Remote.URL)
	if err != nil {
		return config, err
	}
	reference, err := ParseReference(config.Reference.Kind, config.Reference.Value)
	if err != nil {
		return config, err
	}
	if err := credentialUsername(config.CredentialUsername); err != nil {
		return config, err
	}
	if (config.CredentialUsername == nil) != (config.CredentialID == nil) {
		return config, errors.New("repository credential username and credential must be paired")
	}
	if config.Privacy == Private && config.CredentialID == nil {
		return config, errors.New("private repositories require a credential")
	}
	if config.Privacy == Public && config.CredentialID != nil {
		return config, errors.New("public repositories cannot retain a credential")
	}
	if config.Privacy != Public && config.Privacy != Private {
		return config, errors.New("source privacy is invalid")
	}
	include, err := normalizePatterns(config.IncludePatterns)
	if err != nil {
		return config, err
	}
	exclude, err := normalizePatterns(config.ExcludePatterns)
	if err != nil {
		return config, err
	}
	if err := validatePoll(config.PollIntervalSeconds); err != nil {
		return config, err
	}
	config.Name, config.Remote, config.Reference = name, remote, reference
	config.IncludePatterns, config.ExcludePatterns = include, exclude
	config.CredentialUsername = cloneString(config.CredentialUsername)
	config.CredentialID = cloneID(config.CredentialID)
	config.PollIntervalSeconds = cloneInt(config.PollIntervalSeconds)
	return config, nil
}

func NormalizeRepositoryConfiguration(config RepositoryConfiguration) (RepositoryConfiguration, error) {
	return config.normalize()
}

type WebsiteConfiguration struct {
	Name                 Name
	Privacy              Privacy
	Remote               Remote
	CredentialHeader     *string
	CredentialPrefix     *string
	CredentialID         *ID
	Limits               CrawlLimits
	PollIntervalSeconds  *int
	AcquisitionMode      AcquisitionMode
	TinyFishCredentialID *ID
}

func (config WebsiteConfiguration) normalize() (WebsiteConfiguration, error) {
	name, err := ParseName(config.Name.Display)
	if err != nil {
		return config, err
	}
	remote, err := ParseWebsiteRemote(config.Remote.URL)
	if err != nil {
		return config, err
	}
	if config.Limits == (CrawlLimits{}) {
		config.Limits = DefaultCrawlLimits()
	}
	if err := config.Limits.validate(); err != nil {
		return config, err
	}
	if config.AcquisitionMode == "" {
		config.AcquisitionMode = BuiltinCrawl
	}
	if err := credentialHeader(config.CredentialHeader); err != nil {
		return config, err
	}
	if (config.CredentialHeader == nil) != (config.CredentialID == nil) {
		return config, errors.New("website credential header and credential must be paired")
	}
	if config.CredentialPrefix != nil && (config.CredentialHeader == nil || utf8.RuneCountInString(*config.CredentialPrefix) > 128 || strings.ContainsAny(*config.CredentialPrefix, "\r\n")) {
		return config, errors.New("website credential prefix is invalid")
	}
	if config.Privacy == Private && config.CredentialID == nil {
		return config, errors.New("private websites require a credential")
	}
	if config.Privacy == Public && config.CredentialID != nil {
		return config, errors.New("public websites cannot retain a credential")
	}
	if config.Privacy != Public && config.Privacy != Private {
		return config, errors.New("source privacy is invalid")
	}
	if config.TinyFishCredentialID != nil && config.AcquisitionMode != TinyFishCrawl {
		return config, errors.New("tinyfish credential requires tinyfish acquisition")
	}
	if config.AcquisitionMode == TinyFishCrawl {
		if config.Privacy != Public {
			return config, errors.New("tinyfish acquisition requires a public website")
		}
		if config.TinyFishCredentialID == nil {
			return config, errors.New("tinyfish acquisition requires a tinyfish credential")
		}
		if config.CredentialID != nil {
			return config, errors.New("tinyfish acquisition cannot retain a website credential")
		}
	} else if config.AcquisitionMode != BuiltinCrawl && config.AcquisitionMode != DirectJSONAPI {
		return config, errors.New("website acquisition mode is invalid")
	}
	if config.AcquisitionMode == DirectJSONAPI && (config.Limits.MaxPages != 1 || config.Limits.MaxDepth != 0) {
		return config, errors.New("direct JSON API acquisition requires max_pages 1 and max_depth 0")
	}
	if err := validatePoll(config.PollIntervalSeconds); err != nil {
		return config, err
	}
	config.Name, config.Remote = name, remote
	config.CredentialHeader, config.CredentialPrefix = cloneString(config.CredentialHeader), cloneString(config.CredentialPrefix)
	config.CredentialID, config.TinyFishCredentialID = cloneID(config.CredentialID), cloneID(config.TinyFishCredentialID)
	config.PollIntervalSeconds = cloneInt(config.PollIntervalSeconds)
	return config, nil
}

func NormalizeWebsiteConfiguration(config WebsiteConfiguration) (WebsiteConfiguration, error) {
	return config.normalize()
}

type Source struct {
	ID                            ID
	KnowledgeBaseID               ID
	Kind                          Kind
	Name                          string
	Privacy                       Privacy
	Lifecycle                     Lifecycle
	Health                        Health
	SanitizedError                *string
	CheckedAt                     *time.Time
	CurrentRevisionID             *ID
	Version                       int
	ConfigurationVersion          int
	ValidatedConfigurationVersion *int
	CreatedAt                     time.Time
	UpdatedAt                     time.Time
	DisabledAt                    *time.Time
	RemovedAt                     *time.Time
	Repository                    *RepositoryConfiguration
	Website                       *WebsiteConfiguration
}

type CapturedRepository struct {
	Privacy            Privacy
	Remote             Remote
	Reference          Reference
	CredentialUsername *string
	CredentialID       *ID
	CredentialVersion  *int
	IncludePatterns    []string
	ExcludePatterns    []string
}

type CapturedWebsite struct {
	Privacy                   Privacy
	Remote                    Remote
	CredentialHeader          *string
	CredentialPrefix          *string
	CredentialID              *ID
	CredentialVersion         *int
	Limits                    CrawlLimits
	AcquisitionMode           AcquisitionMode
	TinyFishCredentialID      *ID
	TinyFishCredentialVersion *int
	PreviousRevisionID        *ID
}

type Sync struct {
	ID                           ID
	SourceID                     ID
	JobID                        jobs.JobID
	Kind                         SyncKind
	RequestedBy                  *ID
	CapturedSourceVersion        int
	CapturedConfigurationVersion int
	Repository                   *CapturedRepository
	Website                      *CapturedWebsite
	CandidateRevisionID          *ID
	Status                       SyncStatus
	ResultRevisionID             *ID
	ResolvedNativeVersion        *string
	SanitizedError               *string
	CreatedAt                    time.Time
	StartedAt                    *time.Time
	CompletedAt                  *time.Time
}

type PageCapture struct {
	CanonicalURL         string
	ContentPath          string
	ContentSHA256        [sha256.Size]byte
	EvidenceURI          string
	Freshness            string
	ETag                 *string
	LastModified         *string
	ReusedFromRevisionID *ID
}

type Revision struct {
	ID            ID
	SourceID      ID
	ObservedRef   Reference
	NativeVersion string
	Fingerprint   [sha256.Size]byte
	ArtifactKey   string
	FileCount     int
	ByteCount     int64
	IgnoredPaths  []string
	CreatedAt     time.Time
	WebsitePages  []PageCapture
}

type RevisionCandidate struct {
	NativeVersion string
	Fingerprint   [sha256.Size]byte
	ArtifactKey   string
	FileCount     int
	ByteCount     int64
	IgnoredPaths  []string
	WebsitePages  []PageCapture
}

type Created struct {
	Source     Source
	Validation Sync
}

type ValidationCompletion struct {
	SyncID                ID
	ResolvedNativeVersion *string
	SanitizedError        *string
	Retryable             bool
}
type SyncCompletion struct {
	SyncID         ID
	Revision       *RevisionCandidate
	SanitizedError *string
	Retryable      bool
}

func ArtifactKey(sourceID, revisionID ID) string {
	return sourcefiles.SnapshotArtifactKey(sourcefiles.ID(sourceID), sourcefiles.ID(revisionID))
}

func Transition(current, target Lifecycle) (Lifecycle, error) {
	allowed := map[Lifecycle][]Lifecycle{
		Draft: {Disabled, Removed}, Active: {Disabled, Removed}, Disabled: {Active, Removed}, Removed: {},
	}
	if !slices.Contains(allowed[current], target) {
		return "", ErrTransition
	}
	return target, nil
}

func validateCandidate(kind Kind, candidate RevisionCandidate) error {
	if candidate.FileCount < 0 || candidate.ByteCount < 0 {
		return errors.New("revision counts must be nonnegative")
	}
	if kind == Repository && !commitPattern.MatchString(strings.ToLower(candidate.NativeVersion)) {
		return errors.New("commit must be a 40 or 64 character hash")
	}
	if kind == Website && !websiteVersionPattern.MatchString(strings.ToLower(candidate.NativeVersion)) {
		return errors.New("website version must be a 64 character hash")
	}
	if _, err := normalizeSourcePaths(candidate.IgnoredPaths); err != nil {
		return err
	}
	if kind == Repository && len(candidate.WebsitePages) != 0 {
		return errors.New("repository revision cannot contain website pages")
	}
	if len(candidate.WebsitePages) > 10_000 {
		return errors.New("website revision pages are invalid")
	}
	paths := make(map[string]struct{}, len(candidate.WebsitePages))
	for _, page := range candidate.WebsitePages {
		if _, duplicate := paths[page.ContentPath]; duplicate {
			return errors.New("website revision pages are invalid")
		}
		paths[page.ContentPath] = struct{}{}
	}
	return nil
}

func validateOutcome(success bool, sanitized *string, retryable bool) error {
	if success == (sanitized != nil) {
		return errors.New("source result must contain either success evidence or an error")
	}
	if sanitized != nil && (*sanitized == "" || utf8.RuneCountInString(*sanitized) > 1000 || *sanitized != trimPythonWhitespace(*sanitized) || strings.ContainsAny(*sanitized, "\r\n\x00")) {
		return errors.New("sanitized source error is invalid")
	}
	if retryable && sanitized == nil {
		return errors.New("successful source result cannot be retryable")
	}
	return nil
}

func normalizePatterns(values []string) ([]string, error) {
	if len(values) > 100 {
		return nil, errors.New("repository patterns must contain at most 100 entries")
	}
	result := make([]string, 0, len(values))
	total := 0
	for _, raw := range values {
		value := trimPythonWhitespace(raw)
		if value == "" || utf8.RuneCountInString(value) > 4096 || strings.ContainsAny(value, `\[]`) || hasControl(value) {
			return nil, errors.New("repository pattern is invalid")
		}
		value = strings.TrimPrefix(value, "/")
		directory := strings.HasSuffix(value, "/")
		value = strings.TrimSuffix(value, "/")
		if value == "" {
			return nil, errors.New("repository pattern is invalid")
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == "" || segment == "." || segment == ".." || strings.Contains(segment, "**") && segment != "**" {
				return nil, errors.New("repository pattern is invalid")
			}
		}
		if directory {
			value += "/"
		}
		total += len([]byte(value))
		result = append(result, value)
	}
	if total > 65_536 {
		return nil, errors.New("repository patterns exceed the size limit")
	}
	return result, nil
}

func normalizeSourcePaths(values []string) ([]string, error) {
	if len(values) > 1_000 {
		return nil, errors.New("ignored paths must contain at most 1000 entries")
	}
	result := slices.Clone(values)
	total := 0
	for _, value := range result {
		total += len([]byte(value))
		if value == "" || len([]byte(value)) > 4096 || strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, `\`) || hasControl(value) {
			return nil, errors.New("ignored source path is invalid")
		}
		for _, segment := range strings.Split(value, "/") {
			if segment == "" || segment == "." || segment == ".." {
				return nil, errors.New("ignored source path is invalid")
			}
		}
	}
	if total > 1<<20 {
		return nil, errors.New("ignored source paths exceed the size limit")
	}
	return result, nil
}

func credentialUsername(value *string) error {
	if value == nil {
		return nil
	}
	if *value == "" || *value != trimPythonWhitespace(*value) || utf8.RuneCountInString(*value) > 255 {
		return errors.New("repository credential username is invalid")
	}
	for _, character := range *value {
		if character < 33 || character == 127 {
			return errors.New("repository credential username is invalid")
		}
	}
	return nil
}

func credentialHeader(value *string) error {
	if value == nil {
		return nil
	}
	lower := strings.ToLower(*value)
	forbidden := map[string]struct{}{"connection": {}, "cookie": {}, "forwarded": {}, "host": {}, "proxy-authorization": {}, "te": {}, "trailer": {}, "transfer-encoding": {}, "upgrade": {}, "via": {}}
	_, denied := forbidden[lower]
	if !headerPattern.MatchString(*value) || denied || strings.HasPrefix(lower, "proxy-") || strings.HasPrefix(lower, "sec-") || strings.HasPrefix(lower, "x-forwarded-") {
		return errors.New("website credential header is forbidden")
	}
	return nil
}

func validatePoll(value *int) error {
	if value != nil && (*value < 60 || *value > 604_800) {
		return errors.New("poll interval must be between 60 and 604800 seconds")
	}
	return nil
}
func hasControl(value string) bool {
	return strings.IndexFunc(value, func(r rune) bool { return r < 32 || r == 127 }) >= 0
}
func trimPythonWhitespace(value string) string {
	return strings.TrimFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || character >= '\x1c' && character <= '\x1f'
	})
}
func cloneID(value *ID) *ID {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
