import { useMemo } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { EditorView } from "@codemirror/view";
import { StreamLanguage, type LanguageSupport } from "@codemirror/language";
import { yaml } from "@codemirror/lang-yaml";
import { json, jsonParseLinter } from "@codemirror/lang-json";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { dockerFile } from "@codemirror/legacy-modes/mode/dockerfile";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { oneDark } from "@codemirror/theme-one-dark";
import { linter, lintGutter, type Diagnostic } from "@codemirror/lint";
import { parseDocument } from "yaml";
import type { Extension } from "@codemirror/state";

// yamlLinter parses YAML with the `yaml` library — which resolves anchors (&),
// aliases (*) and merge keys (<<) — and surfaces parse errors/warnings as inline
// diagnostics. This catches malformed YAML (incl. broken anchors) as you type,
// before the authoritative server-side `docker compose config` check.
const yamlLinter = linter((view) => {
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
});

// envLinter checks .env files: every non-comment line must be KEY=value, with
// warnings for duplicate keys (last value wins) and unusual variable names.
const envLinter = linter((view) => {
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
});

// isEnvFile matches .env, .env.* (e.g. .env.local) and *.env.
function isEnvFile(name: string): boolean {
  const base = (name.split("/").pop() ?? "").toLowerCase();
  return base === ".env" || base.startsWith(".env.") || base.endsWith(".env");
}

// languageFor picks a CodeMirror language from a file name. Compose/sidecar
// projects are mostly YAML, with shell scripts, Dockerfiles and *.conf/.env.
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

// dcTheme blends one-dark's token colors with the app's own background/border
// palette so the editor sits flush inside the panel.
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

// CodeEditor is a thin CodeMirror wrapper used by the project file editor:
// syntax highlighting by file name, app-matched dark theme.
export function CodeEditor({ value, onChange, filename, readOnly }: {
  value: string;
  onChange?: (v: string) => void;
  filename: string;
  readOnly?: boolean;
}) {
  const extensions = useMemo<Extension[]>(() => {
    const lower = filename.toLowerCase();
    const lang = languageFor(filename);
    const exts: Extension[] = [dcTheme];
    if (lang) exts.push(lang as LanguageSupport | Extension);
    // Live, file-type-aware diagnostics.
    if (/\.ya?ml$/.test(lower)) exts.push(yamlLinter, lintGutter()); // anchor-aware YAML
    else if (/\.json$/.test(lower)) exts.push(linter(jsonParseLinter()), lintGutter());
    else if (isEnvFile(filename)) exts.push(envLinter, lintGutter());
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
    />
  );
}
