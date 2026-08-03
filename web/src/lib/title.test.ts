import { describe, it, expect } from "vitest";
import { pageTitle, APP_NAME } from "./title";

describe("pageTitle", () => {
  it("puts the page first, so the tab is distinguishable when several are open", () => {
    expect(pageTitle("Images")).toBe(`Images · ${APP_NAME}`);
    expect(pageTitle("MCP Access")).toBe(`MCP Access · ${APP_NAME}`);
  });

  it("falls back to the bare app name rather than rendering a dangling separator", () => {
    expect(pageTitle("")).toBe(APP_NAME);
    expect(pageTitle("   ")).toBe(APP_NAME);
    expect(pageTitle(undefined)).toBe(APP_NAME);
    expect(pageTitle(null)).toBe(APP_NAME);
  });

  it("does not repeat the app name when a screen is already called that", () => {
    expect(pageTitle(APP_NAME)).toBe(APP_NAME);
  });

  it("trims, so a padded title cannot smuggle whitespace into the tab", () => {
    expect(pageTitle("  Volumes  ")).toBe(`Volumes · ${APP_NAME}`);
  });

  it("keeps a dynamic title verbatim — container names are the point of it", () => {
    expect(pageTitle("dc-test-nginx")).toBe(`dc-test-nginx · ${APP_NAME}`);
  });
});
