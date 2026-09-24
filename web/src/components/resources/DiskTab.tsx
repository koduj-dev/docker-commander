import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { Boxes, Database, Eraser, Layers, Loader2, RefreshCw } from "lucide-react";
import clsx from "clsx";
import { api } from "../../lib/api";
import type { DiskReport } from "../../lib/types";
import { bytes } from "../../lib/format";
import { Pager, SearchBar, useListControls } from "../ListControls";
import { EmptyState, Spinner, StatCard } from "../ui";

const AUTO_REFRESH_MS = 60_000;

// A size of -1 means the daemon did not calculate it (a volume from a
// non-local driver, an image whose shared size wasn't computed): say so
// instead of showing 0, which would read as "empty".
export const sizeLabel = (n: number) => (n < 0 ? "unknown" : bytes(n));
const Size = ({ n }: { n: number }) => (n < 0 ? <span className="text-muted">unknown</span> : <>{bytes(n)}</>);

type Section = "images" | "containers" | "volumes";

// The Disk tab: what takes the space, largest first. The daemon call behind
// it (`docker system df -v`) is heavy on a busy host, so the data is cached
// server-side, refreshed automatically at most once a minute, and otherwise
// only on request (the Refresh button).
export function DiskTab() {
  const [report, setReport] = useState<DiskReport | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [section, setSection] = useState<Section>("images");

  // A manual Refresh can overlap the automatic read; only the most recently
  // started request may apply its result (or clear the busy state).
  const latest = useRef(0);
  const load = useCallback((refresh: boolean) => {
    const mine = ++latest.current;
    setBusy(true);
    return api.diskReport(refresh)
      .then((r) => { if (mine === latest.current) { setReport(r); setError(""); } })
      .catch((e) => { if (mine === latest.current) setError(e instanceof Error ? e.message : "could not read disk usage"); })
      .finally(() => { if (mine === latest.current) setBusy(false); });
  }, []);

  useEffect(() => {
    load(false);
    const t = setInterval(() => load(false), AUTO_REFRESH_MS);
    return () => clearInterval(t);
  }, [load]);

  const images = report?.images ?? [];
  const containers = report?.containers ?? [];
  const volumes = report?.volumes ?? [];
  const imageControls = useListControls(images, (i, q) => (i.tags ?? []).join(" ").toLowerCase().includes(q) || i.id.toLowerCase().includes(q), { storageKey: "disk-images" });
  const containerControls = useListControls(containers, (c, q) => c.name.toLowerCase().includes(q) || (c.project ?? "").toLowerCase().includes(q), { storageKey: "disk-containers" });
  const volumeControls = useListControls(volumes, (v, q) => v.name.toLowerCase().includes(q) || (v.project ?? "").toLowerCase().includes(q), { storageKey: "disk-volumes" });

  if (!report) {
    return error
      ? <div className="text-sm text-danger">Couldn't read disk usage: {error}</div>
      : <div className="flex items-center gap-2 text-muted"><Spinner /> Reading disk usage… this can take a few seconds.</div>;
  }

  const known = (xs: number[]) => xs.filter((n) => n >= 0);
  const volTotal = known(volumes.map((v) => v.size)).reduce((a, b) => a + b, 0);
  const volUnknown = volumes.length - known(volumes.map((v) => v.size)).length;
  const rwTotal = known(containers.map((c) => c.sizeRw)).reduce((a, b) => a + b, 0);
  const controlsBySection = { images: imageControls, containers: containerControls, volumes: volumeControls };
  const active = controlsBySection[section];
  const segments: { key: Section; label: string; icon: ReactNode; count: number }[] = [
    { key: "images", label: "Images", icon: <Layers className="h-3.5 w-3.5" />, count: images.length },
    { key: "containers", label: "Containers", icon: <Boxes className="h-3.5 w-3.5" />, count: containers.length },
    { key: "volumes", label: "Volumes", icon: <Database className="h-3.5 w-3.5" />, count: volumes.length },
  ];

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <button className="btn-ghost px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy} onClick={() => load(true)}>
          {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : <RefreshCw className="h-4 w-4" />} Refresh
        </button>
        <span className="text-xs text-muted">
          As of {new Date(report.generatedAt * 1000).toLocaleTimeString()}. Reading disk usage is heavy on the Docker
          daemon, so it refreshes automatically at most once a minute — press Refresh to recompute on demand.
        </span>
      </div>
      {error && <div className="text-sm text-danger">Couldn't refresh: {error}</div>}

      <div className="grid grid-cols-2 lg:grid-cols-4 gap-3">
        <StatCard icon={<Database className="h-5 w-5" />} label="Volumes" value={bytes(volTotal)} sub={`${volumes.length} volumes${volUnknown ? ` · ${volUnknown} unknown` : ""}`} />
        <StatCard icon={<Boxes className="h-5 w-5" />} label="Container writable layers" value={bytes(rwTotal)} sub={`${containers.length} containers`} />
        <StatCard icon={<Eraser className="h-5 w-5" />} label="Build cache" value={bytes(report.buildCache.size)} sub={`${bytes(report.buildCache.reclaimable)} reclaimable`} />
        <StatCard icon={<Layers className="h-5 w-5" />} label="Images" value={images.length} sub="sizes overlap — see Unique" />
      </div>

      <div className="flex gap-1 rounded-lg bg-panel2/50 p-0.5 w-fit">
        {segments.map((s) => (
          <button
            key={s.key}
            onClick={() => setSection(s.key)}
            className={clsx("flex items-center gap-1.5 px-3 py-1.5 rounded-md text-sm", section === s.key ? "bg-panel text-text shadow-sm" : "text-muted hover:text-text")}
          >
            {s.icon} {s.label}
            <span className="text-[10px] bg-accent/20 text-accent rounded-full px-1.5 leading-4">{s.count}</span>
          </button>
        ))}
      </div>

      <SearchBar controls={active as never} placeholder={`Search ${section}…`} />

      {active.filteredCount === 0 ? (
        <EmptyState title={active.totalCount === 0 ? `No ${section}` : `No ${section} match`} hint={active.totalCount === 0 ? "Nothing to show on this host." : "Try a different filter."} />
      ) : (
        <div className="card overflow-hidden">
          <table className="w-full text-sm">
            {section === "images" && (
              <>
                <thead className="text-muted text-xs uppercase tracking-wide">
                  <tr className="border-b border-border">
                    <th className="text-left font-medium px-4 py-3">Image</th>
                    <th className="text-right font-medium px-4 py-3" title="What deleting this image would free: its size minus layers other images share">Unique</th>
                    <th className="text-right font-medium px-4 py-3" title="The whole image, including layers shared with other images">Size</th>
                    <th className="text-right font-medium px-4 py-3">Used by</th>
                  </tr>
                </thead>
                <tbody>
                  {imageControls.pageItems.map((i) => (
                    <tr key={i.id} className="border-b border-border/50 last:border-0">
                      <td className="px-4 py-2.5 font-medium">
                        {(i.tags ?? []).length > 0 ? (i.tags ?? []).join(", ") : <span className="text-muted font-mono text-xs">{i.id.replace(/^sha256:/, "").slice(0, 12)} (untagged)</span>}
                      </td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs"><Size n={i.unique} /></td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs text-muted"><Size n={i.size} /></td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right text-xs">{i.containers < 0 ? <span className="text-muted">—</span> : i.containers === 0 ? <span className="text-warn">unused</span> : `${i.containers} container${i.containers === 1 ? "" : "s"}`}</td>
                    </tr>
                  ))}
                </tbody>
              </>
            )}
            {section === "containers" && (
              <>
                <thead className="text-muted text-xs uppercase tracking-wide">
                  <tr className="border-b border-border">
                    <th className="text-left font-medium px-4 py-3">Container</th>
                    <th className="text-left font-medium px-4 py-3">State</th>
                    <th className="text-right font-medium px-4 py-3" title="Data the container wrote on top of its image">Writable layer</th>
                    <th className="text-right font-medium px-4 py-3" title="Writable layer plus the image">Total</th>
                  </tr>
                </thead>
                <tbody>
                  {containerControls.pageItems.map((c) => (
                    <tr key={c.id} className="border-b border-border/50 last:border-0">
                      <td className="px-4 py-2.5 font-medium">
                        <Link to={`/containers/${c.id}`} className="hover:underline">{c.name}</Link>
                        {c.project && <span className="ml-2 text-[10px] text-muted border border-border rounded px-1">{c.project}</span>}
                      </td>
                      <td className="px-4 py-2.5 text-xs text-muted">{c.state}</td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs"><Size n={c.sizeRw} /></td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs text-muted"><Size n={c.sizeRoot} /></td>
                    </tr>
                  ))}
                </tbody>
              </>
            )}
            {section === "volumes" && (
              <>
                <thead className="text-muted text-xs uppercase tracking-wide">
                  <tr className="border-b border-border">
                    <th className="text-left font-medium px-4 py-3">Volume</th>
                    <th className="text-left font-medium px-4 py-3">Driver</th>
                    <th className="text-right font-medium px-4 py-3">Size</th>
                    <th className="text-right font-medium px-4 py-3">Used by</th>
                  </tr>
                </thead>
                <tbody>
                  {volumeControls.pageItems.map((v) => (
                    <tr key={v.name} className="border-b border-border/50 last:border-0">
                      <td className="px-4 py-2.5 font-medium break-all">
                        {v.name}
                        {v.project && <span className="ml-2 text-[10px] text-muted border border-border rounded px-1">{v.project}</span>}
                      </td>
                      <td className="px-4 py-2.5 text-xs text-muted">{v.driver}</td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right font-mono text-xs"><Size n={v.size} /></td>
                      <td className="px-4 py-2.5 whitespace-nowrap text-right text-xs">{v.refCount < 0 ? <span className="text-muted">—</span> : v.refCount === 0 ? <span className="text-warn">unused</span> : `${v.refCount} container${v.refCount === 1 ? "" : "s"}`}</td>
                    </tr>
                  ))}
                </tbody>
              </>
            )}
          </table>
        </div>
      )}
      <Pager controls={active as never} />
    </div>
  );
}
