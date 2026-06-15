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

## Notes

- Authentication goes through `/api/auth/login` (and `/api/auth/2fa` when a code
  is supplied) so the session cookie lands in the browser context.
- Detail views without their own route (network detail, project editor) are
  captured by opening the first list row/card. If the UI changes, adjust the
  `prep`/`openFirstRow` selectors in `generate.mjs`.
- Shot names map 1:1 to the filenames in `docs/images/`.
