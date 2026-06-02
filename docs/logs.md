# Logs

[← Manual index](README.md)

![Aggregated logs](images/logs.png)

A global view that streams **many containers at once**, interleaved by time and
color-coded by source.

## Using it
- Pick sources from the left list (selection persists across visits).
- **Search** filters lines; toggle the **`.*` button** for regular-expression
  search (an invalid pattern is flagged, never crashes).
- **Level filters** (error / warn / info / debug / other) show/hide by detected
  level; `stderr` lines are highlighted.
- **Pause** freezes the live tail; the view auto-scrolls when at the bottom.

## Structured parsing
Turn free-text logs into **columns**:

1. Open the parse-rules manager (gear icon) and add a rule — a regex with
   **named groups**, e.g. `(?<ip>\S+) .* "(?<method>\S+) (?<path>\S+)`.
2. Pick a **preset** (nginx, Apache, logfmt, level+message, ISO timestamp…) as a
   starting point; a live preview validates it against the latest line.
3. Select the rule from the toolbar dropdown — matching lines render as a table
   (one column per named group); non-matching lines fall back to raw text.

Parsing runs in your browser, so it's instant and never leaves the server.

## Tips
- Per-container live logs are also on a container's [detail page](containers.md)
  (Logs tab) and feed **log-pattern [alerts](alerts.md)**.
