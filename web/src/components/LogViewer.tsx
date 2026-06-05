import { useEffect, useMemo, useRef, useState } from "react";
import { Search } from "lucide-react";
import clsx from "clsx";
import type { LogLine } from "../lib/types";

interface Props {
  lines: LogLine[];
}

// Live log panel with substring filtering and stdout/stderr toggles. Auto-
// scrolls to the bottom unless the user has scrolled up to read history.
export function LogViewer({ lines }: Props) {
  const [filter, setFilter] = useState("");
  const [showOut, setShowOut] = useState(true);
  const [showErr, setShowErr] = useState(true);
  const [stick, setStick] = useState(true);
  const boxRef = useRef<HTMLDivElement>(null);

  const filtered = useMemo(() => {
    const f = filter.toLowerCase();
    return lines.filter(
      (l) =>
        ((l.stream === "stdout" && showOut) || (l.stream === "stderr" && showErr)) &&
        (f === "" || l.message.toLowerCase().includes(f))
    );
  }, [lines, filter, showOut, showErr]);

  useEffect(() => {
    if (stick && boxRef.current) {
      boxRef.current.scrollTop = boxRef.current.scrollHeight;
    }
  }, [filtered, stick]);

  return (
    <div className="card flex flex-col h-112">
      <div className="flex items-center gap-3 p-3 border-b border-border">
        <div className="relative flex-1">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
          <input
            className="input pl-8 py-1.5"
            placeholder="Filter logs…"
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </div>
        <Toggle active={showOut} onClick={() => setShowOut((v) => !v)} label="stdout" />
        <Toggle active={showErr} onClick={() => setShowErr((v) => !v)} label="stderr" danger />
      </div>
      <div
        ref={boxRef}
        onScroll={(e) => {
          const el = e.currentTarget;
          setStick(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
        }}
        className="flex-1 overflow-auto font-mono text-xs leading-relaxed p-3 space-y-0.5"
      >
        {filtered.length === 0 ? (
          <div className="text-muted">No log lines.</div>
        ) : (
          filtered.map((l, i) => (
            <div key={i} className="flex gap-2">
              {l.timestamp && <span className="text-muted/60 shrink-0">{l.timestamp.slice(11, 19)}</span>}
              <span className={clsx("whitespace-pre-wrap break-all", l.stream === "stderr" ? "text-danger/90" : "text-text/90")}>
                {l.message}
              </span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}

function Toggle({ active, onClick, label, danger }: { active: boolean; onClick: () => void; label: string; danger?: boolean }) {
  return (
    <button
      onClick={onClick}
      className={clsx(
        "text-xs px-2 py-1 rounded-md font-medium transition-colors",
        active ? (danger ? "bg-danger/15 text-danger" : "bg-accent/15 text-accent") : "bg-panel2 text-muted"
      )}
    >
      {label}
    </button>
  );
}
