import { describe, it, expect } from "vitest";
import { composeOutputText } from "./composeOutput";

describe("composeOutputText", () => {
  it("shows the compose output when there's no note", () => {
    expect(composeOutputText({ output: "Container started" })).toBe("Container started");
  });

  it("leads with the remote-deploy note, then the output", () => {
    const text = composeOutputText({ output: "Container started", note: "Copied 2 bind-mounted path(s)." });
    expect(text.startsWith("Copied 2 bind-mounted path(s).")).toBe(true);
    expect(text).toContain("Container started");
    // Blank line between the two, so the note reads as its own paragraph.
    expect(text).toBe("Copied 2 bind-mounted path(s).\n\nContainer started");
  });

  it("keeps the note when the deploy failed, so the copy semantics aren't lost", () => {
    const text = composeOutputText({ error: "service web failed", note: "Copied 1 path." });
    expect(text).toContain("Copied 1 path.");
    expect(text).toContain("service web failed");
  });

  it("falls back to a placeholder when the run said nothing", () => {
    expect(composeOutputText({})).toBe("(no output)");
    expect(composeOutputText({ output: "" })).toBe("(no output)");
  });

  it("still shows the note when there is no other output", () => {
    expect(composeOutputText({ note: "Copied 1 path." })).toBe("Copied 1 path.\n\n(no output)");
  });

  it("prefers output over error when both are present", () => {
    expect(composeOutputText({ output: "ok", error: "ignored" })).toBe("ok");
  });
});
