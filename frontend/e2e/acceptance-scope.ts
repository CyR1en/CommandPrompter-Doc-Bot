import { expect } from "@playwright/test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

export const exactApplicationImage = "ref0-accept-e2e:local";
export const exactProject = "ref0-accept-e2e";
export const providerProxyName = `${exactProject}-provider-proxy`;
export const secretVerifierName = `${exactProject}-secret-scan`;

const execFileAsync = promisify(execFile);

export type AcceptanceScope = {
  apiPort: number;
  apiUrl: string;
  bootstrapToken: string;
  databaseName: "ref0_accept";
  databasePassword: string;
  databasePort: number;
  databaseUser: "ref0";
  password: string;
  project: typeof exactProject;
  username: string;
};

type ContainerExpectation = {
  application?: boolean;
  containerPort?: number;
  hostPort?: number;
  image: string;
  user?: string;
};

export function acceptanceScope(): AcceptanceScope | null {
  const apiUrl = process.env.CONTROL_PLANE_API_URL;
  const bootstrapToken = process.env.APP_BOOTSTRAP_TOKEN;
  const databaseName = process.env.POSTGRES_DB;
  const databasePassword = process.env.POSTGRES_PASSWORD;
  const databaseUser = process.env.POSTGRES_USER;
  const password = process.env.CONTROL_PLANE_OPERATOR_PASSWORD;
  const project = process.env.COMPOSE_PROJECT_NAME;
  const username = process.env.CONTROL_PLANE_OPERATOR_USERNAME;
  const apiPort = parsePort(process.env.API_PORT);
  const databasePort = parsePort(process.env.POSTGRES_PORT);

  if (
    process.env.REF0_BROWSER_ACCEPTANCE !== "1"
    || !apiUrl
    || !bootstrapToken
    || !databasePassword
    || !password
    || !username
    || project !== exactProject
    || databaseName !== "ref0_accept"
    || databaseUser !== "ref0"
    || apiPort === null
    || databasePort === null
    || apiPort === 8_000
    || databasePort === 5_432
    || apiPort === databasePort
    || !isScopedApiUrl(apiUrl, apiPort)
  ) {
    return null;
  }

  return {
    apiPort,
    apiUrl,
    bootstrapToken,
    databaseName,
    databasePassword,
    databasePort,
    databaseUser,
    password,
    project,
    username,
  };
}

export async function assertScopedContainer(
  scope: AcceptanceScope,
  service: string,
  expected: ContainerExpectation,
): Promise<string> {
  const name = `${scope.project}-${service}-1`;
  const { stdout } = await execFileAsync("docker", ["inspect", name]);
  const inspected: unknown = JSON.parse(stdout);
  if (!Array.isArray(inspected) || inspected.length !== 1 || !isRecord(inspected[0])) {
    throw new Error(`acceptance container ${service} inspection is invalid`);
  }
  const container = inspected[0];
  const config = requiredRecord(container.Config, `${service} configuration`);
  const labels = requiredRecord(config.Labels, `${service} labels`);
  const state = requiredRecord(container.State, `${service} state`);
  const network = requiredRecord(container.NetworkSettings, `${service} network`);
  const ports = requiredRecord(network.Ports, `${service} ports`);

  expect(labels["com.docker.compose.project"]).toBe(scope.project);
  expect(labels["com.docker.compose.service"]).toBe(service);
  expect(state.Status).toBe("running");
  expect(config.Image).toBe(expected.image);

  const { stdout: imageID } = await execFileAsync("docker", [
    "image",
    "inspect",
    "--format",
    "{{.Id}}",
    expected.image,
  ]);
  expect(container.Image).toBe(imageID.trim());

  if (expected.application === true) {
    expect(config.Entrypoint).toEqual(["/usr/local/bin/ref0"]);
  }
  if (expected.user !== undefined) {
    expect(config.User).toBe(expected.user);
  }

  if (expected.containerPort === undefined || expected.hostPort === undefined) {
    expect(Object.keys(ports)).toHaveLength(0);
  } else {
    const binding = ports[`${expected.containerPort}/tcp`];
    if (!Array.isArray(binding) || binding.length !== 1 || !isRecord(binding[0])) {
      throw new Error(`acceptance container ${service} port binding is invalid`);
    }
    expect(binding[0].HostIp).toBe("127.0.0.1");
    expect(binding[0].HostPort).toBe(String(expected.hostPort));
    expect(Object.keys(ports)).toEqual([`${expected.containerPort}/tcp`]);
  }
  return name;
}

function isScopedApiUrl(value: string, port: number): boolean {
  try {
    const parsed = new URL(value);
    return parsed.protocol === "http:"
      && ["127.0.0.1", "localhost", "[::1]"].includes(parsed.hostname)
      && parsed.port === String(port)
      && parsed.pathname === "/"
      && parsed.username === ""
      && parsed.password === ""
      && parsed.search === ""
      && parsed.hash === "";
  } catch {
    return false;
  }
}

function parsePort(value: string | undefined): number | null {
  if (!value || !/^\d+$/u.test(value)) return null;
  const port = Number(value);
  return Number.isInteger(port) && port >= 1_024 && port <= 65_535 ? port : null;
}

function requiredRecord(value: unknown, label: string): Record<string, unknown> {
  if (!isRecord(value)) throw new Error(`acceptance ${label} is invalid`);
  return value;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
