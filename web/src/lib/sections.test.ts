import { describe, it, expect } from "vitest";
import { SECTION_LABELS, sectionLabel } from "./sections";

// Every section the backend knows has a human label, so the Users and Settings
// checkboxes never show a raw key. Keep in step with store.Sections.
const BACKEND_SECTIONS = [
  "dashboard", "containers", "projects", "images", "volumes", "networks", "topology",
  "logs", "events", "alerts", "hosts", "registries", "audit", "diagnostics",
];

describe("section labels", () => {
  it("label every backend section", () => {
    for (const s of BACKEND_SECTIONS) {
      expect(SECTION_LABELS[s], s).toBeTruthy();
    }
  });

  it("names diagnostics after the page it unlocks", () => {
    expect(sectionLabel("diagnostics")).toBe("Troubleshooting");
  });
});
