import { useCallback, useRef } from "react";

// useLatestRequest guards a component's reloads against answers that arrive out
// of order. Each call of the returned begin() starts a new request and returns
// isLatest(), which stays true only until the next begin(). Set state only when
// isLatest() holds: a slow older response (a remote host, a burst of Docker
// events) then can't overwrite a newer one that already arrived.
export function useLatestRequest(): () => () => boolean {
  const gen = useRef(0);
  return useCallback(() => {
    const mine = ++gen.current;
    return () => mine === gen.current;
  }, []);
}
