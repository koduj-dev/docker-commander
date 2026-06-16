import { useEffect, useMemo, useRef } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { EditorView } from "@codemirror/view";
import { type Text } from "@codemirror/state";
import { StreamLanguage, type LanguageSupport } from "@codemirror/language";
import { yaml } from "@codemirror/lang-yaml";
import { json, jsonParseLinter } from "@codemirror/lang-json";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { dockerFile } from "@codemirror/legacy-modes/mode/dockerfile";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { oneDark } from "@codemirror/theme-one-dark";
import { lintGutter, setDiagnostics, type Diagnostic } from "@codemirror/lint";
import { parseDocument } from "yaml";
import type { Extension } from "@codemirror/state";
import type { CompletionContext, CompletionResult, Completion } from "@codemirror/autocomplete";
import { imageNameSuggestions, imageTagSuggestions } from "../lib/imageSuggest";
import { composeCompletionsAt, isComposeFilename } from "../lib/composeSchema";

// ServerCheck is the latest authoritative validation result for the open file —
// from `docker compose config` (compose) or `docker build --check` (dockerfile).
// CodeEditor resolves these messages to inline diagnostics so they sit on the
// relevant line, just like the client-side syntax linters.
export type ServerCheck =
  | { kind: "compose"; error?: string; warnings?: string[] }
  | { kind: "dockerfile"; output?: string }
  | null;

// ---- client-side syntax linters ---------------------------------------------

// yamlLinter parses YAML with the `yaml` library — which resolves anchors (&),
// aliases (*) and merge keys (<<) — and surfaces parse errors/warnings inline.
function yamlSource(view: EditorView): Diagnostic[] {
  const text = view.state.doc.toString();
  if (!text.trim()) return [];
  const doc = parseDocument(text);
  const len = view.state.doc.length;
  const clamp = (n: number) => Math.max(0, Math.min(n, len));
  const out: Diagnostic[] = [];
  for (const e of doc.errors) {
    const [from, to] = e.pos ?? [0, 1];
    out.push({ from: clamp(from), to: clamp(Math.max(to, from + 1)), severity: "error", message: e.message });
  }
  for (const wmsg of doc.warnings) {
    const [from, to] = wmsg.pos ?? [0, 1];
    out.push({ from: clamp(from), to: clamp(Math.max(to, from + 1)), severity: "warning", message: wmsg.message });
  }
  return out;
}

// envSource checks .env files: every non-comment line must be KEY=value, with
// warnings for duplicate keys (last value wins) and unusual variable names.
function envSource(view: EditorView): Diagnostic[] {
  const out: Diagnostic[] = [];
  const seen = new Set<string>();
  const doc = view.state.doc;
  for (let i = 1; i <= doc.lines; i++) {
    const line = doc.line(i);
    const t = line.text.trim();
    if (t === "" || t.startsWith("#")) continue;
    const eq = line.text.indexOf("=");
    if (eq < 0) {
      out.push({ from: line.from, to: line.to, severity: "error", message: "expected KEY=value (no '=' on this line)" });
      continue;
    }
    const key = line.text.slice(0, eq).replace(/^\s*export\s+/, "").trim();
    if (key === "") {
      out.push({ from: line.from, to: line.from + eq + 1, severity: "error", message: "missing key before '='" });
      continue;
    }
    if (!/^[A-Za-z_][A-Za-z0-9_.]*$/.test(key)) {
      out.push({ from: line.from, to: line.from + eq, severity: "warning", message: `unusual variable name "${key}"` });
    }
    if (seen.has(key)) {
      out.push({ from: line.from, to: line.from + eq, severity: "warning", message: `duplicate key "${key}" — the last value wins` });
    }
    seen.add(key);
  }
  return out;
}

// jsonSource underlines JSON syntax errors.
const jsonSource = jsonParseLinter();

function isEnvFile(name: string): boolean {
  const base = (name.split("/").pop() ?? "").toLowerCase();
  return base === ".env" || base.startsWith(".env.") || base.endsWith(".env");
}

// ---- server-result → inline diagnostics -------------------------------------

function lineRange(doc: Text, ln: number): { from: number; to: number } {
  const n = ln >= 1 && ln <= doc.lines ? ln : 1;
  const line = doc.line(n);
  return { from: line.from, to: line.to };
}

// cleanComposeMessage strips the noisy "validating <path>:" / "yaml:" prefixes.
function cleanComposeMessage(s: string): string {
  return s.replace(/^validating\s+\S+:\s*/i, "").replace(/^yaml:\s*/i, "").trim();
}

// composePathRange resolves a leading compose path in the error (e.g.
// "services.web.ports") to a node range in the document via the YAML parser.
function composePathRange(errorText: string, docText: string): { from: number; to: number } | null {
  const m = /\b((?:services|networks|volumes|configs|secrets)(?:\.[A-Za-z0-9_.-]+)+)/.exec(errorText);
  if (!m) return null;
  const path = m[1].split(".").filter(Boolean);
  try {
    const node = parseDocument(docText).getIn(path, true) as { range?: [number, number, number] } | undefined;
    if (node?.range) return { from: node.range[0], to: node.range[1] };
  } catch { /* fall through */ }
  return null;
}

// findVarRange locates a ${VAR} (or $VAR) occurrence in the document text.
function findVarRange(docText: string, varName: string): { from: number; to: number } | null {
  for (const p of [`\${${varName}}`, `\${${varName}:`, `\${${varName}-`]) {
    const i = docText.indexOf(p);
    if (i >= 0) return { from: i, to: i + p.length };
  }
  const m = new RegExp("\\$" + varName + "\\b").exec(docText);
  return m ? { from: m.index, to: m.index + m[0].length } : null;
}

// parseDockerfileDiags turns `docker build --check` output into line diagnostics:
// each WARNING/ERROR block ends with a `Dockerfile:<line>` reference.
function parseDockerfileDiags(output: string, doc: Text): Diagnostic[] {
  const out: Diagnostic[] = [];
  let cur: { sev: "error" | "warning"; parts: string[] } | null = null;
  for (const raw of output.split("\n")) {
    const ln = raw.trim();
    const head = /^(WARNING|ERROR):\s*(.*)$/.exec(ln);
    const df = /^Dockerfile:(\d+)/.exec(ln);
    if (head) {
      cur = { sev: head[1] === "ERROR" ? "error" : "warning", parts: [head[2].replace(/\s*-\s*https?:\/\/\S+$/, "")] };
    } else if (df && cur) {
      const r = lineRange(doc, parseInt(df[1], 10));
      out.push({ ...r, severity: cur.sev, message: cur.parts.filter(Boolean).join(" — ") });
      cur = null;
    } else if (cur && ln && !ln.startsWith("---") && !/^\d+\s*\|/.test(ln) && !ln.startsWith("Check complete")) {
      cur.parts.push(ln);
    }
  }
  // Flush a trailing block that never hit a Dockerfile:<line> reference.
  if (cur) out.push({ ...lineRange(doc, 1), severity: cur.sev, message: cur.parts.filter(Boolean).join(" — ") });
  return out;
}

function resolveServerDiags(check: ServerCheck, view: EditorView): Diagnostic[] {
  if (!check) return [];
  const doc = view.state.doc;
  const docText = doc.toString();
  const len = doc.length;
  const clamp = (n: number) => Math.max(0, Math.min(n, len));
  const mk = (r: { from: number; to: number } | null, severity: "error" | "warning", message: string): Diagnostic => {
    const range = r ?? lineRange(doc, 1);
    return { from: clamp(range.from), to: clamp(Math.max(range.to, range.from + 1)), severity, message };
  };
  const out: Diagnostic[] = [];
  if (check.kind === "compose") {
    if (check.error) {
      const lm = /line (\d+)/.exec(check.error);
      const range = lm ? lineRange(doc, parseInt(lm[1], 10)) : composePathRange(check.error, docText);
      out.push(mk(range, "error", cleanComposeMessage(check.error)));
    }
    for (const wmsg of check.warnings ?? []) {
      const vn = /"([A-Za-z_][A-Za-z0-9_]*)"\s+variable is not set/.exec(wmsg)?.[1];
      out.push(mk(vn ? findVarRange(docText, vn) : null, "warning", wmsg));
    }
  } else if (check.kind === "dockerfile") {
    out.push(...parseDockerfileDiags(check.output ?? "", doc));
  }
  return out;
}

// ---- image-name autocomplete (compose YAML) ---------------------------------

// imageCompletionSource completes the value of a compose `image:` line. Before a
// `:` it suggests repository names (local images + a Docker Hub search); after a
// `:` it suggests tags for that repo (local tags + Hub's tag list). It returns
// null off an image line so other completion sources (word completion) still run.
async function imageCompletionSource(context: CompletionContext): Promise<CompletionResult | null> {
  const line = context.state.doc.lineAt(context.pos);
  const before = line.text.slice(0, context.pos - line.from);
  const m = /^(\s*image:\s*)(["']?)([^\s"']*)$/.exec(before);
  if (!m) return null;
  const valueStart = line.from + m[1].length + m[2].length;
  const typed = m[3];
  const colon = typed.lastIndexOf(":");
  // A ":" with a "/" after it is a registry port (host:port/repo), not a tag.
  if (colon >= 0 && !typed.slice(colon + 1).includes("/")) {
    const repo = typed.slice(0, colon);
    const prefix = typed.slice(colon + 1).toLowerCase();
    if (!repo) return null;
    const tags = await imageTagSuggestions(repo);
    const options: Completion[] = tags
      .filter((t) => t.value.toLowerCase().startsWith(prefix))
      .map((t) => ({ label: t.value, detail: t.detail, type: "constant" }));
    if (!options.length) return null;
    return { from: valueStart + colon + 1, options, validFor: /^[\w][\w.-]*$/ };
  }
  if (!context.explicit && typed.length < 1) return null;
  const sugg = await imageNameSuggestions(typed);
  const options: Completion[] = sugg.map((s) => ({ label: s.value, detail: s.detail, type: s.local ? "constant" : "class" }));
  if (!options.length) return null;
  return { from: valueStart, options, validFor: /^[\w][\w./-]*$/ };
}

// ---- compose schema autocomplete (keys + enum values) ----------------------

// composeCompletionSource offers Compose keys (at the right nesting level) and
// known enum values (e.g. restart policies). It delegates to the pure
// composeCompletionsAt so the logic is unit-tested without an editor. It returns
// null off a schema-relevant position so image/word completion still run.
function composeCompletionSource(context: CompletionContext): CompletionResult | null {
  const res = composeCompletionsAt(context.state.doc.toString(), context.pos, context.explicit);
  if (!res) return null;
  const options: Completion[] = res.options.map((o) => ({
    label: o.label,
    detail: o.detail,
    type: o.kind === "enum" ? "constant" : "property",
  }));
  return { from: res.from, options, validFor: /^[\w.-]*$/ };
}

// ---- theme + component ------------------------------------------------------

function languageFor(name: string): Extension | null {
  const lower = name.toLowerCase();
  const base = lower.split("/").pop() ?? lower;
  if (/\.ya?ml$/.test(lower)) return yaml();
  if (/\.json$/.test(lower)) return json();
  if (/\.(sh|bash|zsh)$/.test(lower)) return StreamLanguage.define(shell);
  if (base === "dockerfile" || /(^|\.)dockerfile$/.test(base) || base.startsWith("dockerfile.")) return StreamLanguage.define(dockerFile) as Extension;
  if (/\.(conf|cfg|ini|properties|env)$/.test(lower) || base.startsWith(".env") || base === "env") return StreamLanguage.define(properties);
  return null;
}

const dcTheme = EditorView.theme({
  "&": { backgroundColor: "#0b0f17", color: "#e6ebf4", height: "100%" },
  ".cm-content": { caretColor: "#2496ed", fontFamily: "ui-monospace, SFMono-Regular, Menlo, monospace" },
  ".cm-cursor, .cm-dropCursor": { borderLeftColor: "#2496ed" },
  ".cm-gutters": { backgroundColor: "#0b0f17", color: "#8b97ad", border: "none", borderRight: "1px solid #243047" },
  ".cm-activeLine": { backgroundColor: "#1a223340" },
  ".cm-activeLineGutter": { backgroundColor: "#1a2233" },
  "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, .cm-content ::selection": { backgroundColor: "#2496ed33" },
  ".cm-scroller": { fontSize: "13px" },
}, { dark: true });

// allDiagnostics combines the client syntax checks with the resolved server
// results for the open file.
function allDiagnostics(view: EditorView, filename: string, check: ServerCheck): Diagnostic[] {
  const lower = filename.toLowerCase();
  const diags: Diagnostic[] = [];
  if (/\.ya?ml$/.test(lower)) diags.push(...yamlSource(view));
  else if (/\.json$/.test(lower)) diags.push(...jsonSource(view));
  else if (isEnvFile(filename)) diags.push(...envSource(view));
  diags.push(...resolveServerDiags(check, view));
  return diags;
}

// CodeEditor: syntax highlighting + inline diagnostics (client syntax checks
// plus server validation results) in an app-matched dark theme.
//
// Diagnostics are pushed imperatively with setDiagnostics rather than via a
// linter() source: the server result arrives asynchronously (no edit), and
// dispatching directly is reliable where forceLinting proved not to repaint.
export function CodeEditor({ value, onChange, filename, readOnly, serverCheck }: {
  value: string;
  onChange?: (v: string) => void;
  filename: string;
  readOnly?: boolean;
  serverCheck?: ServerCheck;
}) {
  const viewRef = useRef<EditorView | null>(null);

  // Recompute + push diagnostics on edits, file switches and new server results.
  useEffect(() => {
    const view = viewRef.current;
    if (!view) return;
    view.dispatch(setDiagnostics(view.state, allDiagnostics(view, filename, serverCheck ?? null)));
  }, [value, filename, serverCheck]);

  const extensions = useMemo<Extension[]>(() => {
    const lang = languageFor(filename);
    const exts: Extension[] = [dcTheme, lintGutter()];
    if (lang) {
      exts.push(lang as LanguageSupport | Extension);
      // Register image-name completion as a YAML language-data source so it
      // merges with (rather than replaces) the default word completion.
      if (/\.ya?ml$/.test(filename.toLowerCase())) {
        exts.push((lang as LanguageSupport).language.data.of({ autocomplete: imageCompletionSource }));
        // Schema-aware key/enum completion, only for Compose files so it doesn't
        // pollute arbitrary YAML.
        if (isComposeFilename(filename)) {
          exts.push((lang as LanguageSupport).language.data.of({ autocomplete: composeCompletionSource }));
        }
      }
    }
    return exts;
  }, [filename]);

  return (
    <CodeMirror
      value={value}
      onChange={onChange}
      theme={oneDark}
      extensions={extensions}
      readOnly={readOnly}
      height="100%"
      style={{ height: "100%", overflow: "hidden" }}
      basicSetup={{ lineNumbers: true, foldGutter: true, highlightActiveLine: true, autocompletion: true, tabSize: 2 }}
      onCreateEditor={(view) => { viewRef.current = view; }}
    />
  );
}
