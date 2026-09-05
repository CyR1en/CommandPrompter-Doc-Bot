import { spawnSync } from "node:child_process";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const frontend = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const repository = resolve(frontend, "..");
const openAPI = resolve(frontend, "openapi.json");
const schema = resolve(frontend, "src/api/schema.d.ts");
const go = process.env.GO_BINARY || "go";
const goDockerImage = process.env.REF0_GO_DOCKER_IMAGE;
const mode = process.argv[2] || "generate";

function run(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: repository,
    env: process.env,
    stdio: "inherit",
    ...options,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) process.exit(result.status ?? 1);
}

function runGo(args, environment = {}) {
  if (!goDockerImage) {
    run(go, args, { env: { ...process.env, ...environment } });
    return;
  }
  const dockerArgs = [
    "run",
    "--rm",
    "-v",
    `${repository}:/workspace`,
    "-w",
    "/workspace",
  ];
  if (environment.REF0_OPENAPI_OUTPUT) {
    dockerArgs.push("-e", "REF0_OPENAPI_OUTPUT=/workspace/frontend/openapi.json");
  }
  dockerArgs.push(goDockerImage, "go", ...args);
  run("docker", dockerArgs);
}

function check() {
  runGo([
    "test",
    "./internal/api",
    "-run",
    "^TestControlPlaneOpenAPIContract$",
    "-count=1",
  ]);
}

if (mode === "check") {
  check();
} else if (mode === "generate") {
  runGo([
    "test",
    "./internal/api",
    "-run",
    "^TestWriteControlPlaneOpenAPI$",
    "-count=1",
  ], { REF0_OPENAPI_OUTPUT: openAPI });
  run(process.execPath, [
    resolve(frontend, "node_modules/openapi-typescript/bin/cli.js"),
    openAPI,
    "-o",
    schema,
  ], { cwd: frontend });
  check();
} else {
  process.stderr.write(`unknown OpenAPI contract mode: ${mode}\n`);
  process.exit(2);
}
