# Manual screenshot generator

Regenerates the agenda screenshots in [`docs/images/`](../../docs/images/) by
driving a **running** Docker Commander instance with a headless Chrome. One PNG
per agenda, framed at **2560×1440** (1280×720 @ deviceScaleFactor 2) on the dark
theme — matching the existing manual.

It uses **`playwright-core`** and your **already-installed Google Chrome**, so no
Playwright browser download is needed.

## Prerequisites

- A running instance (default `http://127.0.0.1:8470`) with real Docker data.
- Google Chrome at `/usr/bin/google-chrome` (override with `CHROME_BIN`).
- Admin credentials for that instance.

## Run

```bash
cd scripts/screenshots
npm install                       # one-time: pulls playwright-core only
DC_USER=admin DC_PASS=… npm run shoot
```

From the repo root you can also run `DC_PASS=… node scripts/screenshots/generate.mjs`.

## Environment

| Var           | Default                  | Notes                                                        |
| ------------- | ------------------------ | ------------------------------------------------------------ |
| `DC_BASE_URL` | `http://127.0.0.1:8470`  | Target instance.                                             |
| `DC_USER`     | `admin`                  | Admin username.                                              |
| `DC_PASS`     | —                        | **Required.** Admin password.                                |
| `DC_TOTP`     | —                        | Current 6-digit code; only if the localhost 2FA exemption is off. |
| `CHROME_BIN`  | `/usr/bin/google-chrome` | Chrome executable.                                           |
| `ONLY`        | —                        | Comma list of shot names to (re)generate, e.g. `ONLY=alerts,mcp`. |
| `HEADED`      | —                        | Set to `1` to watch the run in a visible window.             |

The pictures are only as good as the instance behind them, so these choose what
they show. Point them at something under real load — an idle daemon photographs
as flat lines and empty feeds.

| Var              | Default                                | Notes                                                  |
| ---------------- | -------------------------------------- | ------------------------------------------------------ |
| `DC_CONTAINER`   | `queue-worker`                         | Container to feature in `container_detail`.            |
| `DC_LOG_SOURCES` | `app-php-fpm,queue-worker,db-postgres` | Containers to tick in the aggregated Logs view.        |
| `DC_NETWORK`     | —                                      | Network to open in the detail drawer. Prefer one with several containers attached: the graph view of a single-container network is two boxes and a line. |

## Notes

- Authentication goes through `/api/auth/login` (and `/api/auth/2fa` when a code
  is supplied) so the session cookie lands in the browser context.
- Detail views without their own route (network detail, project editor) are
  captured by opening the first list row/card. If the UI changes, adjust the
  `prep`/`openFirstRow` selectors in `generate.mjs`.
- A `prep` that cannot find its control now **fails the shot** instead of
  photographing the page unchanged. It used to shoot anyway, which is how
  `network_detail.png` and `network_detail_graph.png` stayed byte-identical: the
  graph toggle is an icon-only button, the selector matched on text, and the
  silent miss produced a plausible-looking duplicate nobody re-checked.
- The alert toast stack is hidden for the duration of a run. On an instance busy
  enough to be worth photographing, rules fire constantly and three stacked toasts
  sit on top of whatever the picture is meant to show.
- Some shots wait before firing (`settle`): the events feed needs time to collect
  anything, and the dashboard's network tile is a *rate*, so it reads
  "Collecting…" until a second sample arrives.
- Shot names map 1:1 to the filenames in `docs/images/`.
