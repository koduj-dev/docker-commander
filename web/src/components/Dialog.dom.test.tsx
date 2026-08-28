/** @vitest-environment happy-dom */
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { DialogProvider, useDialogs } from "./Dialog";

// A confirm/prompt/alert dialog is almost always opened from INSIDE another
// modal (e.g. a destructive-action confirm launched from a project's own
// modal) — so its backdrop click bubbling up to that ancestor modal's own
// close handler was the single highest-impact instance of this bug class:
// clicking to dismiss "are you sure?" could silently close the whole modal
// underneath it too.

let container: HTMLDivElement;
let root: Root | undefined;

function Trigger() {
  const dialogs = useDialogs();
  return <button onClick={() => dialogs.confirm({ title: "Delete it?" })}>open</button>;
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  if (root) act(() => root!.unmount());
  root = undefined;
  container?.remove();
});

describe("DialogProvider — backdrop click does not escape to an ancestor", () => {
  it("dismisses the dialog without triggering a parent's own click handler", async () => {
    const parentOnClick = () => { parentOnClick.calls++; };
    parentOnClick.calls = 0;

    container = document.createElement("div");
    document.body.appendChild(container);
    root = createRoot(container);
    await act(async () => {
      root!.render(
        <div onClick={parentOnClick}>
          <DialogProvider>
            <Trigger />
          </DialogProvider>
        </div>,
      );
    });

    const openBtn = container.querySelector("button") as HTMLButtonElement;
    await act(async () => openBtn.click());
    expect(container.textContent).toContain("Delete it?");
    parentOnClick.calls = 0; // the "open" click itself also bubbles to the wrapper — not what's under test

    const backdrop = container.querySelector(".fixed.z-\\[80\\]") as HTMLElement;
    expect(backdrop).toBeTruthy();
    await act(async () => backdrop.click());

    expect(container.textContent).not.toContain("Delete it?");
    expect(parentOnClick.calls).toBe(0);
  });
});
