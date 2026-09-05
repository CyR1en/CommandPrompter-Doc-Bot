import { expect, test } from "@playwright/test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

import {
  acceptanceScope,
  assertScopedContainer,
  exactApplicationImage,
} from "./acceptance-scope";

const execFileAsync = promisify(execFile);

test("session and a UI-created record survive API restart before logout", async ({ context, page, request }) => {
  const scope = acceptanceScope();
  test.skip(
    scope === null,
    "the exact disposable ref0 acceptance stack and operator credentials are required",
  );
  if (scope === null) return;

  await assertScopedContainer(scope, "api", {
    application: true,
    containerPort: 8_000,
    hostPort: scope.apiPort,
    image: exactApplicationImage,
    user: "ref0",
  });
  await assertScopedContainer(scope, "postgres", {
    containerPort: 5_432,
    hostPort: scope.databasePort,
    image: "postgres:18.6-bookworm",
  });

  await page.goto("/login");
  await page.getByLabel("Username").fill(scope.username);
  await page.getByLabel("Password").fill(scope.password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  const overviewHeading = page.getByRole("heading", { name: "Today’s operations ledger" });
  await Promise.race([
    overviewHeading.waitFor(),
    page.getByRole("alert").waitFor(),
  ]);
  if (!(await overviewHeading.isVisible())) {
    await page.goto("/bootstrap");
    await page.getByLabel("Username").fill(scope.username);
    await page.getByLabel("Password").fill(scope.password);
    await page.getByLabel("Bootstrap token").fill(scope.bootstrapToken);
    await page.getByRole("button", { name: "Create operator" }).click();
  }
  await expect(page.getByRole("heading", { name: "Today’s operations ledger" })).toBeVisible();

  const session = (await context.cookies()).find((cookie) => cookie.name === "ref0_session");
  expect(session?.httpOnly).toBe(true);
  expect(session?.sameSite).toBe("Lax");

  const knowledgeBaseName = `Browser restart proof ${Date.now()}`;
  await page.setViewportSize({ width: 1440, height: 1000 });
  await expect(page.getByRole("link", { name: "Unhealthy sources 0 clear" })).toBeVisible();
  await page.screenshot({ fullPage: true, path: "/tmp/ref0-accept-overview-desktop.png" });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.screenshot({ fullPage: true, path: "/tmp/ref0-accept-overview-mobile.png" });
  const menu = page.getByRole("button", { name: /Operations menu/ });
  await expect(menu).toHaveAttribute("aria-expanded", "false");
  await menu.click();
  await expect(menu).toHaveAttribute("aria-expanded", "true");
  await page.getByRole("link", { exact: true, name: "Knowledge bases" }).click();
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.locator("summary").filter({ hasText: "Create knowledge base" }).click();
  await page.getByLabel("Name").fill(knowledgeBaseName);
  await page.getByRole("button", { name: "Create knowledge base" }).click();
  await expect(page.getByRole("link", { name: new RegExp(knowledgeBaseName) })).toBeVisible();

  await execFileAsync("docker", ["restart", `${scope.project}-api-1`]);
  await expect.poll(async () => {
    try {
      return (await request.get("/health/ready")).status();
    } catch {
      return 0;
    }
  }, { timeout: 30_000 }).toBe(200);
  await assertScopedContainer(scope, "api", {
    application: true,
    containerPort: 8_000,
    hostPort: scope.apiPort,
    image: exactApplicationImage,
    user: "ref0",
  });
  await page.reload();
  await expect(page.getByRole("link", { name: new RegExp(knowledgeBaseName) })).toBeVisible();

  await page.getByRole("link", { exact: true, name: "Settings" }).click();
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  expect((await context.cookies()).some((cookie) => cookie.name === "ref0_session")).toBe(false);
});
