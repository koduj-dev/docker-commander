import { describe, it, expect } from "vitest";
import { ancestorPath, composeCompletionsAt, isComposeFilename } from "./composeSchema";

// at returns the completion result for the cursor placed at the "|" marker in
// the given source (the marker is stripped before computing).
function at(srcWithCursor: string, explicit = false) {
  const pos = srcWithCursor.indexOf("|");
  if (pos < 0) throw new Error("test source must contain a | cursor marker");
  const text = srcWithCursor.slice(0, pos) + srcWithCursor.slice(pos + 1);
  return composeCompletionsAt(text, pos, explicit);
}

function labels(r: ReturnType<typeof at>): string[] {
  return (r?.options ?? []).map((o) => o.label);
}

describe("isComposeFilename", () => {
  it("matches common compose names", () => {
    for (const n of ["compose.yml", "compose.yaml", "docker-compose.yml", "stack/docker-compose.yaml", "app.compose.yml", "compose.prod.yaml"]) {
      expect(isComposeFilename(n)).toBe(true);
    }
  });
  it("rejects arbitrary YAML", () => {
    for (const n of ["config.yaml", "values.yml", "ci.yml", "notcompose.txt"]) {
      expect(isComposeFilename(n)).toBe(false);
    }
  });
});

describe("ancestorPath", () => {
  it("reconstructs the parent key chain by indentation", () => {
    const lines = ["services:", "  web:", "    build:", "      context: ."];
    expect(ancestorPath(lines, 3, 6)).toEqual(["services", "web", "build"]);
    expect(ancestorPath(lines, 2, 4)).toEqual(["services", "web"]);
    expect(ancestorPath(lines, 1, 2)).toEqual(["services"]);
  });
  it("skips comments and blank lines", () => {
    const lines = ["services:", "  # a comment", "", "  web:", "    image: x"];
    expect(ancestorPath(lines, 4, 4)).toEqual(["services", "web"]);
  });
});

describe("composeCompletionsAt — keys", () => {
  it("suggests top-level keys at column 0", () => {
    const ls = labels(at("ser|", false));
    expect(ls).toContain("services");
  });

  it("suggests service keys under a service, filtered by the partial", () => {
    const r = at("services:\n  web:\n    re|");
    const ls = labels(r);
    expect(ls).toContain("restart");
    expect(ls).not.toContain("services"); // top-level keys must not leak in
  });

  it("suggests nested build keys under services.<name>.build", () => {
    const ls = labels(at("services:\n  web:\n    build:\n      cont|"));
    expect(ls).toContain("context");
  });

  it("offers no completion for user-chosen service names (directly under services)", () => {
    // path is ["services"], which has no schema — should fall through to null.
    expect(at("services:\n  my|")).toBeNull();
  });

  it("does not pop up on an empty line unless explicit", () => {
    expect(at("services:\n  web:\n    |", false)).toBeNull();
    expect(labels(at("services:\n  web:\n    |", true))).toContain("image");
  });
});

describe("composeCompletionsAt — enum values", () => {
  it("suggests restart policy values", () => {
    const r = at("services:\n  web:\n    restart: |");
    expect(labels(r)).toEqual(expect.arrayContaining(["no", "always", "on-failure", "unless-stopped"]));
  });

  it("filters enum values by the typed prefix", () => {
    const r = at("services:\n  web:\n    restart: un|");
    expect(labels(r)).toEqual(["unless-stopped"]);
  });

  it("returns null for a value with no enum (e.g. image)", () => {
    expect(at("services:\n  web:\n    image: ng|")).toBeNull();
  });

  it("places `from` at the start of the partial token", () => {
    const src = "services:\n  web:\n    restart: un";
    const r = composeCompletionsAt(src, src.length, false);
    expect(r).not.toBeNull();
    expect(src.slice(r!.from)).toBe("un");
  });
});
