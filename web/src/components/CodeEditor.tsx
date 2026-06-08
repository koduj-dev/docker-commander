import { useMemo } from "react";
import CodeMirror from "@uiw/react-codemirror";
import { EditorView } from "@codemirror/view";
import { StreamLanguage, type LanguageSupport } from "@codemirror/language";
import { yaml } from "@codemirror/lang-yaml";
import { json } from "@codemirror/lang-json";
import { shell } from "@codemirror/legacy-modes/mode/shell";
import { dockerFile } from "@codemirror/legacy-modes/mode/dockerfile";
import { properties } from "@codemirror/legacy-modes/mode/properties";
import { oneDark } from "@codemirror/theme-one-dark";
import type { Extension } from "@codemirror/state";

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
    const lang = languageFor(filename);
    return [dcTheme, ...(lang ? [lang as LanguageSupport | Extension] : [])];
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
