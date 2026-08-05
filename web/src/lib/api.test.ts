import { describe, it, expect, vi, afterEach } from "vitest";
import { api, ApiError } from "./api";

const originalFetch = globalThis.fetch;
afterEach(() => {
  globalThis.fetch = originalFetch;
  vi.restoreAllMocks();
});

function respondWith(status: number, body: string, ok = status < 400) {
  globalThis.fetch = vi.fn().mockResolvedValue({
    ok,
    status,
    statusText: status === 502 ? "Bad Gateway" : "Error",
    text: () => Promise.resolve(body),
  }) as unknown as typeof fetch;
}

describe("the API client", () => {
  // Parsing before checking res.ok meant an error page from something that isn't
  // this app — a reverse proxy's 502 HTML — threw SyntaxError instead of the
  // ApiError every caller expects. The UI showed "Unexpected token '<'" instead
  // of the status, and code branching on `e instanceof ApiError` took the wrong
  // path.
  it("reports the status when the body is not JSON", async () => {
    respondWith(502, "<html><body>502 Bad Gateway</body></html>");
    await expect(api.version()).rejects.toBeInstanceOf(ApiError);

    respondWith(502, "<html><body>502 Bad Gateway</body></html>");
    await expect(api.version()).rejects.toMatchObject({ status: 502, message: "Bad Gateway" });
  });

  it("prefers the server's own error message when there is one", async () => {
    respondWith(403, JSON.stringify({ error: "read-only" }));
    await expect(api.version()).rejects.toMatchObject({ status: 403, message: "read-only" });
  });

  it("still returns parsed JSON on success", async () => {
    respondWith(200, JSON.stringify({ version: "1.2.3" }));
    await expect(api.version()).resolves.toEqual({ version: "1.2.3" });
  });

  it("treats an empty body as no data rather than an error", async () => {
    respondWith(200, "");
    await expect(api.version()).resolves.toBeNull();
  });
});
