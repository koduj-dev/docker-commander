import { useEffect } from "react";

// The browser tab is the one label that survives being minimised, and with a
// dozen agendas open in tabs "Docker Commander" twelve times over is no label at
// all. Every screen therefore names itself first and the app second — the page
// is what distinguishes the tab, the app name only qualifies it.
export const APP_NAME = "Docker Commander";

// pageTitle builds the document title for a screen. Kept pure (and separate from
// the hook) so it can be tested without a DOM, and so an empty or duplicated
// title degrades to the bare app name rather than rendering "· Docker Commander".
export function pageTitle(title?: string | null): string {
  const t = (title ?? "").trim();
  if (t === "" || t === APP_NAME) return APP_NAME;
  return `${t} · ${APP_NAME}`;
}

// useDocumentTitle keeps document.title in step with the screen. It restores the
// previous title on unmount, so a screen that mounts briefly (a redirect through
// the login shell, say) cannot leave its name behind on the tab it navigated to.
export function useDocumentTitle(title?: string | null): void {
  useEffect(() => {
    const previous = document.title;
    document.title = pageTitle(title);
    return () => {
      document.title = previous;
    };
  }, [title]);
}
