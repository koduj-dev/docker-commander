import { useMemo, useState } from "react";
import { ChevronLeft, ChevronRight, Search } from "lucide-react";

const PAGE_SIZES = [10, 20, 50, 100];

// useListControls wires up client-side search + pagination for a list. The
// caller supplies a matcher that decides whether an item matches the query.
export function useListControls<T>(items: T[], match: (item: T, q: string) => boolean) {
  const [query, setQuery] = useState("");
  const [pageSize, setPageSize] = useState(20);
  const [page, setPage] = useState(0);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? items.filter((it) => match(it, q)) : items;
    // match is stable enough for our pages; intentionally not a dep.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items, query]);

  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize));
  const clampedPage = Math.min(page, pageCount - 1);
  const pageItems = filtered.slice(clampedPage * pageSize, clampedPage * pageSize + pageSize);

  return {
    query,
    setQuery: (q: string) => { setQuery(q); setPage(0); },
    pageSize,
    setPageSize: (n: number) => { setPageSize(n); setPage(0); },
    page: clampedPage,
    setPage,
    pageItems,
    filteredCount: filtered.length,
    totalCount: items.length,
    pageCount,
  };
}

export type ListControls<T> = ReturnType<typeof useListControls<T>>;

// SearchBar is the top toolbar: a search field plus a page-size selector.
export function SearchBar<T>({ controls, placeholder }: { controls: ListControls<T>; placeholder?: string }) {
  return (
    <div className="flex items-center gap-3">
      <div className="relative flex-1">
        <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-muted" />
        <input
          className="input pl-8 py-1.5"
          placeholder={placeholder ?? "Search…"}
          value={controls.query}
          onChange={(e) => controls.setQuery(e.target.value)}
        />
      </div>
      <label className="flex items-center gap-1.5 text-xs text-muted shrink-0">
        Per page
        <select className="input py-1.5 w-20" value={controls.pageSize} onChange={(e) => controls.setPageSize(Number(e.target.value))}>
          {PAGE_SIZES.map((n) => <option key={n} value={n}>{n}</option>)}
        </select>
      </label>
    </div>
  );
}

// Pager is the bottom footer: result count + prev/next page navigation.
export function Pager<T>({ controls }: { controls: ListControls<T> }) {
  const { page, pageCount, filteredCount, totalCount, pageSize } = controls;
  const from = filteredCount === 0 ? 0 : page * pageSize + 1;
  const to = Math.min(filteredCount, page * pageSize + pageSize);
  return (
    <div className="flex items-center justify-between text-xs text-muted px-1">
      <span>
        {from}–{to} of {filteredCount}
        {filteredCount !== totalCount && <span className="text-muted/60"> (filtered from {totalCount})</span>}
      </span>
      {pageCount > 1 && (
        <div className="flex items-center gap-1">
          <button className="btn-ghost px-2 py-1 disabled:opacity-40" disabled={page === 0} onClick={() => controls.setPage(page - 1)}>
            <ChevronLeft className="h-4 w-4" />
          </button>
          <span className="px-1">{page + 1} / {pageCount}</span>
          <button className="btn-ghost px-2 py-1 disabled:opacity-40" disabled={page >= pageCount - 1} onClick={() => controls.setPage(page + 1)}>
            <ChevronRight className="h-4 w-4" />
          </button>
        </div>
      )}
    </div>
  );
}
