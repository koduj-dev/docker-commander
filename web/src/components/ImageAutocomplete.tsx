import { useEffect, useRef, useState } from "react";
import { Loader2 } from "lucide-react";
import { imageNameSuggestions, imageTagSuggestions, type ImageSuggestion } from "../lib/imageSuggest";

// tagContext reports whether the cursor value is completing a tag (after the
// repo's `:`) rather than a repository name. A `:` followed by a `/` is a
// registry port (host:port/repo), not a tag separator.
function tagContext(value: string): { tag: boolean; colon: number } {
  const colon = value.lastIndexOf(":");
  return { tag: colon > 0 && !value.slice(colon + 1).includes("/"), colon };
}

// ImageAutocomplete is a text input with a suggestion dropdown for Docker image
// references: repository names (local images + Docker Hub search) and, once a
// `:` is typed, tags for that repo. It's a thin controlled wrapper so it drops
// into any form the way a plain <input> would.
export function ImageAutocomplete({ value, onChange, className, placeholder, required, autoFocus }: {
  value: string;
  onChange: (v: string) => void;
  className?: string;
  placeholder?: string;
  required?: boolean;
  autoFocus?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<ImageSuggestion[]>([]);
  const [loading, setLoading] = useState(false);
  const [active, setActive] = useState(-1);
  const boxRef = useRef<HTMLDivElement>(null);
  const seq = useRef(0);

  // Debounced fetch while the dropdown is open. A monotonically increasing seq
  // discards out-of-order responses.
  useEffect(() => {
    if (!open) return;
    const my = ++seq.current;
    const { tag, colon } = tagContext(value);
    const t = setTimeout(async () => {
      setLoading(true);
      try {
        let list: ImageSuggestion[];
        if (tag) {
          const prefix = value.slice(colon + 1).toLowerCase();
          list = (await imageTagSuggestions(value.slice(0, colon))).filter((s) => s.value.toLowerCase().startsWith(prefix));
        } else {
          list = await imageNameSuggestions(value);
        }
        if (my === seq.current) { setItems(list.slice(0, 30)); setActive(-1); }
      } finally {
        if (my === seq.current) setLoading(false);
      }
    }, 220);
    return () => clearTimeout(t);
  }, [value, open]);

  // Close when clicking outside.
  useEffect(() => {
    const onDoc = (e: MouseEvent) => { if (boxRef.current && !boxRef.current.contains(e.target as Node)) setOpen(false); };
    document.addEventListener("mousedown", onDoc);
    return () => document.removeEventListener("mousedown", onDoc);
  }, []);

  const pick = (s: ImageSuggestion) => {
    const { tag, colon } = tagContext(value);
    onChange(tag ? value.slice(0, colon + 1) + s.value : s.value);
    setOpen(false);
    setActive(-1);
  };

  const onKey = (e: React.KeyboardEvent) => {
    if (!open || !items.length) return;
    if (e.key === "ArrowDown") { e.preventDefault(); setActive((a) => (a + 1) % items.length); }
    else if (e.key === "ArrowUp") { e.preventDefault(); setActive((a) => (a <= 0 ? items.length - 1 : a - 1)); }
    else if (e.key === "Enter" && active >= 0) { e.preventDefault(); pick(items[active]); }
    else if (e.key === "Escape") { setOpen(false); }
  };

  return (
    <div className="relative" ref={boxRef}>
      <input
        className={className}
        value={value}
        placeholder={placeholder}
        required={required}
        autoFocus={autoFocus}
        autoComplete="off"
        spellCheck={false}
        onChange={(e) => { onChange(e.target.value); setOpen(true); }}
        onFocus={() => setOpen(true)}
        onKeyDown={onKey}
      />
      {open && (loading || items.length > 0) && (
        <div className="absolute z-20 mt-1 w-full max-h-60 overflow-auto rounded-lg border border-border bg-panel shadow-lg">
          {loading && items.length === 0 ? (
            <div className="flex items-center gap-2 px-3 py-2 text-xs text-muted"><Loader2 className="h-3 w-3 animate-spin" /> Searching…</div>
          ) : (
            items.map((s, i) => (
              <button
                type="button"
                key={s.value + i}
                className={`flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm ${i === active ? "bg-accent/15" : "hover:bg-panel2"}`}
                onMouseEnter={() => setActive(i)}
                onMouseDown={(e) => { e.preventDefault(); pick(s); }}
              >
                <span className="font-mono truncate">{s.value}</span>
                {s.detail && <span className="ml-auto shrink-0 text-[11px] text-muted truncate max-w-[50%]">{s.detail}</span>}
              </button>
            ))
          )}
        </div>
      )}
    </div>
  );
}
