package providers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type scanner interface{ Scan(...any) error }

func endpointQuery(lock bool) string {
	query := `
		SELECT id, display_name, display_key, base_url, credential_id, headers,
		       chat_completions_path, responses_path, models_path, allow_http,
		       allow_private_network, lifecycle, version, configuration_version,
		       created_at, updated_at, archived_at, health, health_checked_at
		FROM provider_endpoints WHERE id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	return query
}

func scanEndpoint(row scanner) (Endpoint, error) {
	var value Endpoint
	var id, credentialID pgtype.UUID
	var headers []byte
	var responsesPath pgtype.Text
	var archivedAt, healthCheckedAt pgtype.Timestamptz
	err := row.Scan(
		&id, &value.Configuration.DisplayName, &value.Configuration.DisplayKey,
		&value.Configuration.BaseURL, &credentialID, &headers,
		&value.Configuration.ChatCompletionsPath, &responsesPath,
		&value.Configuration.ModelsPath, &value.Configuration.AllowHTTP,
		&value.Configuration.AllowPrivateNetwork, &value.Lifecycle, &value.Version,
		&value.ConfigurationVersion, &value.CreatedAt, &value.UpdatedAt, &archivedAt,
		&value.Health, &healthCheckedAt,
	)
	if err != nil {
		return Endpoint{}, err
	}
	value.ID = EndpointID(id.Bytes)
	if credentialID.Valid {
		credential := credentials.ID(credentialID.Bytes)
		value.Configuration.CredentialID = &credential
	}
	if responsesPath.Valid {
		path := responsesPath.String
		value.Configuration.ResponsesPath = &path
	}
	if err := json.Unmarshal(headers, &value.Configuration.Headers); err != nil {
		return Endpoint{}, errors.New("stored provider headers are invalid")
	}
	value.ArchivedAt = timeFromPG(archivedAt)
	value.HealthCheckedAt = timeFromPG(healthCheckedAt)
	if err := validateEndpointState(value); err != nil {
		return Endpoint{}, err
	}
	return value, nil
}

func profileQuery(lock bool) string {
	query := `
		SELECT id, endpoint_id, model_id, availability, current_version_id,
		       version, created_at, updated_at
		FROM model_profiles WHERE id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	return query
}

type profileRow struct {
	ID               ProfileID
	EndpointID       EndpointID
	ModelID          string
	Availability     Availability
	CurrentVersionID ProfileVersionID
	Version          int32
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func scanProfileRow(row scanner) (profileRow, error) {
	var value profileRow
	var id, endpointID, versionID pgtype.UUID
	err := row.Scan(&id, &endpointID, &value.ModelID, &value.Availability, &versionID,
		&value.Version, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return profileRow{}, err
	}
	value.ID = ProfileID(id.Bytes)
	value.EndpointID = EndpointID(endpointID.Bytes)
	value.CurrentVersionID = ProfileVersionID(versionID.Bytes)
	return value, nil
}

func scanVersion(row scanner) (ProfileVersion, error) {
	var value ProfileVersion
	var id, profileID, actorID pgtype.UUID
	var contextTokens, outputTokens pgtype.Int4
	var streaming, tools, structured, temperature pgtype.Bool
	var reasoning, extra, origins []byte
	err := row.Scan(
		&id, &profileID, &value.VersionNumber, &value.ConfigurationVersion,
		&value.Settings.Transport, &contextTokens, &outputTokens, &streaming,
		&tools, &structured, &temperature, &value.Settings.ReasoningTransport,
		&reasoning, &value.Settings.TimeoutSeconds, &value.Settings.MaxRetries,
		&value.Settings.MaxConcurrentTasks, &extra, &origins, &value.Source, &actorID, &value.CreatedAt,
	)
	if err != nil {
		return ProfileVersion{}, err
	}
	value.ID = ProfileVersionID(id.Bytes)
	value.ProfileID = ProfileID(profileID.Bytes)
	value.Settings.ContextWindowTokens = int32FromPG(contextTokens)
	value.Settings.MaxOutputTokens = int32FromPG(outputTokens)
	value.Settings.SupportsStreaming = boolFromPG(streaming)
	value.Settings.SupportsTools = boolFromPG(tools)
	value.Settings.SupportsStructuredOutput = boolFromPG(structured)
	value.Settings.SupportsTemperature = boolFromPG(temperature)
	if len(reasoning) != 0 && !bytes.Equal(reasoning, []byte("null")) {
		if err := json.Unmarshal(reasoning, &value.Settings.ReasoningMapping); err != nil {
			return ProfileVersion{}, errors.New("stored reasoning mapping is invalid")
		}
	}
	if err := json.Unmarshal(extra, &value.Settings.ExtraBody); err != nil {
		return ProfileVersion{}, errors.New("stored provider extra body is invalid")
	}
	if err := json.Unmarshal(origins, &value.Settings.MetadataOrigin); err != nil {
		return ProfileVersion{}, errors.New("stored provider metadata origin is invalid")
	}
	if actorID.Valid {
		actor := ActorID(actorID.Bytes)
		value.CreatedByActorID = &actor
	}
	settings, err := value.Settings.Normalize()
	if err != nil {
		return ProfileVersion{}, fmt.Errorf("stored model profile settings are invalid: %w", err)
	}
	value.Settings = settings
	return value, nil
}

const versionColumns = `
	id, profile_id, version_number, configuration_version, transport,
	context_window_tokens, max_output_tokens, supports_streaming, supports_tools,
	supports_structured_output, supports_temperature, reasoning_transport,
	reasoning_mapping, timeout_seconds, max_retries, max_concurrent_tasks,
	extra_body, metadata_origin,
	source, created_by_operator_id, created_at`

func getProfileTx(ctx context.Context, tx pgx.Tx, id ProfileID, lock bool) (Profile, error) {
	row, err := scanProfileRow(tx.QueryRow(ctx, profileQuery(lock), uuid(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, fmt.Errorf("%w: model profile not found", ErrNotFound)
	}
	if err != nil {
		return Profile{}, err
	}
	version, err := scanVersion(tx.QueryRow(ctx,
		"SELECT "+versionColumns+" FROM model_profile_versions WHERE id=$1", uuid(row.CurrentVersionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return Profile{}, fmt.Errorf("%w: model profile version not found", ErrNotFound)
	}
	if err != nil {
		return Profile{}, err
	}
	return Profile{
		ID: row.ID, EndpointID: row.EndpointID, ModelID: row.ModelID,
		Availability: row.Availability, CurrentVersion: version, Version: row.Version,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}, nil
}

func scanDiscovery(row scanner) (DiscoveryRun, error) {
	var value DiscoveryRun
	var id, endpointID, jobID, actorID pgtype.UUID
	var credentialVersion, httpStatus, modelCount pgtype.Int4
	var modelIDs, raw []byte
	var tlsVerified, authentication pgtype.Bool
	var digest []byte
	var sanitized pgtype.Text
	var started, completed pgtype.Timestamptz
	err := row.Scan(
		&id, &endpointID, &jobID, &value.CapturedConfigurationVersion,
		&credentialVersion, &value.TLSRequired, &actorID, &value.Status,
		&modelIDs, &raw, &tlsVerified, &authentication, &httpStatus, &digest,
		&modelCount, &sanitized, &value.CreatedAt, &started, &completed,
	)
	if err != nil {
		return DiscoveryRun{}, err
	}
	value.ID, value.EndpointID = DiscoveryRunID(id.Bytes), EndpointID(endpointID.Bytes)
	value.JobID, value.RequestedByActorID = jobs.JobID(jobID.Bytes), ActorID(actorID.Bytes)
	value.CapturedCredentialVersion = int32FromPG(credentialVersion)
	value.HTTPStatus, value.ModelCount = int32FromPG(httpStatus), int32FromPG(modelCount)
	value.TLSVerified, value.AuthenticationSucceeded = boolFromPG(tlsVerified), boolFromPG(authentication)
	value.ResponseSHA256 = append([]byte(nil), digest...)
	value.SanitizedError = stringFromPG(sanitized)
	value.StartedAt, value.CompletedAt = timeFromPG(started), timeFromPG(completed)
	if err := json.Unmarshal(modelIDs, &value.ModelIDs); err != nil {
		return DiscoveryRun{}, errors.New("stored discovery model IDs are invalid")
	}
	if len(raw) != 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &value.RawResponse); err != nil {
			return DiscoveryRun{}, errors.New("stored discovery response is invalid")
		}
	}
	return value, nil
}

const discoveryColumns = `
	id, endpoint_id, job_id, captured_configuration_version,
	captured_credential_version, tls_required, requested_by_operator_id, status,
	model_ids, raw_response, tls_verified, authentication_succeeded, http_status,
	response_sha256, model_count, sanitized_error, created_at, started_at, completed_at`

func scanProbe(row scanner) (ProbeRun, error) {
	var value ProbeRun
	var id, profileID, jobID, capturedVersionID, actorID, resultingID pgtype.UUID
	var credentialVersion pgtype.Int4
	var checks, findings, raw []byte
	var sanitized pgtype.Text
	var started, completed pgtype.Timestamptz
	err := row.Scan(
		&id, &profileID, &jobID, &value.CapturedConfigurationVersion,
		&credentialVersion, &capturedVersionID, &actorID, &checks,
		&value.AcknowledgeCost, &value.Status, &findings, &raw, &sanitized,
		&resultingID, &value.CreatedAt, &started, &completed,
	)
	if err != nil {
		return ProbeRun{}, err
	}
	value.ID, value.ProfileID = ProbeRunID(id.Bytes), ProfileID(profileID.Bytes)
	value.JobID, value.CapturedProfileVersionID = jobs.JobID(jobID.Bytes), ProfileVersionID(capturedVersionID.Bytes)
	value.RequestedByActorID = ActorID(actorID.Bytes)
	value.CapturedCredentialVersion = int32FromPG(credentialVersion)
	value.SanitizedError = stringFromPG(sanitized)
	value.StartedAt, value.CompletedAt = timeFromPG(started), timeFromPG(completed)
	if resultingID.Valid {
		result := ProfileVersionID(resultingID.Bytes)
		value.ResultingVersionID = &result
	}
	if err := json.Unmarshal(checks, &value.SelectedChecks); err != nil {
		return ProbeRun{}, errors.New("stored probe checks are invalid")
	}
	if len(findings) != 0 && !bytes.Equal(findings, []byte("null")) {
		value.Findings = &ProbeFindings{}
		if err := json.Unmarshal(findings, value.Findings); err != nil {
			return ProbeRun{}, errors.New("stored probe findings are invalid")
		}
	}
	if len(raw) != 0 && !bytes.Equal(raw, []byte("null")) {
		if err := json.Unmarshal(raw, &value.RawResponse); err != nil {
			return ProbeRun{}, errors.New("stored probe response is invalid")
		}
	}
	return value, nil
}

const probeColumns = `
	id, model_profile_id, job_id, captured_configuration_version,
	captured_credential_version, captured_profile_version_id,
	requested_by_operator_id, selected_checks, acknowledge_cost, status,
	findings, raw_response, sanitized_error, resulting_version_id,
	created_at, started_at, completed_at`

func scanAssignment(row scanner) (Assignment, error) {
	var value Assignment
	var id, knowledgeBaseID, profileID pgtype.UUID
	err := row.Scan(&id, &knowledgeBaseID, &value.Role, &profileID,
		&value.ReasoningEffort, &value.AnswerMode, &value.Version,
		&value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return Assignment{}, err
	}
	value.ID = AssignmentID(id.Bytes)
	value.KnowledgeBaseID = KnowledgeBaseID(knowledgeBaseID.Bytes)
	value.ProfileID = ProfileID(profileID.Bytes)
	return value, nil
}

const assignmentColumns = `
	id, knowledge_base_id, role, model_profile_id, reasoning_effort,
	answer_mode, version, created_at, updated_at`

func uuid[T ~[16]byte](value T) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte(value), Valid: true}
}

func nullableUUID[T ~[16]byte](value *T) pgtype.UUID {
	if value == nil {
		return pgtype.UUID{}
	}
	return uuid(*value)
}

func int32FromPG(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	result := value.Int32
	return &result
}

func boolFromPG(value pgtype.Bool) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Bool
	return &result
}

func stringFromPG(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func timeFromPG(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	result := value.Time
	return &result
}

func newUUID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return id, err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}

func pythonCanonicalJSON(value any) ([]byte, error) {
	var result strings.Builder
	if err := writePythonJSON(&result, reflect.ValueOf(value)); err != nil {
		return nil, err
	}
	return []byte(result.String()), nil
}

func writePythonJSON(result *strings.Builder, value reflect.Value) error {
	for value.IsValid() && (value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer) {
		if value.IsNil() {
			result.WriteString("null")
			return nil
		}
		value = value.Elem()
	}
	if !value.IsValid() {
		result.WriteString("null")
		return nil
	}

	if value.Type() == reflect.TypeOf(json.Number("")) {
		return writePythonJSONNumber(result, value.Interface().(json.Number))
	}

	switch value.Kind() {
	case reflect.Bool:
		result.WriteString(strconv.FormatBool(value.Bool()))
	case reflect.String:
		return writePythonJSONString(result, value.String())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		result.WriteString(strconv.FormatInt(value.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		result.WriteString(strconv.FormatUint(value.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		encoded, err := pythonFloat(value.Float(), value.Type().Bits())
		if err != nil {
			return err
		}
		result.WriteString(encoded)
	case reflect.Map:
		if value.Type().Key().Kind() != reflect.String {
			return errors.New("JSON object keys must be strings")
		}
		keys := value.MapKeys()
		sort.Slice(keys, func(left, right int) bool {
			return keys[left].String() < keys[right].String()
		})
		result.WriteByte('{')
		for index, key := range keys {
			if index != 0 {
				result.WriteByte(',')
			}
			if err := writePythonJSONString(result, key.String()); err != nil {
				return err
			}
			result.WriteByte(':')
			if err := writePythonJSON(result, value.MapIndex(key)); err != nil {
				return err
			}
		}
		result.WriteByte('}')
	case reflect.Slice, reflect.Array:
		result.WriteByte('[')
		for index := 0; index < value.Len(); index++ {
			if index != 0 {
				result.WriteByte(',')
			}
			if err := writePythonJSON(result, value.Index(index)); err != nil {
				return err
			}
		}
		result.WriteByte(']')
	case reflect.Struct:
		return writePythonJSONStruct(result, value)
	default:
		return fmt.Errorf("unsupported JSON type %s", value.Type())
	}
	return nil
}

func writePythonJSONStruct(result *strings.Builder, value reflect.Value) error {
	type field struct {
		name  string
		value reflect.Value
	}
	fields := make([]field, 0, value.NumField())
	for index := 0; index < value.NumField(); index++ {
		definition := value.Type().Field(index)
		if definition.PkgPath != "" {
			continue
		}
		tag := definition.Tag.Get("json")
		parts := strings.Split(tag, ",")
		if parts[0] == "-" {
			continue
		}
		name := parts[0]
		if name == "" {
			name = definition.Name
		}
		omitEmpty := false
		for _, option := range parts[1:] {
			omitEmpty = omitEmpty || option == "omitempty"
		}
		fieldValue := value.Field(index)
		if omitEmpty && fieldValue.IsZero() {
			continue
		}
		fields = append(fields, field{name: name, value: fieldValue})
	}
	sort.Slice(fields, func(left, right int) bool { return fields[left].name < fields[right].name })
	result.WriteByte('{')
	for index, item := range fields {
		if index != 0 {
			result.WriteByte(',')
		}
		if err := writePythonJSONString(result, item.name); err != nil {
			return err
		}
		result.WriteByte(':')
		if err := writePythonJSON(result, item.value); err != nil {
			return err
		}
	}
	result.WriteByte('}')
	return nil
}

func writePythonJSONString(result *strings.Builder, value string) error {
	if !utf8.ValidString(value) {
		return errors.New("JSON string is not UTF-8")
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	raw := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	for _, character := range string(raw) {
		if character <= 0x7f {
			result.WriteRune(character)
			continue
		}
		if character <= 0xffff {
			fmt.Fprintf(result, `\u%04x`, character)
			continue
		}
		first, second := utf16.EncodeRune(character)
		fmt.Fprintf(result, `\u%04x\u%04x`, first, second)
	}
	return nil
}

func writePythonJSONNumber(result *strings.Builder, value json.Number) error {
	raw := value.String()
	if strings.ContainsAny(raw, ".eE") {
		number, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return errors.New("invalid JSON number")
		}
		encoded, err := pythonFloat(number, 64)
		if err != nil {
			return err
		}
		result.WriteString(encoded)
		return nil
	}
	integer, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return errors.New("invalid JSON number")
	}
	result.WriteString(integer.String())
	return nil
}

func pythonFloat(value float64, bitSize int) (string, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return "", errors.New("JSON float must be finite")
	}
	if value == 0 {
		if math.Signbit(value) {
			return "-0.0", nil
		}
		return "0.0", nil
	}

	scientific := strconv.FormatFloat(value, 'e', -1, bitSize)
	separator := strings.LastIndexByte(scientific, 'e')
	if separator < 0 {
		return "", errors.New("could not format JSON float")
	}
	mantissa := scientific[:separator]
	exponent, err := strconv.Atoi(scientific[separator+1:])
	if err != nil {
		return "", errors.New("could not format JSON float")
	}
	if exponent < -4 || exponent >= 16 {
		sign := "+"
		if exponent < 0 {
			sign = "-"
			exponent = -exponent
		}
		return mantissa + "e" + sign + fmt.Sprintf("%02d", exponent), nil
	}

	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign = "-"
		mantissa = mantissa[1:]
	}
	digits := strings.ReplaceAll(mantissa, ".", "")
	decimalPosition := 1 + exponent
	switch {
	case decimalPosition <= 0:
		return sign + "0." + strings.Repeat("0", -decimalPosition) + digits, nil
	case decimalPosition >= len(digits):
		return sign + digits + strings.Repeat("0", decimalPosition-len(digits)) + ".0", nil
	default:
		return sign + digits[:decimalPosition] + "." + digits[decimalPosition:], nil
	}
}

func pythonTime(value time.Time) string {
	value = value.UTC()
	text := value.Format("2006-01-02T15:04:05")
	if value.Nanosecond() != 0 {
		text += fmt.Sprintf(".%06d", value.Nanosecond()/1000)
	}
	return text + "+00:00"
}

func optionalTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return pythonTime(*value)
}
