---
name: dc-web-ui
description: Docker Commander web UI layout conventions — modal sizing scale, z-index layering, when/why to use tabs, live-preview panes, card-list management surfaces, and the built-in-vs-user (read-only) model with Duplicate. Read BEFORE creating or editing any React modal/dialog, tabbed surface, builder, or Templates-style management page under web/src. Complements the user-level /enterprise-design (which owns colors/typography/tokens).
---

# Docker Commander — web UI layout & interaction conventions

These are **project decisions already made** for `web/src` (React 19 + Tailwind 4,
`lucide-react`, `clsx`, dark theme). Follow them so the user doesn't have to keep
re-specifying sizes/patterns. For colors, typography, spacing tokens and the
`card`/`btn-*`/`input`/`label` utilities, defer to **/enterprise-design** and
`web/src/index.css` — this skill is only **layout, modals, tabs, and management
surfaces**.

When this conflicts with older code, this skill wins — bring the code up to it.

## Modal sizing scale (IMPORTANT — default WIDE)

The user wants modals **roomy**, not cramped. Narrow modals were repeatedly
rejected (2026-06-12). Pick the smallest tier that isn't tight; when unsure, go
one tier wider.

| Modal content | Width class on the `card` panel |
|---|---|
| Tiny text form (name + maybe 1–2 fields) | `w-full max-w-lg` |
| Standard form (several fields, no code) | `w-full max-w-2xl` |
| **Anything with a code/YAML textarea or editor** (service block, shared definition, snippet) | `w-full max-w-3xl` |
| Two-pane form **with a live preview** (e.g. New project) | `w-[92vw] max-w-[1500px]` while the preview shows, else `w-full max-w-2xl` (toggle via `clsx`) |
| Full multi-file editor (project / template files) | `w-[92vw] h-[90vh]` |
| Read-only output / log / resolved-compose panel | `w-[70vw] max-h-[80vh]` |

Never ship a code/YAML modal at `max-w-md`/`max-w-xl` — that's the mistake that
keeps getting flagged. Always pair a width with `flex flex-col` and a height cap
(`max-h-[88vh]`/`max-h-[90vh]`) so the body scrolls.

**Big modals use viewport-relative width.** A pure `max-w-*` cap (even `max-w-6xl`)
looks small and lost on a 2K/ultrawide monitor — large/two-pane modals were flagged
as "malej" on a wide screen (2026-06-12). So for the big tiers use
`w-[92vw] max-w-[1500px]` (fills wide screens, still sane on small ones), not a bare
`max-w-6xl`. Small text forms can stay `w-full max-w-lg/2xl`.

## Modal anatomy & z-index layering

Custom modal skeleton (matches every modal in the app):

```tsx
<div className="fixed inset-0 z-[55] bg-black/60 grid place-items-center p-6" onClick={onClose}>
  <div className="card w-full max-w-3xl flex flex-col max-h-[88vh]" onClick={(e) => e.stopPropagation()}>
    <div className="flex items-center gap-3 p-4 border-b border-border">
      <Icon className="h-4 w-4 text-accent" /><div className="font-medium">Title</div>
      <button type="button" className="btn-ghost px-2 py-1.5 ml-auto" onClick={onClose}><X className="h-4 w-4" /></button>
    </div>
    <div className="p-4 space-y-3 overflow-y-auto">{/* body */}</div>
    <div className="flex justify-end gap-2 p-4 border-t border-border">
      <button className="btn-ghost px-3 py-1.5 text-sm" onClick={onClose}>Cancel</button>
      <button className="btn-primary px-3 py-1.5 text-sm disabled:opacity-40" disabled={busy}>{busy ? <Loader2 className="h-4 w-4 animate-spin"/> : <Save className="h-4 w-4"/>} Save</button>
    </div>
  </div>
</div>
```

z-index layers (don't invent new values):
- `z-50` — a primary full-screen modal opened from a page (e.g. ProjectEditor).
- `z-[55]` — a create/output modal opened from a page (New project, output panel).
- `z-[60]` — a **nested** modal opened from inside another modal (Save-as,
  Block/Fragment editor, Compose summary).
- `z-[80]` is reserved for `useDialogs()` (confirm/prompt/alert) so they always sit
  on top — never put a custom modal there.

Always use **`useDialogs()`** (`components/Dialog.tsx`) for confirm / prompt /
alert — never `window.confirm`/`prompt`. Destructive confirms pass `danger: true`.

## Tabs — when and why

**Decision (2026-06-12):** a surface that stacks **3+ distinct, independently
-browsable sections** in one scroll gets cluttered ("nepřehledné"). Split it into
tabs instead of one long column. Each tab shows a **count badge**.

Two tab styles, by level:

1. **Top-level tabs** — bordered pills. Used for mutually-exclusive *modes* or the
   main sections of a management page (New project: Template / Builder / Import;
   Templates page: Presets / Service blocks / Shared definitions).
   ```tsx
   <button className={clsx("flex items-center gap-2 px-3 py-1.5 text-sm rounded-lg border",
     active ? "border-accent bg-accent/10 text-text" : "border-border text-muted hover:text-text")}>
     {icon} {label} <span className="text-[10px] text-muted">{count}</span>
   </button>
   ```

2. **Inner sub-tabs** — a segmented control, for splitting *within* a tab (the
   builder's Services / Shared defs / Variables).
   ```tsx
   <div className="flex gap-1 rounded-lg bg-panel2/50 p-0.5">
     <button className={clsx("flex-1 flex items-center justify-center gap-1.5 px-2 py-1.5 rounded-md text-sm",
       active ? "bg-panel text-text shadow-sm" : "text-muted hover:text-text")}>
       {icon} {label}{count > 0 ? <span className="text-[10px] bg-accent/20 text-accent rounded-full px-1.5 leading-4">{count}</span> : null}
     </button>
   </div>
   ```

Put a page's primary "New …" action in the `PageHeader` `actions` slot and make it
**contextual to the active tab** (e.g. "New service block" only on the blocks tab).
Render each tab's content with `{tab === "x" && (…)}`; keep `EmptyState` per tab.
Move incidental panels (e.g. **Variables**) into their own sub-tab rather than
piling them at the bottom — the user explicitly dislikes the long stacked form.

## Live-preview pane (form → resulting artifact)

When a create/build form produces an artifact worth seeing first (the assembled
`compose.yml`), add a right-side preview pane inside the modal; widen the modal to
`max-w-6xl` while it shows. Debounce the fetch (~350 ms) and guard against
out-of-order responses (a `cancelled` flag or a seq ref).

```tsx
<div className="flex-1 flex min-h-0">
  <div className="flex-1 min-w-0 p-4 space-y-3 overflow-y-auto">{/* form */}</div>
  {showPreview && (
    <div className="hidden md:flex w-[42%] shrink-0 border-l border-border flex-col min-h-0">
      <div className="flex items-center gap-2 px-3 py-2 border-b border-border text-xs text-muted">
        <Eye className="h-3.5 w-3.5" /> Preview — <span className="font-mono">{name}</span>{busy && <Loader2 className="h-3 w-3 animate-spin ml-auto" />}
      </div>
      <div className="flex-1 overflow-auto p-3 bg-panel2/40"><pre className="text-xs font-mono whitespace-pre text-text/90">{content || "…"}</pre></div>
    </div>
  )}
</div>
```

Keep the gate consistent across `showPreview`, `canSubmit`, and the submit branch —
if any one includes a new input (e.g. fragments), they all must, or you silently
drop a selection.

## Card-list management surface

Reusable items (presets, blocks, fragments) render as a `grid grid-cols-1 md:grid-cols-2 gap-2`
of `card p-3 flex items-start gap-3`: left = name + **source badge** + muted
description; right = a `shrink-0` row of icon `btn-ghost` buttons (`h-4 w-4`).

Action order, left→right: **Edit/View** → **Duplicate** (`Copy`) → secondary
(Rename / Download) → **Delete** (`text-danger`, always last). Every destructive
action confirms via `useDialogs`.

Source badge:
```tsx
source === "user"
  ? <span className="text-[10px] uppercase tracking-wide text-accent border border-accent/40 rounded px-1">yours</span>
  : <span className="text-[10px] uppercase tracking-wide text-muted border border-border rounded px-1">built-in</span>
```

## Built-in vs user (read-only) model

Anything that ships with the app (presets, blocks, fragments) is **read-only**:
its editor opens with `readOnly` inputs and a "Close" (not "Save") footer; its
file/update API routes 404 on a non-numeric (built-in) id. User-saved items are
editable and deletable.

Always give built-ins a **Duplicate** action → a new **editable user copy**. When
the source is a built-in whose content is parameterized (`{{.Var}}`), **render it
with default variables first** (user copies are literal snapshots/blocks, never
re-rendered), so the copy has concrete values, not stray `{{…}}`. Name the copy via
a prompt defaulting to `` `${name} copy` ``.

## Why these exist (so they don't get re-litigated)

- **Wide modals** — narrow modals felt cramped, especially with YAML; widened on
  2026-06-12. See memory `wider-modal-dialogs`.
- **Tabs** — the Templates page (3 sections) and the builder (services + shared
  defs + variables) were one long, cluttered scroll; tabs separate concerns and
  the count badges show what's selected at a glance.
- **Live preview** — users want to see the assembled compose before committing to
  create the project.
- **Duplicate of built-ins** — built-ins are read-only, so cloning into an editable
  copy (rendered with defaults) is how a user customizes a shipped preset/block.
