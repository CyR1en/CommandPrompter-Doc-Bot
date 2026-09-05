package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"github.com/cyr1en/ref0/internal/sourcefiles"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/cyr1en/ref0/db/migrations"
	"github.com/cyr1en/ref0/internal/artifacts"
	docgen "github.com/cyr1en/ref0/internal/documentation"
	"github.com/cyr1en/ref0/internal/jobs"
	"github.com/cyr1en/ref0/internal/security"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestDocumentationRoutesPostgreSQLGenerationAndReadBoundary(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	migrateDocumentationAPIDatabase(t, ctx, databaseURL)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err = pool.Exec(ctx, `TRUNCATE operators,knowledge_bases,sources,source_revisions,jobs,event_log,audit_events,idempotency_records RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}

	authenticated := fixedAuthenticatedSession(t)
	actorID := docgen.ID(authenticated.Session.Operator.ID)
	kbID := mustDocumentationID(t, "d0000000-0000-4000-8000-00000000000d")
	sourceID := mustDocumentationID(t, "e0000000-0000-4000-8000-00000000000e")
	revisionID := mustDocumentationID(t, "f0000000-0000-4000-8000-00000000000f")
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO operators(id,username,username_key,password_hash) VALUES($1,'Documentation Operator','documentation operator','unused')`, []any{actorID.String()}},
		{`INSERT INTO knowledge_bases(id,name,name_key,access_policy,lifecycle,instructions,language,version) VALUES($1,'Documentation API KB','documentation api kb','RESTRICTED','ACTIVE','Use exact evidence.','en',1)`, []any{kbID.String()}},
		{`INSERT INTO sources(id,knowledge_base_id,kind,display_name,display_key,privacy,lifecycle,health,checked_at,version,configuration_version,validated_configuration_version) VALUES($1,$2,'REPOSITORY','Docs','docs','PUBLIC','ACTIVE','HEALTHY',clock_timestamp(),2,1,1)`, []any{sourceID.String(), kbID.String()}},
		{`INSERT INTO source_revisions(id,source_id,observed_ref_kind,observed_ref,native_version,fingerprint,artifact_key,file_count,byte_count,ignored_paths) VALUES($1,$2,'BRANCH','main',$3,$4,$5,1,24,'[]'::jsonb)`, []any{revisionID.String(), sourceID.String(), strings.Repeat("a", 40), bytes.Repeat([]byte{'f'}, 32), "sources/" + sourceID.String() + "/snapshots/" + revisionID.String()}},
		{`UPDATE sources SET current_revision_id=$2 WHERE id=$1`, []any{sourceID.String(), revisionID.String()}},
	}
	for _, statement := range statements {
		if _, err = pool.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}

	vault, err := security.NewCredentialVault("active:"+base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "")
	if err != nil {
		t.Fatal(err)
	}
	dataRoot := t.TempDir()
	runArtifacts, err := artifacts.NewRunStore(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	wikiArtifacts, err := artifacts.NewWikiStore(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	queue := jobs.NewStore(pool, docgen.TerminalCallback)
	evidenceArtifacts, err := sourcefiles.NewStore(dataRoot)
	if err != nil {
		t.Fatal(err)
	}
	store, err := docgen.NewStore(pool, queue, vault, runArtifacts, wikiArtifacts, evidenceArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	handler := documentationRoutesTestHandler(t, &fakeSessionService{session: authenticated.Session}, store, queue)
	headers := map[string]string{
		"Cookie": sessionCookie(authenticated.Token.Reveal()), csrfHeaderName: authenticated.CSRFToken,
		"Idempotency-Key": "postgres-generate",
	}
	generatePath := "/api/v1/knowledge-bases/" + kbID.String() + "/generate"
	generated := authRequest(t, handler, http.MethodPost, generatePath, `{"expected_version":1}`, headers)
	if generated.Code != http.StatusAccepted {
		t.Fatalf("generated=%d %s", generated.Code, generated.Body.String())
	}
	replayed := authRequest(t, handler, http.MethodPost, generatePath, `{"expected_version":1}`, headers)
	if replayed.Code != http.StatusAccepted || replayed.Body.String() != generated.Body.String() {
		t.Fatalf("replayed=%d %s first=%s", replayed.Code, replayed.Body.String(), generated.Body.String())
	}

	listed := authRequest(t, handler, http.MethodGet, documentationRunsPath+"?knowledge_base_id="+kbID.String(), "", map[string]string{"Cookie": headers["Cookie"]})
	if listed.Code != http.StatusOK {
		t.Fatalf("listed=%d %s", listed.Code, listed.Body.String())
	}
	var runs []DocumentationRunResponse
	if err = json.Unmarshal(listed.Body.Bytes(), &runs); err != nil || len(runs) != 1 || runs[0].Status != "preparing" {
		t.Fatalf("runs=%+v err=%v body=%s", runs, err, listed.Body.String())
	}
	fetched := authRequest(t, handler, http.MethodGet, documentationRunsPath+"/"+runs[0].ID, "", map[string]string{"Cookie": headers["Cookie"]})
	if fetched.Code != http.StatusOK || !strings.Contains(fetched.Body.String(), `"knowledge_base_id":"`+kbID.String()+`"`) {
		t.Fatalf("fetched=%d %s", fetched.Code, fetched.Body.String())
	}

	wikiPath := "/api/v1/knowledge-bases/" + kbID.String() + "/wiki"
	versions := authRequest(t, handler, http.MethodGet, wikiPath+"/versions", "", map[string]string{"Cookie": headers["Cookie"]})
	if versions.Code != http.StatusOK || strings.TrimSpace(versions.Body.String()) != "[]" {
		t.Fatalf("versions=%d %s", versions.Code, versions.Body.String())
	}
	for _, path := range []string{wikiPath, wikiPath + "/export"} {
		response := authRequest(t, handler, http.MethodGet, path, "", map[string]string{"Cookie": headers["Cookie"]})
		if response.Code != http.StatusNotFound || problemDetail(t, response) != "Documentation resource was not found." {
			t.Fatalf("%s=%d %s", path, response.Code, response.Body.String())
		}
	}
}

func migrateDocumentationAPIDatabase(t *testing.T, ctx context.Context, databaseURL string) {
	t.Helper()
	database, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err = database.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	goose.SetBaseFS(migrations.FS)
	if err = goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err = goose.UpContext(ctx, database, "."); err != nil {
		t.Fatal(err)
	}
}
