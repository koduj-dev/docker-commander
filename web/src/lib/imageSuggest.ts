// Image-name autocomplete sources, shared by the compose editor (CodeMirror) and
// the Create-container form. Suggestions blend the host's locally-pulled images
// (instant, offline) with a Docker Hub search; tag suggestions blend local tags
// for the repo with Hub's tag list. Everything degrades gracefully to local-only
// when the network/daemon search is unavailable.

import { api } from "./api";

export interface ImageSuggestion {
  value: string; // the full repo[:tag] to insert
  detail?: string; // a short hint shown next to it (stars, "local", description)
  local?: boolean;
}

// Local repo:tags are cached briefly so rapid keystrokes don't refetch the whole
// image list each time.
let localCache: { at: number; tags: string[] } | null = null;
async function localTags(): Promise<string[]> {
  if (localCache && Date.now() - localCache.at < 30_000) return localCache.tags;
  try {
    const imgs = await api.images();
    const tags = imgs
      .flatMap((i) => i.repoTags ?? [])
      .filter((t) => t && !t.startsWith("<none>"));
    localCache = { at: Date.now(), tags: Array.from(new Set(tags)).sort() };
  } catch {
    localCache = { at: Date.now(), tags: [] };
  }
  return localCache.tags;
}

// imageNameSuggestions returns repository suggestions for a partial name: local
// images that match first, then Docker Hub search hits (deduped).
export async function imageNameSuggestions(query: string): Promise<ImageSuggestion[]> {
  const q = query.trim().toLowerCase();
  const out: ImageSuggestion[] = [];
  const seen = new Set<string>();
  const push = (s: ImageSuggestion) => { if (!seen.has(s.value)) { seen.add(s.value); out.push(s); } };

  const local = await localTags();
  for (const t of local) {
    if (!q || t.toLowerCase().includes(q)) push({ value: t, detail: "local", local: true });
  }
  if (q.length >= 2) {
    try {
      const hits = await api.searchImages(q);
      for (const h of hits) {
        const detail = [h.official ? "official" : "", h.stars ? `★${h.stars}` : ""].filter(Boolean).join(" ");
        push({ value: h.name, detail: detail || h.description });
      }
    } catch { /* offline / no daemon — local suggestions still stand */ }
  }
  return out.slice(0, 40);
}

// imageTagSuggestions returns tag suggestions for a repository: tags already
// pulled locally first, then Docker Hub's tag list.
export async function imageTagSuggestions(repo: string): Promise<ImageSuggestion[]> {
  const r = repo.trim().toLowerCase();
  if (!r) return [];
  const out: ImageSuggestion[] = [];
  const seen = new Set<string>();
  const push = (tag: string, local: boolean) => {
    if (!tag || seen.has(tag)) return;
    seen.add(tag);
    out.push({ value: tag, detail: local ? "local" : undefined, local });
  };

  const local = await localTags();
  for (const t of local) {
    const i = t.lastIndexOf(":");
    if (i > 0 && t.slice(0, i).toLowerCase() === r) push(t.slice(i + 1), true);
  }
  try {
    // Pass the normalized repo so local-tag matching and the Hub query agree on
    // casing (the backend lowercases too, but keep the two paths consistent).
    for (const tag of await api.imageTags(r)) push(tag, false);
  } catch { /* best-effort */ }
  return out.slice(0, 60);
}
