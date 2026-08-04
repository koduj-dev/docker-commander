/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { MemoryRouter } from "react-router-dom";
import { useDocumentTitle, APP_NAME } from "./title";
import { AuthShell } from "../pages/AuthShell";
import { PageHeader } from "../layout/Shell";

// The pure composition is covered in title.test.ts. What needs a DOM is the part
// that actually reaches the browser: that mounting a screen sets document.title,
// that unmounting puts back what was there, and — the bit worth the dependency —
// that the three components which are supposed to call the hook really do.

vi.mock("./api", () => ({
  api: { hosts: () => Promise.resolve([]), version: () => Promise.resolve({ version: "test" }) },
}));

function Screen({ title }: { title?: string | null }) {
  useDocumentTitle(title);
  return null;
}

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  document.title = APP_NAME;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function render(ui: React.ReactNode) {
  act(() => root.render(ui));
}

describe("useDocumentTitle", () => {
  it("names the tab when a screen mounts", () => {
    render(<Screen title="Images" />);
    expect(document.title).toBe(`Images · ${APP_NAME}`);
  });

  it("follows a title that changes without remounting", () => {
    render(<Screen title="Containers" />);
    render(<Screen title="queue-worker" />);
    expect(document.title).toBe(`queue-worker · ${APP_NAME}`);
  });

  // A screen that mounts briefly — a redirect through the login shell, say — must
  // not leave its name on the tab it navigated away to.
  it("restores the previous title on unmount", () => {
    document.title = `Dashboard · ${APP_NAME}`;
    render(<Screen title="Sign in" />);
    expect(document.title).toBe(`Sign in · ${APP_NAME}`);

    act(() => root.render(null));
    expect(document.title).toBe(`Dashboard · ${APP_NAME}`);
  });

  it("falls back to the bare app name for an empty title", () => {
    render(<Screen title="" />);
    expect(document.title).toBe(APP_NAME);
  });
});

// The wiring, not the helper. Every agenda gets its tab name from PageHeader and
// every auth screen from AuthShell, so if either stops calling the hook the whole
// feature silently reverts to "Docker Commander" everywhere — with the unit tests
// still green, which is exactly the hole this closes.
describe("the components that own the tab name", () => {
  it("AuthShell names the tab (sign-in, 2FA, first run)", () => {
    render(
      <AuthShell title="Create your admin account" subtitle="This is the first run.">
        <div />
      </AuthShell>,
    );
    expect(document.title).toBe(`Create your admin account · ${APP_NAME}`);
  });

  it("PageHeader names the tab (every agenda)", () => {
    render(
      <MemoryRouter initialEntries={["/images"]}>
        <PageHeader title="Images" />
      </MemoryRouter>,
    );
    expect(document.title).toBe(`Images · ${APP_NAME}`);
  });
});
