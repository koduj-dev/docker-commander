/** Response shape shared by the deploy / down / restart endpoints. */
export type ComposeRunResult = {
  output?: string;
  error?: string;
  /** Set by a remote deploy that copied bind-mounted paths into seeded volumes. */
  note?: string;
};

/**
 * Builds the text shown for a compose run.
 *
 * A remote deploy returns a `note` explaining that the project's bind-mounted
 * files were **copied** to the host rather than mounted live — editing them
 * needs a redeploy. That's a semantic difference from a local deploy, so it
 * leads, followed by the compose output.
 *
 * This lives here, and is covered by tests, because both places that can trigger
 * a deploy (the project list and the project editor) must render it identically:
 * the logic was duplicated once and the editor silently dropped the note.
 */
export function composeOutputText(r: ComposeRunResult): string {
  const body = r.output || r.error || "(no output)";
  return r.note ? `${r.note}\n\n${body}` : body;
}
