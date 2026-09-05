package verification

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/cyr1en/ref0/internal/auth"
	"github.com/cyr1en/ref0/internal/credentials"
	"github.com/cyr1en/ref0/internal/migrate"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

const backupPostgresImage = "postgres:18.6-bookworm"

type postgresContainer struct {
	id       string
	name     string
	port     int
	password string
}

func TestDatabaseAndArtifactBackupRestore(t *testing.T) {
	if os.Getenv("REF0_RUN_DOCKER_TESTS") != "1" {
		t.Skip("set REF0_RUN_DOCKER_TESTS=1 to run the container backup/restore proof")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	t.Setenv("APP_MASTER_KEY", "backup-key-v1:"+base64.RawURLEncoding.EncodeToString(randomBytes(t, 32)))
	t.Setenv("APP_PREVIOUS_MASTER_KEYS", "")

	password := randomToken(t, 24)
	suffix := hex.EncodeToString(randomBytes(t, 6))
	source := startPostgres(t, ctx, "ref0-backup-source-"+suffix, password)
	defer stopContainer(source)
	setDatabaseURL(t, source.databaseURL())
	if err := migrate.Run(ctx, []string{"up"}); err != nil {
		t.Fatalf("migrate source database: %v", err)
	}

	sourceData := t.TempDir()
	expected := seedBackupState(t, ctx, source.databaseURL(), sourceData)
	dump := dockerOutput(t, ctx, nil, "exec", source.id, "pg_dump", "-U", "ref0", "-d", "ref0", "--format=custom")
	archive := archiveDirectory(t, sourceData)

	restored := startPostgres(t, ctx, "ref0-backup-restore-"+suffix, password)
	defer stopContainer(restored)
	dockerOutput(t, ctx, dump, "exec", "-i", restored.id, "pg_restore", "-U", "ref0", "-d", "ref0", "--exit-on-error")
	restoredData := t.TempDir()
	extractArchive(t, archive, restoredData)
	verifyBackupState(t, ctx, restored.databaseURL(), restoredData, expected)
}

func startPostgres(t *testing.T, ctx context.Context, name, password string) postgresContainer {
	t.Helper()
	id := strings.TrimSpace(string(dockerOutput(t, ctx, nil,
		"run", "--detach", "--rm", "--name", name,
		"--label", "com.ref0.purpose=backup-restore-verification",
		"--publish", "127.0.0.1::5432",
		"--env", "POSTGRES_DB=ref0", "--env", "POSTGRES_USER=ref0",
		"--env", "POSTGRES_PASSWORD="+password, backupPostgresImage,
	)))
	if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(id) {
		t.Fatal("Docker returned an invalid PostgreSQL container ID")
	}
	container := postgresContainer{id: id, name: name, password: password}
	portText := strings.TrimSpace(string(dockerOutput(t, ctx, nil, "port", id, "5432/tcp")))
	match := regexp.MustCompile(`(?:127\.0\.0\.1|\[::1\]):([1-9][0-9]{0,4})$`).FindStringSubmatch(portText)
	if len(match) != 2 {
		t.Fatal("disposable PostgreSQL port mapping is invalid")
	}
	container.port, _ = strconv.Atoi(match[1])
	deadline := time.Now().Add(time.Minute)
	for time.Now().Before(deadline) {
		connection, err := pgx.Connect(ctx, container.databaseURL())
		if err == nil {
			var one int
			err = connection.QueryRow(ctx, "SELECT 1").Scan(&one)
			_ = connection.Close(ctx)
			if err == nil && one == 1 {
				return container
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("disposable PostgreSQL did not become ready")
	return postgresContainer{}
}

func (container postgresContainer) databaseURL() string {
	return fmt.Sprintf("postgresql://ref0:%s@127.0.0.1:%d/ref0?sslmode=disable", container.password, container.port)
}

func stopContainer(container postgresContainer) {
	if container.id == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "stop", container.id).Run()
}

func dockerOutput(t *testing.T, ctx context.Context, input []byte, arguments ...string) []byte {
	t.Helper()
	command := exec.CommandContext(ctx, "docker", arguments...)
	command.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("Docker command %q failed: %v (%s)", arguments[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes()
}

func setDatabaseURL(t *testing.T, value string) {
	t.Helper()
	previous, present := os.LookupEnv("DATABASE_URL")
	if err := os.Setenv("DATABASE_URL", value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv("DATABASE_URL", previous)
		} else {
			_ = os.Unsetenv("DATABASE_URL")
		}
	})
}

type backupExpectation struct {
	knowledgeBaseID          string
	secondaryKnowledgeBaseID string
	wikiID                   string
	secondaryWikiID          string
	credentialID             string
	endpointID               string
	agentID                  string
	agentVersionID           string
	agentRunID               string
	artifactKey              string
	secondaryArtifactKey     string
	credentialPlaintext      string
	credentialKeyID          string
	credentialNonce          []byte
	credentialVersion        int32
	ciphertext               []byte
	manifest                 []byte
	secondaryManifest        []byte
	manifestDigest           [32]byte
	secondaryManifestDigest  [32]byte
	scopeDigests             [2][32]byte
	page                     []byte
	secondaryPage            []byte
	agentSnapshot            string
	runSnapshot              string
}

func seedBackupState(t *testing.T, ctx context.Context, databaseURL, dataRoot string) backupExpectation {
	t.Helper()
	expected := backupExpectation{
		knowledgeBaseID:          "10000000-0000-4000-8000-000000000001",
		secondaryKnowledgeBaseID: "10000000-0000-4000-8000-00000000000d",
		wikiID:                   "10000000-0000-4000-8000-000000000002",
		secondaryWikiID:          "10000000-0000-4000-8000-00000000000e",
		endpointID:               "10000000-0000-4000-8000-000000000004",
		agentID:                  "10000000-0000-4000-8000-00000000000a",
		agentVersionID:           "10000000-0000-4000-8000-00000000000b",
		agentRunID:               "10000000-0000-4000-8000-00000000000c",
		credentialPlaintext:      "backup-provider-secret-" + randomToken(t, 18),
		page:                     []byte("# Restored wiki\n\nThis content must survive the volume round trip.\n"),
		secondaryPage:            []byte("# Restored reference\n\nOrdered Agent membership must survive too.\n"),
	}
	profileID := "10000000-0000-4000-8000-000000000005"
	profileVersionID := "10000000-0000-4000-8000-000000000006"
	jobID := "10000000-0000-4000-8000-000000000007"
	documentationRunID := "10000000-0000-4000-8000-000000000008"
	secondaryJobID := "10000000-0000-4000-8000-00000000000f"
	secondaryDocumentationRunID := "10000000-0000-4000-8000-000000000010"
	operatorID := "10000000-0000-4000-8000-000000000009"
	expected.artifactKey = "knowledge-bases/" + expected.knowledgeBaseID + "/wiki/" + expected.wikiID
	expected.secondaryArtifactKey = "knowledge-bases/" + expected.secondaryKnowledgeBaseID + "/wiki/" + expected.secondaryWikiID
	expected.manifest, expected.manifestDigest = writeBackupWikiArtifact(t, dataRoot, expected.artifactKey, documentationRunID, "Restored wiki", expected.page)
	expected.secondaryManifest, expected.secondaryManifestDigest = writeBackupWikiArtifact(t, dataRoot, expected.secondaryArtifactKey, secondaryDocumentationRunID, "Restored reference", expected.secondaryPage)
	copy(expected.scopeDigests[0][:], bytes.Repeat([]byte{0xcd}, 32))
	copy(expected.scopeDigests[1][:], bytes.Repeat([]byte{0xef}, 32))

	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	if _, err = connection.Exec(ctx, `
		INSERT INTO operators(id,username,username_key,password_hash)
		VALUES($1,'Backup Operator','backup operator','unused')
	`, operatorID); err != nil {
		t.Fatal(err)
	}
	vault, err := security.NewCredentialVault(os.Getenv("APP_MASTER_KEY"), os.Getenv("APP_PREVIOUS_MASTER_KEYS"))
	if err != nil {
		t.Fatal(err)
	}
	secret, err := security.NewSecretValue(expected.credentialPlaintext)
	if err != nil {
		t.Fatal(err)
	}
	var operatorUUID pgtype.UUID
	if err = operatorUUID.Scan(operatorID); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	credentialService, err := credentials.NewService(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := credentialService.Create(ctx, credentials.CreateCommand{
		Kind: credentials.ProviderAPIKey, Label: "Backup proof key", Secret: secret,
	}, auth.OperatorID(operatorUUID.Bytes), "backup-proof-credential")
	if err != nil {
		t.Fatal(err)
	}
	expected.credentialID = credential.ID.String()
	if err = connection.QueryRow(ctx, `
		SELECT key_id,nonce,ciphertext,secret_version FROM credentials WHERE id=$1
	`, expected.credentialID).Scan(
		&expected.credentialKeyID, &expected.credentialNonce, &expected.ciphertext, &expected.credentialVersion,
	); err != nil || expected.credentialKeyID != vault.ActiveKeyID() || expected.credentialVersion != 1 {
		t.Fatalf("seeded encrypted credential = key %q version %d, %v", expected.credentialKeyID, expected.credentialVersion, err)
	}
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	now := time.Now().UTC()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO provider_endpoints(id,display_name,display_key,base_url,credential_id,headers) VALUES($1,'Restored endpoint','restored endpoint','https://provider.invalid/v1',$2,'{"X-Restore-Proof":"present"}'::jsonb)`, []any{expected.endpointID, expected.credentialID}},
		{`INSERT INTO model_profiles(id,endpoint_id,model_id,availability,current_version_id) VALUES($1,$2,'backup-model','AVAILABLE',$3)`, []any{profileID, expected.endpointID, profileVersionID}},
		{`
			INSERT INTO model_profile_versions(
				id,profile_id,version_number,configuration_version,transport,context_window_tokens,max_output_tokens,
				supports_streaming,supports_tools,supports_structured_output,supports_temperature,reasoning_transport,
				timeout_seconds,max_retries,max_concurrent_tasks,extra_body,metadata_origin,source,created_by_operator_id
			) VALUES($1,$2,1,1,'CHAT_COMPLETIONS',16000,4096,true,true,true,true,'NONE',30,0,1,'{}','{}','OPERATOR',$3)
		`, []any{profileVersionID, profileID, operatorID}},
		{`INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language) VALUES($1,'Restored knowledge base','restored knowledge base','RESTRICTED','ACTIVE','Preserve this configuration.','en')`, []any{expected.knowledgeBaseID}},
		{`INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language) VALUES($1,'Restored reference','restored reference','PUBLIC','ACTIVE','Preserve ordered reference membership.','en')`, []any{expected.secondaryKnowledgeBaseID}},
		{`INSERT INTO jobs(id,job_type,target_type,target_id,payload,operation_key,status,attempt_count,max_attempts,progress,result,started_at,finished_at) VALUES($1,'PREPARE_RUN','knowledge_base',$2,'{}'::jsonb,$3,'SUCCEEDED',1,3,100,'{}'::jsonb,$4,$4)`, []any{jobID, expected.knowledgeBaseID, "backup-proof:" + expected.knowledgeBaseID, now}},
		{`INSERT INTO jobs(id,job_type,target_type,target_id,payload,operation_key,status,attempt_count,max_attempts,progress,result,started_at,finished_at) VALUES($1,'PREPARE_RUN','knowledge_base',$2,'{}'::jsonb,$3,'SUCCEEDED',1,3,100,'{}'::jsonb,$4,$4)`, []any{secondaryJobID, expected.secondaryKnowledgeBaseID, "backup-proof:" + expected.secondaryKnowledgeBaseID, now}},
		{`INSERT INTO documentation_runs(id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,instructions,language,plan_digest,completed_at) VALUES($1,$2,'PUBLISHED',$3,1,'Preserve this configuration.','en',$4,$5)`, []any{documentationRunID, expected.knowledgeBaseID, jobID, bytes.Repeat([]byte{'p'}, 32), now}},
		{`INSERT INTO documentation_runs(id,knowledge_base_id,status,prepare_job_id,knowledge_base_version,instructions,language,plan_digest,completed_at) VALUES($1,$2,'PUBLISHED',$3,1,'Preserve ordered reference membership.','en',$4,$5)`, []any{secondaryDocumentationRunID, expected.secondaryKnowledgeBaseID, secondaryJobID, bytes.Repeat([]byte{'q'}, 32), now}},
		{`INSERT INTO wiki_versions(id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count,published_at) VALUES($1,$2,$3,$4,$5,1,$6)`, []any{expected.wikiID, expected.knowledgeBaseID, documentationRunID, expected.artifactKey, expected.manifestDigest[:], now}},
		{`INSERT INTO wiki_versions(id,knowledge_base_id,documentation_run_id,artifact_key,manifest_sha256,page_count,published_at) VALUES($1,$2,$3,$4,$5,1,$6)`, []any{expected.secondaryWikiID, expected.secondaryKnowledgeBaseID, secondaryDocumentationRunID, expected.secondaryArtifactKey, expected.secondaryManifestDigest[:], now}},
		{`UPDATE documentation_runs SET published_wiki_version_id=$1 WHERE id=$2`, []any{expected.wikiID, documentationRunID}},
		{`UPDATE documentation_runs SET published_wiki_version_id=$1 WHERE id=$2`, []any{expected.secondaryWikiID, secondaryDocumentationRunID}},
		{`UPDATE knowledge_bases SET published_wiki_id=$1,version=2 WHERE id=$2`, []any{expected.wikiID, expected.knowledgeBaseID}},
		{`UPDATE knowledge_bases SET published_wiki_id=$1,version=2 WHERE id=$2`, []any{expected.secondaryWikiID, expected.secondaryKnowledgeBaseID}},
		{`INSERT INTO agents(id,agent_key,lifecycle,current_version_id,activated_at) VALUES($1,'backup-agent','ACTIVE',$2,clock_timestamp())`, []any{expected.agentID, expected.agentVersionID}},
		{`
			INSERT INTO agent_versions(
				id,agent_id,version_number,display_name,description,response_language,identity_instructions,model_profile_id,
				reasoning_effort,answer_mode,behavioral_instructions,evidence_access,refusal_markdown,
				max_tool_calls,max_answer_tokens,created_by_operator_id
			) VALUES($1,$2,1,'Restored Agent','Complete backup proof.','en-US','Answer only from restored evidence.',$3,
			         'LOW','TOOL_CALLING','Prefer concise verified answers.','WIKI_AND_SOURCE','Cannot answer from restored evidence.',4,2048,$4)
		`, []any{expected.agentVersionID, expected.agentID, profileID, operatorID}},
		{`INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id) VALUES($1,$2,0,$3)`, []any{expected.agentID, expected.agentVersionID, expected.knowledgeBaseID}},
		{`INSERT INTO agent_version_knowledge_bases(agent_id,agent_version_id,position,knowledge_base_id) VALUES($1,$2,1,$3)`, []any{expected.agentID, expected.agentVersionID, expected.secondaryKnowledgeBaseID}},
		{`
			INSERT INTO agent_runs(
				id,agent_id,agent_version_id,agent_resource_version,agent_version_number,
				model_profile_id,model_profile_version_id,model_profile_version_number,
				provider_endpoint_id,captured_endpoint_configuration_version,captured_credential_id,captured_credential_version,
				origin,subject,request_digest,effective_access_policy,outcome,model_usage,latency_ms,tool_calls,citations,
				created_at,completed_at
			) VALUES($1,$2,$3,1,1,$4,$5,1,$6,1,$7,1,'HTTP','chat-token:backup-proof',
				decode(repeat('ab',32),'hex'),'RESTRICTED','ANSWERED',
				'{"model_calls":2,"input_tokens":120,"output_tokens":30,"total_tokens":150}'::jsonb,321,
				'["initial_search_wiki","read_wiki_page"]'::jsonb,
				'[{"id":"cite_backup","knowledge_base_id":"10000000-0000-4000-8000-000000000001","wiki_version_id":"10000000-0000-4000-8000-000000000002","label":"Restored wiki","resource":"wiki://backup/overview","path":"overview","start_line":1,"end_line":2}]'::jsonb,
				$8,$8)
		`, []any{expected.agentRunID, expected.agentID, expected.agentVersionID, profileID, profileVersionID, expected.endpointID, expected.credentialID, now}},
		{`
			INSERT INTO agent_run_knowledge_bases(
				run_id,position,knowledge_base_id,knowledge_base_version,access_policy,
				wiki_version_id,documentation_run_id,source_revision_ids,source_scope_digest
			) VALUES($1,0,$2,2,'RESTRICTED',$3,$4,ARRAY['10000000-0000-4000-8000-000000000011'::uuid],decode(repeat('cd',32),'hex'))
		`, []any{expected.agentRunID, expected.knowledgeBaseID, expected.wikiID, documentationRunID}},
		{`
			INSERT INTO agent_run_knowledge_bases(
				run_id,position,knowledge_base_id,knowledge_base_version,access_policy,
				wiki_version_id,documentation_run_id,source_revision_ids,source_scope_digest
			) VALUES($1,1,$2,2,'PUBLIC',$3,$4,ARRAY['10000000-0000-4000-8000-000000000012'::uuid],decode(repeat('ef',32),'hex'))
		`, []any{expected.agentRunID, expected.secondaryKnowledgeBaseID, expected.secondaryWikiID, secondaryDocumentationRunID}},
	}
	for _, statement := range statements {
		if _, err := tx.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed backup state: %v", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	expected.agentSnapshot = readBackupSnapshot(t, ctx, connection, `
		SELECT jsonb_build_object(
			'agent',to_jsonb(agent),
			'version',to_jsonb(version),
			'memberships',coalesce((
				SELECT jsonb_agg(to_jsonb(membership) ORDER BY membership.position)
				FROM agent_version_knowledge_bases AS membership
				WHERE membership.agent_id=agent.id AND membership.agent_version_id=version.id
			),'[]'::jsonb)
		)::text
		FROM agents AS agent
		JOIN agent_versions AS version ON version.agent_id=agent.id AND version.id=agent.current_version_id
		WHERE agent.id=$1
	`, expected.agentID)
	expected.runSnapshot = readBackupSnapshot(t, ctx, connection, `
		SELECT jsonb_build_object(
			'run',to_jsonb(run),
			'scopes',coalesce((
				SELECT jsonb_agg(to_jsonb(scope) ORDER BY scope.position)
				FROM agent_run_knowledge_bases AS scope
				WHERE scope.run_id=run.id
			),'[]'::jsonb)
		)::text
		FROM agent_runs AS run WHERE run.id=$1
	`, expected.agentRunID)
	var configured struct {
		Memberships []struct {
			Position        int    `json:"position"`
			KnowledgeBaseID string `json:"knowledge_base_id"`
		} `json:"memberships"`
	}
	if err := json.Unmarshal([]byte(expected.agentSnapshot), &configured); err != nil || len(configured.Memberships) != 2 ||
		configured.Memberships[0].Position != 0 || configured.Memberships[0].KnowledgeBaseID != expected.knowledgeBaseID ||
		configured.Memberships[1].Position != 1 || configured.Memberships[1].KnowledgeBaseID != expected.secondaryKnowledgeBaseID {
		t.Fatalf("seeded ordered Agent membership is invalid: %#v, %v", configured.Memberships, err)
	}
	var seeded struct {
		Scopes []struct {
			Position           int      `json:"position"`
			KnowledgeBaseID    string   `json:"knowledge_base_id"`
			WikiVersionID      string   `json:"wiki_version_id"`
			DocumentationRunID string   `json:"documentation_run_id"`
			SourceRevisionIDs  []string `json:"source_revision_ids"`
			SourceScopeDigest  string   `json:"source_scope_digest"`
		} `json:"scopes"`
	}
	if err := json.Unmarshal([]byte(expected.runSnapshot), &seeded); err != nil || len(seeded.Scopes) != 2 {
		t.Fatalf("seeded Agent run scope snapshot is invalid: %v", err)
	}
	for position, scope := range seeded.Scopes {
		if scope.Position != position || scope.KnowledgeBaseID == "" || scope.WikiVersionID == "" ||
			scope.DocumentationRunID == "" || len(scope.SourceRevisionIDs) == 0 || scope.SourceScopeDigest == "" {
			t.Fatalf("seeded Agent run scope %d is incomplete: %#v", position, scope)
		}
	}
	return expected
}

func readBackupSnapshot(t *testing.T, ctx context.Context, connection *pgx.Conn, query string, arguments ...any) string {
	t.Helper()
	var snapshot string
	if err := connection.QueryRow(ctx, query, arguments...).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func writeBackupWikiArtifact(t *testing.T, dataRoot, artifactKey, runID, title string, page []byte) ([]byte, [32]byte) {
	t.Helper()
	artifactRoot := filepath.Join(dataRoot, filepath.FromSlash(artifactKey))
	if err := os.MkdirAll(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest, err := json.Marshal(map[string]any{
		"format": "ref0-page-manifest/v1",
		"pages":  []any{map[string]any{"path": "index.md", "slug": "index", "title": title}},
		"run_id": runID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(artifactRoot, "index.md"), page, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(artifactRoot, ".page-manifest.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	return manifest, sha256.Sum256(manifest)
}

func verifyBackupState(t *testing.T, ctx context.Context, databaseURL, dataRoot string, expected backupExpectation) {
	t.Helper()
	connection, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(ctx)
	var keyID, masked string
	var nonce, ciphertext []byte
	var secretVersion int32
	if err := connection.QueryRow(ctx, `
		SELECT key_id,nonce,ciphertext,secret_version,masked_value
		FROM credentials WHERE id=$1
	`, expected.credentialID).Scan(&keyID, &nonce, &ciphertext, &secretVersion, &masked); err != nil ||
		keyID != expected.credentialKeyID || !bytes.Equal(nonce, expected.credentialNonce) ||
		!bytes.Equal(ciphertext, expected.ciphertext) || secretVersion != expected.credentialVersion ||
		masked != credentials.MaskedValue {
		t.Fatalf("restored encrypted credential differs: key=%q version=%d, %v", keyID, secretVersion, err)
	}
	vault, err := security.NewCredentialVault(os.Getenv("APP_MASTER_KEY"), os.Getenv("APP_PREVIOUS_MASTER_KEYS"))
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	credentialID, err := credentials.ParseID(expected.credentialID)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := credentials.NewSecretReader(pool, vault)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := reader.Read(ctx, credentialID, credentials.ProviderAPIKey, expected.credentialVersion)
	if err != nil || plaintext.Reveal() != expected.credentialPlaintext {
		t.Fatalf("restored credential decryption failed: %v", err)
	}
	var endpointName string
	var headers []byte
	if err := connection.QueryRow(ctx, `SELECT display_name,headers FROM provider_endpoints WHERE id=$1`, expected.endpointID).Scan(&endpointName, &headers); err != nil || endpointName != "Restored endpoint" || !bytes.Contains(headers, []byte("X-Restore-Proof")) {
		t.Fatalf("restored endpoint differs: %v", err)
	}
	var name, wikiID string
	if err := connection.QueryRow(ctx, `SELECT name,published_wiki_id::text FROM knowledge_bases WHERE id=$1`, expected.knowledgeBaseID).Scan(&name, &wikiID); err != nil || name != "Restored knowledge base" || wikiID != expected.wikiID {
		t.Fatalf("restored knowledge base differs: %v", err)
	}
	for _, wiki := range []struct {
		id, artifactKey string
		digest          [32]byte
	}{
		{id: expected.wikiID, artifactKey: expected.artifactKey, digest: expected.manifestDigest},
		{id: expected.secondaryWikiID, artifactKey: expected.secondaryArtifactKey, digest: expected.secondaryManifestDigest},
	} {
		var artifactKey string
		var digest []byte
		if err := connection.QueryRow(ctx, `
			SELECT artifact_key,manifest_sha256 FROM wiki_versions WHERE id=$1
		`, wiki.id).Scan(&artifactKey, &digest); err != nil || artifactKey != wiki.artifactKey || !bytes.Equal(digest, wiki.digest[:]) {
			t.Fatalf("restored wiki %s publication manifest differs: %v", wiki.id, err)
		}
	}
	restoredAgentSnapshot := readBackupSnapshot(t, ctx, connection, `
		SELECT jsonb_build_object(
			'agent',to_jsonb(agent),
			'version',to_jsonb(version),
			'memberships',coalesce((
				SELECT jsonb_agg(to_jsonb(membership) ORDER BY membership.position)
				FROM agent_version_knowledge_bases AS membership
				WHERE membership.agent_id=agent.id AND membership.agent_version_id=version.id
			),'[]'::jsonb)
		)::text
		FROM agents AS agent
		JOIN agent_versions AS version ON version.agent_id=agent.id AND version.id=agent.current_version_id
		WHERE agent.id=$1
	`, expected.agentID)
	if restoredAgentSnapshot != expected.agentSnapshot {
		t.Fatalf("restored complete Agent configuration differs\nsource:   %s\nrestored: %s", expected.agentSnapshot, restoredAgentSnapshot)
	}
	restoredRunSnapshot := readBackupSnapshot(t, ctx, connection, `
		SELECT jsonb_build_object(
			'run',to_jsonb(run),
			'scopes',coalesce((
				SELECT jsonb_agg(to_jsonb(scope) ORDER BY scope.position)
				FROM agent_run_knowledge_bases AS scope
				WHERE scope.run_id=run.id
			),'[]'::jsonb)
		)::text
		FROM agent_runs AS run WHERE run.id=$1
	`, expected.agentRunID)
	if restoredRunSnapshot != expected.runSnapshot {
		t.Fatalf("restored complete Agent run receipt differs\nsource:   %s\nrestored: %s", expected.runSnapshot, restoredRunSnapshot)
	}
	rows, err := connection.Query(ctx, `
		SELECT position,source_scope_digest
		FROM agent_run_knowledge_bases WHERE run_id=$1 ORDER BY position
	`, expected.agentRunID)
	if err != nil {
		t.Fatal(err)
	}
	position := 0
	for rows.Next() {
		var storedPosition int
		var digest []byte
		if err = rows.Scan(&storedPosition, &digest); err != nil || storedPosition != position ||
			position >= len(expected.scopeDigests) || !bytes.Equal(digest, expected.scopeDigests[position][:]) {
			rows.Close()
			t.Fatalf("restored Agent scope manifest %d differs: %v", position, err)
		}
		position++
	}
	if err = rows.Err(); err != nil || position != len(expected.scopeDigests) {
		rows.Close()
		t.Fatalf("restored Agent scope manifest count = %d, %v", position, err)
	}
	rows.Close()
	for _, artifact := range []struct {
		key      string
		manifest []byte
		digest   [32]byte
		page     []byte
	}{
		{key: expected.artifactKey, manifest: expected.manifest, digest: expected.manifestDigest, page: expected.page},
		{key: expected.secondaryArtifactKey, manifest: expected.secondaryManifest, digest: expected.secondaryManifestDigest, page: expected.secondaryPage},
	} {
		root := filepath.Join(dataRoot, filepath.FromSlash(artifact.key))
		manifest, readErr := os.ReadFile(filepath.Join(root, ".page-manifest.json"))
		if readErr != nil || !bytes.Equal(manifest, artifact.manifest) || sha256.Sum256(manifest) != artifact.digest {
			t.Fatalf("restored artifact manifest %s differs: %v", artifact.key, readErr)
		}
		page, readErr := os.ReadFile(filepath.Join(root, "index.md"))
		if readErr != nil || !bytes.Equal(page, artifact.page) {
			t.Fatalf("restored artifact page %s differs: %v", artifact.key, readErr)
		}
	}
}

func archiveDirectory(t *testing.T, root string) []byte {
	t.Helper()
	var result bytes.Buffer
	gzipWriter := gzip.NewWriter(&result)
	tarWriter := tar.NewWriter(gzipWriter)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		if err := tarWriter.WriteHeader(header); err != nil || entry.IsDir() {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	if closeErr := tarWriter.Close(); err == nil {
		err = closeErr
	}
	if closeErr := gzipWriter.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func extractArchive(t *testing.T, archive []byte, root string) {
	t.Helper()
	gzipReader, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(root, filepath.FromSlash(header.Name))
		relative, err := filepath.Rel(root, target)
		if err != nil || filepath.IsAbs(relative) || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatal("backup archive path escaped its restore root")
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			_, copyErr := io.CopyN(file, reader, header.Size)
			closeErr := file.Close()
			if err := errors.Join(copyErr, closeErr); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatal("backup archive contains an unsupported entry")
		}
	}
}

func randomToken(t *testing.T, length int) string {
	t.Helper()
	return base64.RawURLEncoding.EncodeToString(randomBytes(t, length))
}

func randomBytes(t *testing.T, length int) []byte {
	t.Helper()
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return value
}
