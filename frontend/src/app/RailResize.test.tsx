import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, expect, test, vi } from "vitest";

import { AuthProvider } from "./auth";
import { router } from "./router";

/* jsdom has no layout, so measureIntrinsicRailWidth falls back to 200px; the
   collapse threshold is therefore 200 - 24 = 176px of drag reach. */
const FALLBACK_MIN = 200;
const COLLAPSE_BELOW = 176;

/* jsdom implements no pointer-capture API; the component calls it on drag. */
if (!("setPointerCapture" in Element.prototype)) {
  Object.defineProperty(Element.prototype, "setPointerCapture", {
    configurable: true,
    value: () => undefined,
  });
}

class SilentEventSource {
  constructor(_url: string) {}
  addEventListener(): void {}
  removeEventListener(): void {}
  close(): void {}
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

async function mountShell(): Promise<{ rail: HTMLElement; separator: HTMLElement; shell: HTMLElement }> {
  vi.stubGlobal("fetch", vi.fn(async (request: Request) => {
    const path = new URL(request.url).pathname;
    if (path === "/api/v1/auth/session") {
      return jsonResponse({
        operator: { id: "00000000-0000-0000-0000-000000000001", username: "operator" },
        expires_at: "2026-08-29T00:00:00Z",
        csrf_token: "csrf-memory-only",
      });
    }
    if (path === "/api/v1/overview") {
      return jsonResponse({
        generated_at: "2026-08-29T00:00:00Z",
        unhealthy_sources: [],
        failed_jobs: [],
        knowledge_base_issues: [],
        provider_errors: [],
		agent_failures: [],
      });
    }
    return jsonResponse([]);
  }));
  vi.stubGlobal("EventSource", SilentEventSource);

  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <AuthProvider><RouterProvider router={router} /></AuthProvider>
    </QueryClientProvider>,
  );
  await screen.findByRole("heading", { name: "Today’s operations ledger" });

  const rail = screen.getByRole("navigation", { name: "Primary" });
  const separator = screen.getByRole("separator", { name: "Resize sidebar" });
  const shell = rail.closest(".folio-shell");
  if (!(shell instanceof HTMLElement)) throw new Error("shell is missing");
  /* Anchor the rail's left edge at 0 so drag reach equals clientX. */
  vi.spyOn(rail, "getBoundingClientRect").mockReturnValue(new DOMRect(0, 0, 248, 700));
  return { rail, separator, shell };
}

function drag(separator: HTMLElement, from: number, path: readonly number[]): void {
  fireEvent.pointerDown(separator, { button: 0, clientX: from, pointerId: 1 });
  for (const clientX of path) {
    fireEvent(window, new MouseEvent("pointermove", { bubbles: true, clientX }));
  }
  fireEvent(window, new MouseEvent("pointerup", { bubbles: true }));
}

test("dragging the separator sets the rail width and clamps it to the maximum", async () => {
  const { separator, shell } = await mountShell();

  drag(separator, 248, [300]);
  expect(shell.style.getPropertyValue("--rail-w")).toBe("300px");
  expect(shell.className).not.toContain("is-rail-collapsed");

  drag(separator, 300, [9999]);
  expect(shell.style.getPropertyValue("--rail-w")).toBe("420px");
});

test("the rail never narrows past its longest label, and collapses beyond that", async () => {
  const { separator, shell } = await mountShell();

  /* Just inside the threshold: pinned to the intrinsic minimum, still expanded. */
  drag(separator, 248, [COLLAPSE_BELOW + 2]);
  expect(shell.style.getPropertyValue("--rail-w")).toBe(`${FALLBACK_MIN}px`);
  expect(shell.className).not.toContain("is-rail-collapsed");

  /* Past it: snaps to the rail. */
  drag(separator, 200, [COLLAPSE_BELOW - 2]);
  expect(shell.className).toContain("is-rail-collapsed");
});

test("dragging back out from the collapsed rail restores the labels", async () => {
  const { separator, shell } = await mountShell();

  drag(separator, 248, [80]);
  expect(shell.className).toContain("is-rail-collapsed");

  drag(separator, 68, [260]);
  expect(shell.className).not.toContain("is-rail-collapsed");
  expect(shell.style.getPropertyValue("--rail-w")).toBe("260px");
});

test("the separator resizes and collapses from the keyboard", async () => {
  const { separator, shell } = await mountShell();

  fireEvent.keyDown(separator, { key: "End" });
  expect(shell.style.getPropertyValue("--rail-w")).toBe("420px");

  fireEvent.keyDown(separator, { key: "Home" });
  expect(shell.style.getPropertyValue("--rail-w")).toBe(`${FALLBACK_MIN}px`);

  /* One step below the minimum collapses rather than clipping a label. */
  fireEvent.keyDown(separator, { key: "ArrowLeft" });
  expect(shell.className).toContain("is-rail-collapsed");

  fireEvent.keyDown(separator, { key: "ArrowRight" });
  expect(shell.className).not.toContain("is-rail-collapsed");
});

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}
