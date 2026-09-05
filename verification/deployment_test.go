package verification

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLiveRuntimeIdentityIsRef0Only(t *testing.T) {
	for _, root := range []string{"../cmd", "../internal", "../db", "../frontend/src", "../frontend/e2e"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(strings.ToLower(string(content)), "cmdp") ||
				strings.Contains(strings.ToLower(string(content)), "commandprompter") {
				t.Errorf("%s retains the retired runtime identity", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"Dockerfile", "docker-compose.yml", "docker-compose.portainer.yml", ".env.example"} {
		content := strings.ToLower(repositoryFile(t, name))
		if strings.Contains(content, "cmdp") || strings.Contains(content, "commandprompter") {
			t.Errorf("%s retains the retired runtime identity", name)
		}
	}
	if _, err := os.Lstat("../ref0"); !os.IsNotExist(err) {
		t.Fatalf("repository-root ref0 build artifact exists: %v", err)
	}
	if !strings.Contains(repositoryFile(t, ".gitignore"), "\n/ref0\n") {
		t.Fatal("repository-root ref0 build artifact is not ignored")
	}
}

func TestApplicationImageIsGoOnlyAndNonRoot(t *testing.T) {
	dockerfile := repositoryFile(t, "Dockerfile")
	for _, required := range []string{
		"FROM golang:1.27.0-bookworm AS go-build",
		"CGO_ENABLED=0 GOOS=linux go build",
		"-o /out/ref0 ./cmd/ref0",
		"COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/licenses/ref0/",
		"USER ref0",
		`ENTRYPOINT ["/usr/local/bin/ref0"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("application Dockerfile is missing %q", required)
		}
	}
	for _, forbidden := range []string{"FROM python:", "pip install", "requirements.txt", "uvicorn", "alembic"} {
		if strings.Contains(dockerfile, forbidden) {
			t.Fatalf("application Dockerfile retains Python runtime text %q", forbidden)
		}
	}
}

func TestDockerBuildContextKeepsRequiredVerificationInputs(t *testing.T) {
	dockerignore := repositoryFile(t, ".dockerignore")
	for _, required := range []string{"capsule/test/", "verification/", "verify/", "**/*.test.ts"} {
		if hasDockerignorePattern(dockerignore, required) {
			t.Fatalf(".dockerignore excludes build input %q", required)
		}
	}
}

func TestComposeRunsOnlyRef0CommandsAndKeepsTwoIsolatedCapsules(t *testing.T) {
	for _, name := range []string{"docker-compose.yml", "docker-compose.portainer.yml"} {
		compose := repositoryFile(t, name)
		for _, command := range []string{`command: ["migrate", "up"]`, `command: ["api"]`, `command: ["worker"]`, `command: ["discord"]`} {
			if !strings.Contains(compose, command) {
				t.Fatalf("%s is missing %q", name, command)
			}
		}
		for _, forbidden := range []string{"python", "uvicorn", "alembic", "CMDP_IMAGE", "CMDP_CAPSULE_IMAGE"} {
			if strings.Contains(compose, forbidden) {
				t.Fatalf("%s retains obsolete runtime text %q", name, forbidden)
			}
		}
		for _, invariant := range []string{
			"network_mode: none", "read_only: true", "cap_drop: [ALL]",
			"security_opt: [no-new-privileges:true]", "pids_limit: 64",
			"init: true", "pids_limit: 256", "mem_limit: 768m",
			"pids_limit: 512", "mem_limit: 1g", "shm_size: 256m",
			"pids_limit: 128", "mem_limit: 512m", "mem_limit: 384m",
			"capsule_slot_0_socket", "capsule_slot_1_socket",
			`["/run/capsule-slot-0/capsule.sock","/run/capsule-slot-1/capsule.sock"]`,
		} {
			if !strings.Contains(compose, invariant) {
				t.Fatalf("%s is missing capsule invariant %q", name, invariant)
			}
		}
		const migrateEnvironment = "  migrate:\n    <<: *runtime\n    command: [\"migrate\", \"up\"]\n    environment:\n      <<: *database-environment"
		if !strings.Contains(compose, migrateEnvironment) {
			t.Fatalf("%s gives migrate more than the database environment", name)
		}
	}
}

func TestCapsuleImagePinsRevisionAndPrivilegeBoundary(t *testing.T) {
	dockerfile := repositoryFile(t, "capsule", "Dockerfile")
	for _, required := range []string{
		"FROM golang:1.27.0-bookworm AS supervisor-build",
		"FROM node:22.19.0-bookworm-slim AS capsule-build",
		`LABEL org.opencontainers.image.version="pi-0.84.4-r9"`,
		"apt-get upgrade -y",
		"rm -rf /var/lib/apt/lists/*",
		"/usr/local/lib/node_modules/npm",
		"/usr/local/lib/node_modules/corepack",
		"npm prune --omit=dev --ignore-scripts",
		"COPY --from=capsule-build /build/dist/src /opt/capsule/dist/src",
		"COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/licenses/ref0/",
		`ENTRYPOINT ["/usr/local/bin/capsule-supervisor"]`,
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("capsule Dockerfile is missing %q", required)
		}
	}
	if strings.Contains(dockerfile, "USER capsule") || strings.Contains(dockerfile, "/build/dist /opt/capsule/dist") {
		t.Fatal("capsule image weakened its supervisor boundary or copied test output")
	}
}

func TestThirdPartyNoticeBundleCoversDistributedArtifacts(t *testing.T) {
	notices := repositoryFile(t, "THIRD_PARTY_NOTICES.md")
	for _, required := range []string{
		"Go runtime and standard library | 1.27.0",
		"github.com/bwmarrin/discordgo | v0.29.0",
		"github.com/prometheus/client_golang | v1.24.1",
		"@fontsource-variable/jetbrains-mono | 5.3.0",
		"Copyright 2020 The JetBrains Mono Project Authors",
		"@fontsource-variable/outfit | 5.3.0",
		"Copyright 2021 The Outfit Project Authors",
		"Vite module-preload runtime | 8.2.2",
		"Rolldown-generated module helpers | 1.2.6",
		"@earendil-works/pi-agent-core, @earendil-works/pi-ai, @earendil-works/pi-telemetry | 0.84.4",
		"Copyright (c) 2025 Mario Zechner",
		"Version 2.0, January 2004",
		"SIL OPEN FONT LICENSE Version 1.1",
	} {
		if !strings.Contains(notices, required) {
			t.Fatalf("third-party notice bundle is missing %q", required)
		}
	}
}

func repositoryFile(t *testing.T, parts ...string) string {
	t.Helper()
	pathParts := append([]string{".."}, parts...)
	content, err := os.ReadFile(filepath.Join(pathParts...))
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func hasDockerignorePattern(content, wanted string) bool {
	for line := range strings.SplitSeq(content, "\n") {
		if strings.TrimSpace(line) == wanted {
			return true
		}
	}
	return false
}
