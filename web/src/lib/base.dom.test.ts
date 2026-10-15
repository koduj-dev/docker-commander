/** @vitest-environment happy-dom */
import { describe, it, expect, afterEach } from "vitest";
import { readBasePath, withBase, wsUrl } from "./base";

function setBase(href: string | null) {
  document.head.querySelectorAll("base").forEach((b) => b.remove());
  if (href !== null) {
    const b = document.createElement("base");
    b.setAttribute("href", href);
    document.head.prepend(b);
  }
}

describe("readBasePath", () => {
  afterEach(() => setBase(null));

  it("is empty without a <base> (Vite dev server)", () => {
    setBase(null);
    expect(readBasePath(document)).toBe("");
  });

  it("is empty at the root", () => {
    setBase("/");
    expect(readBasePath(document)).toBe("");
  });

  it("reads the prefix without its trailing slash", () => {
    setBase("/dockercmd/");
    expect(readBasePath(document)).toBe("/dockercmd");
    setBase("/tools/dockercmd/");
    expect(readBasePath(document)).toBe("/tools/dockercmd");
  });

  it("ignores anything that is not a plain absolute path", () => {
    for (const href of ["//evil.example/", "https://evil.example/", "javascript:alert(1)", "relative/"]) {
      setBase(href);
      expect(readBasePath(document)).toBe("");
    }
  });
});

describe("withBase / wsUrl", () => {
  it("prefixes root-relative paths only", () => {
    expect(withBase("/api/auth/me", "/dockercmd")).toBe("/dockercmd/api/auth/me");
    expect(withBase("/api/auth/me", "")).toBe("/api/auth/me");
    expect(withBase("blob:x", "/dockercmd")).toBe("blob:x");
  });

  it("builds a WebSocket URL on this host under the prefix", () => {
    const proto = location.protocol === "https:" ? "wss" : "ws";
    expect(wsUrl("/api/ws", "/dockercmd")).toBe(`${proto}://${location.host}/dockercmd/api/ws`);
  });
});
