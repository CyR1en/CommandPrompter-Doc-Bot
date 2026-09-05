// capsule-control proves the trusted Go host, isolated capsule, durable queue,
// and documentation publication path as one production-shaped verification.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/cyr1en/ref0/internal/artifacts"
	"github.com/cyr1en/ref0/internal/capsule"
	"github.com/cyr1en/ref0/internal/capsuledoc"
	"github.com/cyr1en/ref0/internal/credentials"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/jobterminal"
	"github.com/cyr1en/ref0/internal/providers"
	"github.com/cyr1en/ref0/internal/safenet"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"github.com/cyr1en/ref0/internal/worker"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	dataRoot        = "/data"
	databaseEnv     = "DATABASE_URL"
	providerURL     = "http://provider:8080/v1"
	crashExit       = 86
	application     = "pi-durable-verification"
	sourceUUID      = "11111111-1111-4111-8111-111111111111"
	revisionUUID    = "22222222-2222-4222-8222-222222222222"
	operatorUUID    = "33333333-3333-4333-8333-333333333333"
	plannerCredUUID = "44444444-4444-4444-8444-444444444444"
	writerCredUUID  = "55555555-5555-4555-8555-555555555555"
	plannerEndUUID  = "66666666-6666-4666-8666-666666666666"
	writerEndUUID   = "77777777-7777-4777-8777-777777777777"
	plannerProfUUID = "88888888-8888-4888-8888-888888888888"
	writerProfUUID  = "99999999-9999-4999-8999-999999999999"
	plannerVerUUID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	writerVerUUID   = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	plannerAsnUUID  = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	writerAsnUUID   = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	knowledgeUUID   = "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	protocolCredID  = "ffffffff-ffff-4fff-8fff-ffffffffffff"
)

var sourceBytes = []byte("def verified_feature():\n    return \"durable publication\"\n")

func require(condition bool, message string, arguments ...any) {
	if !condition {
		panic(fmt.Sprintf(message, arguments...))
	}
}

func newVault() *security.CredentialVault {
	vault, err := security.NewCredentialVault(os.Getenv("APP_MASTER_KEY"), os.Getenv("APP_PREVIOUS_MASTER_KEYS"))
	if err != nil {
		panic(err)
	}
	return vault
}

func databasePool(ctx context.Context) *pgxpool.Pool {
	pool, err := pgxpool.New(ctx, os.Getenv(databaseEnv))
	if err != nil {
		panic(err)
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		panic(err)
	}
	return pool
}

func slotPool(paths ...string) *capsule.SlotPool {
	slots := make([]capsule.Slot, len(paths))
	for index, path := range paths {
		var err error
		slots[index], err = capsule.NewSlot(fmt.Sprintf("verification-%d", index), path)
		if err != nil {
			panic(err)
		}
	}
	pool, err := capsule.NewSlotPool(slots)
	if err != nil {
		panic(err)
	}
	if err = pool.Start(); err != nil {
		panic(err)
	}
	return pool
}

func closeSlots(pool *capsule.SlotPool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pool.Close(ctx); err != nil {
		panic(err)
	}
}

type exactSecretReader struct {
	id      credentials.ID
	version int32
	secret  *security.SecretValue
}

func (reader exactSecretReader) Read(_ context.Context, id credentials.ID, kind credentials.Kind, version int32) (*security.SecretValue, error) {
	if id != reader.id || kind != credentials.ProviderAPIKey || version != reader.version {
		return nil, errors.New("credential resolution rejected")
	}
	return reader.secret, nil
}

func protocolTools() []capsule.Tool {
	object := func(properties map[string]any, required ...any) map[string]any {
		return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
	}
	stringProperty := map[string]any{"type": "string", "maxLength": 4096}
	unused := func(context.Context, map[string]any) (any, error) { return map[string]any{}, nil }
	return []capsule.Tool{
		{Name: "list", Description: "List verified source files.", Parameters: object(map[string]any{"path": stringProperty}, "path"), Handler: unused},
		{Name: "glob", Description: "Glob verified source files.", Parameters: object(map[string]any{"pattern": stringProperty}, "pattern"), Handler: unused},
		{Name: "grep", Description: "Search verified source files.", Parameters: object(map[string]any{"path": stringProperty, "query": map[string]any{"type": "string"}}, "path", "query"), Handler: unused},
		{Name: "read", Description: "Read verified source lines.", Parameters: object(map[string]any{
			"path": stringProperty, "start_line": map[string]any{"type": "integer", "minimum": 1}, "end_line": map[string]any{"type": "integer", "minimum": 1},
		}, "path"), Handler: func(context.Context, map[string]any) (any, error) {
			return map[string]any{"path": "/sources/" + sourceUUID + "/verified.py", "start_line": 1, "end_line": 2, "lines": []any{"def verified_feature():", "    return \"durable publication\""}}, nil
		}},
	}
}

func planSchema() map[string]any {
	slug := map[string]any{"type": "string", "pattern": `^[a-z0-9]+(?:-[a-z0-9]+)*$`}
	return map[string]any{
		"type": "object", "properties": map[string]any{
			"pages": map[string]any{"type": "array", "minItems": 1, "maxItems": 1, "items": map[string]any{
				"type": "object", "properties": map[string]any{
					"slug": slug, "title": map[string]any{"type": "string"}, "purpose": map[string]any{"type": "string"},
					"related_pages": map[string]any{"type": "array", "items": slug},
					"source_seed_paths": map[string]any{"type": "array", "items": map[string]any{
						"type": "object", "properties": map[string]any{"source_id": map[string]any{"type": "string", "format": "uuid"}, "path": map[string]any{"type": "string"}},
						"required": []any{"source_id", "path"}, "additionalProperties": false,
					}},
				}, "required": []any{"slug", "title", "purpose", "related_pages", "source_seed_paths"}, "additionalProperties": false,
			}},
		}, "required": []any{"pages"}, "additionalProperties": false,
	}
}

func protocolBinding(model string, credential credentials.ID, version int32, maximumRequests int, timeout time.Duration) capsule.Binding {
	limits := capsule.DefaultLimits()
	limits.MaxModelRequests = maximumRequests
	limits.AttemptTimeout = timeout
	return capsule.Binding{
		ModelID: model, BaseURL: providerURL, ChatCompletionsPath: "chat/completions",
		BodyOptions: map[string]any{"seed": 7}, ContextWindow: 8192, MaxOutputTokens: 1024,
		ReasoningEffort: providers.EffortNone, ReasoningOptions: map[string]any{}, Timeout: 5 * time.Second,
		Credential:             &capsule.CredentialReference{ID: credential, SecretVersion: version},
		CapsuleRuntimeRevision: capsule.RuntimeRevision, Limits: limits,
		NetworkPolicy: safenet.Policy{AllowPrivateAddresses: true, AllowPlainHTTP: true},
	}
}

func secret(value string) *security.SecretValue {
	selected, err := security.NewSecretValue(value)
	if err != nil {
		panic(err)
	}
	return selected
}

func lowLevelProtocol(ctx context.Context) {
	credential, err := credentials.ParseID(protocolCredID)
	if err != nil {
		panic(err)
	}
	pool := slotPool("/run/prod-0/capsule.sock", "/run/prod-1/capsule.sock")
	defer closeSlots(pool)
	factory, err := capsule.NewFactory(
		protocolBinding("protocol-verification-model", credential, 7, 2, 10*time.Second),
		capsule.Planner, pool,
		exactSecretReader{credential, 7, secret(os.Getenv("PROTOCOL_API_TOKEN"))}, capsule.FactoryOptions{},
	)
	if err != nil {
		panic(err)
	}
	session, err := factory.NewSession(capsule.Planner, "verification", protocolTools(), planSchema())
	if err != nil {
		panic(err)
	}
	invocation, err := session.Invoke(ctx, "verify the captured source")
	if err != nil {
		panic(err)
	}
	pages, ok := invocation.Output["pages"].([]any)
	require(ok && len(pages) == 1 && pages[0].(map[string]any)["slug"] == "verified-flow", "low-level protocol returned an invalid plan")
	require(invocation.Usage == (capsule.Usage{ModelCalls: 2, InputTokens: 7, OutputTokens: 4, TotalTokens: 11}), "low-level usage mismatch: %+v", invocation.Usage)
	observations := providerObservations(ctx)
	selected := filterObservations(observations, "protocol-verification-model")
	require(len(selected) == 2 && selected[0].Turn == 1 && selected[0].Tool == "read" && selected[1].Turn == 2 && selected[1].Tool == "submit_result", "low-level provider observations mismatch: %+v", selected)
}

func stalledProtocol(ctx context.Context) map[string]any {
	credential, err := credentials.ParseID(protocolCredID)
	if err != nil {
		panic(err)
	}
	pool := slotPool("/run/stalled/capsule.sock")
	defer closeSlots(pool)
	factory, err := capsule.NewFactory(
		protocolBinding("stalled-verification-model", credential, 1, 1, 2*time.Second),
		capsule.Planner, pool,
		exactSecretReader{credential, 1, secret(os.Getenv("PROTOCOL_API_TOKEN"))}, capsule.FactoryOptions{},
	)
	if err != nil {
		panic(err)
	}
	schema := map[string]any{
		"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}},
		"required": []any{"answer"}, "additionalProperties": false,
	}
	for attempt := 1; attempt <= 2; attempt++ {
		session, sessionErr := factory.NewSession(capsule.Planner, "verification", nil, schema)
		if sessionErr != nil {
			panic(sessionErr)
		}
		started := time.Now()
		_, invocationErr := session.Invoke(ctx, "verification")
		elapsed := time.Since(started)
		var failure *capsule.InvocationError
		require(errors.As(invocationErr, &failure) &&
			(failure.Error() == "capsule attempt timed out safely" || failure.Error() == "capsule attempt failed safely"),
			"stalled attempt did not fail safely: %v", invocationErr)
		require(failure.Usage == (capsule.Usage{ModelCalls: 1, InputTokens: 31, OutputTokens: 7, TotalTokens: 38}), "stalled usage mismatch: %+v", failure.Usage)
		require(elapsed >= 1500*time.Millisecond && elapsed < 3500*time.Millisecond, "stalled attempt elapsed outside timeout bound: %s", elapsed)
		selected := filterObservations(providerObservations(ctx), "stalled-verification-model")
		require(len(selected) == attempt, "stalled provider request count=%d want=%d", len(selected), attempt)
	}
	return map[string]any{
		"stalled_result_payload_bytes": 700_000, "stalled_transport_attempts": 2,
		"stalled_transport_bounded_timeout": true, "stalled_slot_reopened": true,
	}
}

type observation struct {
	Model           string  `json:"model"`
	Turn            int     `json:"turn"`
	Tool            string  `json:"tool"`
	ReasoningEffort *string `json:"reasoning_effort"`
	InputTokens     int     `json:"input_tokens"`
	OutputTokens    int     `json:"output_tokens"`
	TotalTokens     int     `json:"total_tokens"`
}

func providerObservations(ctx context.Context) []observation {
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://provider:8080/observations", nil)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		panic(err)
	}
	defer response.Body.Close()
	var payload struct {
		Requests []observation `json:"requests"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(response.Body).Decode(&payload) != nil || len(payload.Requests) > 32 {
		panic("provider observations are invalid")
	}
	return payload.Requests
}

func filterObservations(values []observation, model string) []observation {
	result := []observation{}
	for _, value := range values {
		if value.Model == model {
			result = append(result, value)
		}
	}
	return result
}

func seed(ctx context.Context) {
	pool := databasePool(ctx)
	defer pool.Close()
	vault := newVault()
	files, err := sourcefiles.NewStore(dataRoot)
	if err != nil {
		panic(err)
	}
	sourceID, _ := sourcefiles.ParseID(sourceUUID)
	revisionID, _ := sourcefiles.ParseID(revisionUUID)
	stored, err := files.StoreSnapshot(sourceID, revisionID, sourcefiles.Files(sourcefiles.File{Path: "verified.py", Content: sourceBytes}), nil)
	if err != nil {
		panic(err)
	}
	plannerCredential, _ := credentials.ParseID(plannerCredUUID)
	writerCredential, _ := credentials.ParseID(writerCredUUID)
	plannerEnvelope, err := vault.Encrypt(security.CredentialID(plannerCredential), security.CredentialProviderAPIKey, 1, secret(os.Getenv("PLANNER_API_TOKEN")))
	if err != nil {
		panic(err)
	}
	writerEnvelope, err := vault.Encrypt(security.CredentialID(writerCredential), security.CredentialProviderAPIKey, 2, secret(os.Getenv("WRITER_API_TOKEN")))
	if err != nil {
		panic(err)
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		panic(err)
	}
	defer tx.Rollback(ctx)
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO operators(id,username,username_key,password_hash) VALUES($1,'Pi Verification Operator','pi verification operator','not-used')`, []any{operatorUUID}},
		{`INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language,version) VALUES($1,'Pi durable verification','pi durable verification','RESTRICTED','ACTIVE','Use exact captured evidence.','en',1)`, []any{knowledgeUUID}},
		{`INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version) VALUES($1,'PROVIDER_API_KEY','Planner verification key',$2,$3,$4,$5,1)`, []any{plannerCredUUID, credentials.MaskedValue, plannerEnvelope.KeyID(), plannerEnvelope.Nonce(), plannerEnvelope.Ciphertext()}},
		{`INSERT INTO credentials(id,kind,label,masked_value,key_id,nonce,ciphertext,secret_version,rotated_at) VALUES($1,'PROVIDER_API_KEY','Writer verification key',$2,$3,$4,$5,2,clock_timestamp())`, []any{writerCredUUID, credentials.MaskedValue, writerEnvelope.KeyID(), writerEnvelope.Nonce(), writerEnvelope.Ciphertext()}},
		{`INSERT INTO provider_endpoints(id,display_name,display_key,base_url,credential_id,headers,chat_completions_path,responses_path,models_path,allow_http,allow_private_network,lifecycle,version,configuration_version) VALUES($1,'Planner provider','planner provider',$2,$3,'{"X-Role":"planner"}'::jsonb,'chat/completions','responses','models',true,true,'ACTIVE',1,1)`, []any{plannerEndUUID, providerURL, plannerCredUUID}},
		{`INSERT INTO provider_endpoints(id,display_name,display_key,base_url,credential_id,headers,chat_completions_path,responses_path,models_path,allow_http,allow_private_network,lifecycle,version,configuration_version) VALUES($1,'Writer provider','writer provider',$2,$3,'{"X-Role":"writer"}'::jsonb,'chat/completions','responses','models',true,true,'ACTIVE',2,2)`, []any{writerEndUUID, providerURL, writerCredUUID}},
		{`INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id,version) VALUES($1,$2,'planner-verification-model','MANUAL',$3,1)`, []any{plannerProfUUID, plannerEndUUID, plannerVerUUID}},
		{`INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id,version) VALUES($1,$2,'writer-verification-model','MANUAL',$3,2)`, []any{writerProfUUID, writerEndUUID, writerVerUUID}},
		{profileVersionInsert(1, 1), []any{plannerVerUUID, plannerProfUUID, operatorUUID}},
		{profileVersionInsert(2, 2), []any{writerVerUUID, writerProfUUID, operatorUUID}},
		{`INSERT INTO model_assignments(id,knowledge_base_id,role,model_profile_id,reasoning_effort,answer_mode,version) VALUES($1,$2,'DOCUMENTATION_PLANNER',$3,'HIGH','TOOL_CALLING',1)`, []any{plannerAsnUUID, knowledgeUUID, plannerProfUUID}},
		{`INSERT INTO model_assignments(id,knowledge_base_id,role,model_profile_id,reasoning_effort,answer_mode,version) VALUES($1,$2,'DOCUMENTATION_WRITER',$3,'MEDIUM','TOOL_CALLING',1)`, []any{writerAsnUUID, knowledgeUUID, writerProfUUID}},
		{`INSERT INTO sources(id,knowledge_base_id,kind,display_name,display_key,privacy,lifecycle,health,checked_at,version,configuration_version,validated_configuration_version) VALUES($1,$2,'REPOSITORY','Verified source','verified source','PRIVATE','ACTIVE','HEALTHY',clock_timestamp(),2,1,1)`, []any{sourceUUID, knowledgeUUID}},
		{`INSERT INTO source_revisions(id,source_id,observed_ref_kind,observed_ref,native_version,fingerprint,artifact_key,file_count,byte_count,ignored_paths) VALUES($1,$2,'BRANCH','main',$3,$4,$5,$6,$7,'[]'::jsonb)`, []any{revisionUUID, sourceUUID, strings.Repeat("a", 40), stored.Fingerprint.Digest[:], stored.ArtifactKey, stored.Fingerprint.FileCount, stored.Fingerprint.ByteCount}},
		{`UPDATE sources SET current_revision_id=$2 WHERE id=$1`, []any{sourceUUID, revisionUUID}},
	}
	for _, statement := range statements {
		if _, err = tx.Exec(ctx, statement.query, statement.args...); err != nil {
			panic(err)
		}
	}
	if err = tx.Commit(ctx); err != nil {
		panic(err)
	}
	runArtifacts, err := artifacts.NewRunStore(dataRoot)
	if err != nil {
		panic(err)
	}
	wikiArtifacts, err := artifacts.NewWikiStore(dataRoot)
	if err != nil {
		panic(err)
	}
	queue := jobs.NewStore(pool, jobterminal.Callback)
	store, err := docgen.NewStore(pool, queue, vault, runArtifacts, wikiArtifacts, files)
	if err != nil {
		panic(err)
	}
	knowledgeID, _ := docgen.ParseID(knowledgeUUID)
	actorID, _ := docgen.ParseID(operatorUUID)
	if _, err = store.RequestGeneration(ctx, knowledgeID, 1, actorID, "verify-documentation-run"); err != nil {
		panic(err)
	}
}

func profileVersionInsert(version, configuration int) string {
	return fmt.Sprintf(`INSERT INTO model_profile_versions(id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,timeout_seconds,max_retries,extra_body,metadata_origin,source,created_by_operator_id) VALUES($1,$2,%d,%d,'CHAT_COMPLETIONS',8192,1024,true,true,true,true,'REASONING_EFFORT',5,0,'{"seed":7}'::jsonb,'%s'::jsonb,'OPERATOR',$3)`, version, configuration, metadataOrigins())
}

func metadataOrigins() string {
	fields := []string{"model_id", "transport", "context_window_tokens", "max_output_tokens", "supports_streaming", "supports_tools", "supports_structured_output", "supports_temperature", "reasoning_transport", "reasoning_mapping", "timeout_seconds", "max_retries", "max_concurrent_tasks", "extra_body"}
	value := make(map[string]string, len(fields))
	for _, field := range fields {
		value[field] = "OPERATOR"
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

type crashQueue struct {
	*jobs.Store
	mu    sync.Mutex
	types map[jobs.JobID]jobs.Type
}

func (queue *crashQueue) GetCommand(ctx context.Context, permit jobs.Permit) (jobs.Command, error) {
	command, err := queue.Store.GetCommand(ctx, permit)
	if err == nil {
		queue.mu.Lock()
		queue.types[permit.JobID] = command.Type
		queue.mu.Unlock()
	}
	return command, err
}

func (queue *crashQueue) CompleteAcceptedResult(ctx context.Context, permit jobs.Permit, result map[string]any) error {
	queue.mu.Lock()
	jobType := queue.types[permit.JobID]
	queue.mu.Unlock()
	if jobType == jobs.GeneratePage {
		os.Exit(crashExit)
	}
	return queue.Store.CompleteAcceptedResult(ctx, permit, result)
}

type runtimeResources struct {
	pool   *pgxpool.Pool
	slots  *capsule.SlotPool
	runner *worker.Runner
	store  *docgen.Store
}

func (resources *runtimeResources) close() {
	closeSlots(resources.slots)
	resources.pool.Close()
}

func documentationRegistry(handlers *docgen.Handlers) worker.Registry {
	registry := worker.Registry{}
	for jobType, documentationHandler := range handlers.Registry() {
		handler := documentationHandler
		registry[jobType] = func(ctx context.Context, command jobs.Command, permit jobs.Permit) (map[string]any, error) {
			result, err := handler(ctx, command, permit)
			var failure *docgen.HandlerFailure
			if errors.As(err, &failure) && failure != nil {
				return nil, &worker.HandlerFailure{SanitizedError: failure.SanitizedError, Retryable: failure.Retryable}
			}
			return result, err
		}
	}
	return registry
}

func buildRuntime(ctx context.Context, crash bool) *runtimeResources {
	pool := databasePool(ctx)
	slots := slotPool("/run/prod-0/capsule.sock", "/run/prod-1/capsule.sock")
	vault := newVault()
	files, err := sourcefiles.NewStore(dataRoot)
	if err != nil {
		panic(err)
	}
	secrets, err := credentials.NewSecretReader(pool, vault)
	if err != nil {
		panic(err)
	}
	providerStore, err := providers.NewStore(pool, vault)
	if err != nil {
		panic(err)
	}
	agent, err := capsuledoc.NewRuntime(providerStore, files, slots, secrets, application, capsuledoc.DefaultOptions())
	if err != nil {
		panic(err)
	}
	runtime, err := docgen.NewRuntime(pool, jobs.NewStore(pool, jobterminal.Callback), vault, dataRoot, agent)
	if err != nil {
		panic(err)
	}
	var queue worker.Queue = runtime.Queue
	if crash {
		queue = &crashQueue{Store: runtime.Queue, types: map[jobs.JobID]jobs.Type{}}
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner, err := worker.NewRunner(queue, documentationRegistry(runtime.Handlers), worker.Config{
		WorkerID: "verification-worker", LeaseFor: 2 * time.Second, HeartbeatEvery: 500 * time.Millisecond,
		PollEvery: 50 * time.Millisecond, RetryBackoff: 50 * time.Millisecond,
	}, logger)
	if err != nil {
		panic(err)
	}
	return &runtimeResources{pool: pool, slots: slots, runner: runner, store: runtime.Store}
}

func crashPhase(ctx context.Context) {
	runtime := buildRuntime(ctx, true)
	defer runtime.close()
	for range 3 {
		worked, err := runtime.runner.RunOnce(ctx)
		require(err == nil && worked, "crash phase runner failed: worked=%t err=%v", worked, err)
	}
	panic("crash interceptor did not terminate")
}

func recoverPhase(ctx context.Context) {
	runtime := buildRuntime(ctx, false)
	defer runtime.close()
	for range 2 {
		worked, err := runtime.runner.RunOnce(ctx)
		require(err == nil && worked, "recovery runner failed: worked=%t err=%v", worked, err)
	}
	knowledgeID, _ := docgen.ParseID(knowledgeUUID)
	runs, err := runtime.store.ListRuns(ctx, &knowledgeID, 10, 0)
	if err != nil {
		panic(err)
	}
	require(len(runs) == 1 && runs[0].Run.Status == docgen.RunPublished, "documentation run did not publish: %+v", runs)
}

func runPhase(ctx context.Context, phase string) int {
	command := exec.CommandContext(ctx, os.Args[0], "--phase="+phase)
	command.Stdout, command.Stderr = os.Stderr, os.Stderr
	err := command.Run()
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	panic(err)
}

func orchestrate(ctx context.Context) {
	stalled := stalledProtocol(ctx)
	lowLevelProtocol(ctx)
	seed(ctx)
	require(runPhase(ctx, "crash") == crashExit, "crash phase did not exit %d", crashExit)
	select {
	case <-ctx.Done():
		panic(ctx.Err())
	case <-time.After(3 * time.Second):
	}
	require(runPhase(ctx, "recover") == 0, "recovery phase failed")
	manifest := verifyManifest(ctx)
	for key, value := range stalled {
		manifest[key] = value
	}
	encoded, _ := json.Marshal(manifest)
	fmt.Println(string(encoded))
}

type modelCapture struct {
	Role                 string
	ProfileID            string
	ProfileVersionID     string
	ProfileVersion       int
	EndpointID           string
	ConfigurationVersion int
	CredentialVersion    int
	Reasoning            string
}

func verifyManifest(ctx context.Context) map[string]any {
	pool := databasePool(ctx)
	defer pool.Close()
	var runID, status, wikiID string
	var plannerCalls, plannerInput, plannerOutput, plannerTotal int
	if err := pool.QueryRow(ctx, `SELECT id::text,status,planner_model_calls,planner_input_tokens,planner_output_tokens,planner_total_tokens,published_wiki_version_id::text FROM documentation_runs WHERE knowledge_base_id=$1`, knowledgeUUID).Scan(&runID, &status, &plannerCalls, &plannerInput, &plannerOutput, &plannerTotal, &wikiID); err != nil {
		panic(err)
	}
	var pageID, pageJobID, pageStatus string
	var attempts, writerCalls, writerInput, writerOutput, writerTotal int
	var contentDigest, claimsDigest []byte
	if err := pool.QueryRow(ctx, `SELECT id::text,job_id::text,status,attempt_count,model_calls,input_tokens,output_tokens,total_tokens,content_sha256,claims_sha256 FROM documentation_pages WHERE run_id=$1`, runID).Scan(&pageID, &pageJobID, &pageStatus, &attempts, &writerCalls, &writerInput, &writerOutput, &writerTotal, &contentDigest, &claimsDigest); err != nil {
		panic(err)
	}
	var publishedWikiID string
	if err := pool.QueryRow(ctx, `SELECT published_wiki_id::text FROM knowledge_bases WHERE id=$1`, knowledgeUUID).Scan(&publishedWikiID); err != nil {
		panic(err)
	}
	var artifactKey string
	var manifestDigest []byte
	if err := pool.QueryRow(ctx, `SELECT artifact_key,manifest_sha256 FROM wiki_versions WHERE id=$1 AND documentation_run_id=$2`, wikiID, runID).Scan(&artifactKey, &manifestDigest); err != nil {
		panic(err)
	}
	var wikiContentDigest, wikiClaimsDigest []byte
	if err := pool.QueryRow(ctx, `SELECT content_sha256,claims_sha256 FROM wiki_pages WHERE wiki_version_id=$1 AND slug='verified-flow'`, wikiID).Scan(&wikiContentDigest, &wikiClaimsDigest); err != nil {
		panic(err)
	}
	var claimStableID, evidencePath string
	var evidenceStart, evidenceEnd int
	if err := pool.QueryRow(ctx, `SELECT c.stable_id,e.path,e.start_line,e.end_line FROM claims c JOIN evidence e ON e.claim_id=c.id WHERE c.wiki_version_id=$1`, wikiID).Scan(&claimStableID, &evidencePath, &evidenceStart, &evidenceEnd); err != nil {
		panic(err)
	}

	captures := []modelCapture{}
	rows, err := pool.Query(ctx, `SELECT role,model_profile_id::text,model_profile_version_id::text,profile_version,provider_endpoint_id::text,captured_endpoint_configuration_version,captured_credential_version,reasoning_effort FROM documentation_run_models WHERE run_id=$1 ORDER BY role`, runID)
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		var capture modelCapture
		if err = rows.Scan(&capture.Role, &capture.ProfileID, &capture.ProfileVersionID, &capture.ProfileVersion, &capture.EndpointID, &capture.ConfigurationVersion, &capture.CredentialVersion, &capture.Reasoning); err != nil {
			panic(err)
		}
		captures = append(captures, capture)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		panic(err)
	}
	var capturedRevision string
	var capturedFingerprint []byte
	if err = pool.QueryRow(ctx, `SELECT source_revision_id::text,fingerprint FROM documentation_run_sources WHERE run_id=$1`, runID).Scan(&capturedRevision, &capturedFingerprint); err != nil {
		panic(err)
	}
	files, _ := sourcefiles.NewStore(dataRoot)
	sourceID, _ := sourcefiles.ParseID(sourceUUID)
	revisionID, _ := sourcefiles.ParseID(revisionUUID)
	stored, err := files.LoadSnapshot(sourceID, revisionID)
	if err != nil || stored == nil {
		panic("stored source snapshot is unavailable")
	}

	leaseGenerations := []int64{}
	rows, err = pool.Query(ctx, `SELECT lease_generation FROM job_attempts WHERE job_id=$1 ORDER BY attempt_number`, pageJobID)
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		var generation int64
		if rows.Scan(&generation) != nil {
			panic("scan job attempt")
		}
		leaseGenerations = append(leaseGenerations, generation)
	}
	rows.Close()
	jobSequence := []string{}
	rows, err = pool.Query(ctx, `SELECT job_type FROM jobs WHERE target_id IN ($1,$2,$3) ORDER BY created_at,id`, knowledgeUUID, runID, pageID)
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		var value string
		_ = rows.Scan(&value)
		jobSequence = append(jobSequence, value)
	}
	rows.Close()

	docsObservations := []observation{}
	for _, item := range providerObservations(ctx) {
		if item.Model == "planner-verification-model" || item.Model == "writer-verification-model" {
			docsObservations = append(docsObservations, item)
		}
	}
	expectedModels := []string{"planner-verification-model", "planner-verification-model", "writer-verification-model", "writer-verification-model"}
	expectedTurns := []int{1, 2, 1, 2}
	expectedReasoning := []string{"high", "high", "medium", "medium"}
	require(len(docsObservations) == 4, "documentation provider requests=%d", len(docsObservations))
	for index, item := range docsObservations {
		require(item.Model == expectedModels[index] && item.Turn == expectedTurns[index] && item.ReasoningEffort != nil && *item.ReasoningEffort == expectedReasoning[index], "documentation provider observation mismatch: %+v", docsObservations)
	}
	require(status == "PUBLISHED" && pageStatus == "COMPLETE" && publishedWikiID == wikiID, "publication state mismatch")
	require(plannerCalls == 2 && plannerInput == 23 && plannerOutput == 7 && plannerTotal == 30, "planner usage mismatch")
	require(writerCalls == 2 && writerInput == 43 && writerOutput == 11 && writerTotal == 54, "writer usage mismatch")
	require(attempts == 1 && reflect.DeepEqual(leaseGenerations, []int64{1, 2}), "crash recovery attempt mismatch: attempts=%d generations=%v", attempts, leaseGenerations)
	require(len(captures) == 2 && captures[0].Reasoning == "HIGH" && captures[1].Reasoning == "MEDIUM", "model capture reasoning mismatch: %+v", captures)
	require(captures[0].ProfileID != captures[1].ProfileID && captures[0].EndpointID != captures[1].EndpointID, "model captures were not distinct")
	require(captures[0].CredentialVersion == 1 && captures[1].CredentialVersion == 2 && captures[0].ProfileVersion == 1 && captures[1].ProfileVersion == 2 && captures[0].ConfigurationVersion == 1 && captures[1].ConfigurationVersion == 2, "model capture versions mismatch: %+v", captures)
	require(capturedRevision == revisionUUID && bytes.Equal(capturedFingerprint, stored.Fingerprint.Digest[:]), "source capture mismatch")
	require(claimStableID == "durable-return" && evidencePath == "verified.py" && evidenceStart == 2 && evidenceEnd == 2, "claim or evidence mismatch")
	require(bytes.Equal(contentDigest, wikiContentDigest) && bytes.Equal(claimsDigest, wikiClaimsDigest), "wiki/page digest mismatch")
	expectedJobs := []string{"PREPARE_RUN", "PLAN_RUN", "GENERATE_PAGE", "FINALIZE_RUN"}
	require(reflect.DeepEqual(jobSequence, expectedJobs), "job sequence mismatch: %v", jobSequence)

	wikiRoot := filepath.Join(dataRoot, filepath.FromSlash(artifactKey))
	pageSnapshot := filepath.Join(dataRoot, "knowledge-bases", knowledgeUUID, "runs", runID, "page-snapshots", "verified-flow.md")
	require(bytes.Equal(fileDigest(pageSnapshot), contentDigest), "page artifact digest mismatch")
	require(bytes.Equal(fileDigest(filepath.Join(wikiRoot, ".page-manifest.json")), manifestDigest), "wiki manifest digest mismatch")
	assertNoDurableSecrets(ctx, pool, wikiRoot)

	return map[string]any{
		"status": "ok", "run_status": status, "job_sequence": jobSequence,
		"generate_attempts": len(leaseGenerations), "generate_lease_generations": leaseGenerations,
		"provider_requests":   len(docsObservations),
		"planner_usage":       map[string]int{"model_calls": 2, "input_tokens": 23, "output_tokens": 7, "total_tokens": 30},
		"writer_usage":        map[string]int{"model_calls": 2, "input_tokens": 43, "output_tokens": 11, "total_tokens": 54},
		"aggregate_usage":     map[string]int{"model_calls": 4, "input_tokens": 66, "output_tokens": 18, "total_tokens": 84},
		"page_content_sha256": fmt.Sprintf("%x", contentDigest), "page_claims_sha256": fmt.Sprintf("%x", claimsDigest),
		"wiki_manifest_sha256": fmt.Sprintf("%x", manifestDigest), "claim_count": 1, "evidence_count": 1,
		"reasoning": []string{captures[0].Reasoning, captures[1].Reasoning},
	}
}

func fileDigest(path string) []byte {
	content, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	digest := sha256.Sum256(content)
	return digest[:]
}

func assertNoDurableSecrets(ctx context.Context, pool *pgxpool.Pool, wikiRoot string) {
	var durable strings.Builder
	rows, err := pool.Query(ctx, `SELECT snapshot::text FROM event_log UNION ALL SELECT payload::text || coalesce(result::text,'') FROM jobs`)
	if err != nil {
		panic(err)
	}
	for rows.Next() {
		var value string
		if rows.Scan(&value) != nil {
			panic("scan durable state")
		}
		durable.WriteString(value)
	}
	rows.Close()
	if err = filepath.WalkDir(wikiRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, readErr := os.ReadFile(path)
		if readErr == nil {
			durable.Write(content)
		}
		return readErr
	}); err != nil {
		panic(err)
	}
	for _, forbidden := range []string{
		os.Getenv("PLANNER_API_TOKEN"), os.Getenv("WRITER_API_TOKEN"), os.Getenv("PROTOCOL_API_TOKEN"),
		"/run/prod-0/capsule.sock", "/run/prod-1/capsule.sock", "raw-provider-body-sentinel", "underlying-exception-sentinel",
	} {
		require(forbidden != "" && !strings.Contains(durable.String(), forbidden), "durable state leaked a secret or runtime detail")
	}
}

func main() {
	ctx := context.Background()
	phase := "orchestrate"
	if len(os.Args) == 2 && strings.HasPrefix(os.Args[1], "--phase=") {
		phase = strings.TrimPrefix(os.Args[1], "--phase=")
	} else if len(os.Args) != 1 {
		panic("usage: capsule-control [--phase=crash|--phase=recover]")
	}
	switch phase {
	case "orchestrate":
		orchestrate(ctx)
	case "crash":
		crashPhase(ctx)
	case "recover":
		recoverPhase(ctx)
	default:
		panic("unknown verification phase")
	}
}
