/** @vitest-environment happy-dom */
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { FileBrowser } from "./FileBrowser";
import { DialogProvider } from "./Dialog";
import type { FileApi } from "../lib/types";

// Downloading hands the files over, which needs write access; the server
// refuses it otherwise, and a refused download opens a bare error page rather
// than a file. So without canDownload there is nothing to click.

const fs: FileApi = {
  list: async () => ({ ok: true, path: "/", entries: [{ name: "app.conf", isDir: false, size: 10, mode: "-rw-r--r--", modTime: "", linkTarget: "" } as never] }),
  upload: async () => ({ ok: true }),
  uploadExtract: async () => ({ ok: true }),
  mkdir: async () => ({ ok: true }),
  del: async () => ({ ok: true }),
  downloadUrl: (p) => `/dl${p}`,
};

let container: HTMLDivElement;
let root: Root | undefined;

async function mount(canDownload: boolean) {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
  await act(async () => root!.render(<DialogProvider><FileBrowser fs={fs} canDownload={canDownload} /></DialogProvider>));
}

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container.remove();
});

const titles = () => [...container.querySelectorAll("button")].map((b) => b.getAttribute("title") ?? "");

describe("FileBrowser downloads", () => {
  it("are offered with write access", async () => {
    await mount(true);
    expect(container.textContent).toContain("app.conf");
    expect(titles()).toContain("Download");
    expect(titles()).toContain("Download current directory as tar");
  });

  it("are not offered without it", async () => {
    await mount(false);
    expect(container.textContent).toContain("app.conf");
    expect(titles()).not.toContain("Download");
    expect(titles()).not.toContain("Download current directory as tar");
  });
});
