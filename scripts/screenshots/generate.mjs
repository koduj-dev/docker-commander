// Docker Commander — user-manual screenshot generator.
//
// Drives a running instance with a headless Chrome (your installed Google
// Chrome — no Playwright browser download) and captures one PNG per agenda
// into docs/images/, matching the 2560x1440 framing of the existing manual.
//
// Usage (from repo root):
//   DC_PASS=… node scripts/screenshots/generate.mjs
//
// Env:
//   DC_BASE_URL  target instance        (default http://127.0.0.1:8470)
//   DC_USER      admin username         (default "admin")
//   DC_PASS      admin password         (required)
//   DC_TOTP      current 6-digit code   (only if localhost 2FA exemption is OFF)
//   CHROME_BIN   chrome executable      (default /usr/bin/google-chrome)
//   ONLY         comma list of shot names to (re)generate, e.g. ONLY=alerts,mcp
//   HEADED       set to 1 to watch the run in a visible window
//
// What the pictures show is chosen here too — the shots are only as good as the
// instance behind them, so point these at something busy:
//   DC_CONTAINER    container to feature in container_detail  (default queue-worker)
//   DC_LOG_SOURCES  containers to tick in the aggregated Logs view
//   DC_NETWORK      network to open in the detail drawer; prefer one with several
//                   containers attached, or the graph view has nothing to draw

import { chromium } from 'playwright-core';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const OUT = resolve(__dirname, '../../docs/images');

const BASE = (process.env.DC_BASE_URL || 'http://127.0.0.1:8470').replace(/\/$/, '');
const USER = process.env.DC_USER || 'admin';
const PASS = process.env.DC_PASS;
const TOTP = process.env.DC_TOTP || '';
const CHROME = process.env.CHROME_BIN || '/usr/bin/google-chrome';
const ONLY = (process.env.ONLY || '').split(',').map((s) => s.trim()).filter(Boolean);

// Which containers to feature. The pictures are only as good as what is running:
// a busy container shows live graphs and a moving log stream, an idle one shows
// flat lines. Override per instance rather than editing the list.
const FEATURED = (process.env.DC_CONTAINER || 'queue-worker').trim();
const LOG_SOURCES = (process.env.DC_LOG_SOURCES || 'app-php-fpm,queue-worker,db-postgres')
  .split(',').map((s) => s.trim()).filter(Boolean);
// Which network to open in the detail drawer. Pick one with several containers
// attached: the graph view of a single-container network is two boxes and a line,
// which shows the feature at its least convincing.
const NETWORKS = [
  ...(process.env.DC_NETWORK || '').split(',').map((s) => s.trim()).filter(Boolean),
  'elastic', 'bridge',
];

// 1280x720 at deviceScaleFactor 2 → 2560x1440 PNGs (matches the existing docs).
const VIEWPORT = { width: 1280, height: 720 };
const SCALE = 2;

if (!PASS) {
  console.error('DC_PASS is required (the admin password for the target instance).');
  process.exit(2);
}

// openTab clicks a tab by its label. Tab text is lowercase in the DOM and shown
// capitalised via CSS, so every match here is case-insensitive.
async function openTab(page, label) {
  const tab = page.locator('button', { hasText: label }).first();
  if (!(await tab.count().catch(() => 0))) return false;
  await tab.click().catch(() => {});
  await page.waitForTimeout(900);
  return true;
}

// clickTitled clicks a control that carries no text of its own — the icon-only
// buttons in the detail drawers are identified by their `title` alone.
async function clickTitled(page, title) {
  const btn = page.locator(`button[title="${title}"]`).first();
  if (!(await btn.count().catch(() => 0))) return false;
  await btn.click().catch(() => {});
  await page.waitForTimeout(900);
  return true;
}

// hideToasts suppresses the alert toast stack for the duration of the shot. On a
// busy instance — which is exactly the instance worth photographing — rules fire
// constantly, and three stacked toasts sit on top of whatever the picture is
// meant to show. They are transient overlays, not part of the agenda being
// documented; the Alerts page is where the manual covers them.
async function hideToasts(page) {
  await page
    .addStyleTag({ content: 'div.fixed.bottom-4.right-4 { display: none !important; }' })
    .catch(() => {});
}

// selectLogSources ticks containers in the aggregated Logs "Sources" panel. The
// selection lives in localStorage, so a fresh browser context starts with none
// and the picture would otherwise be an empty stream. Returns false if not one
// of the wanted containers exists, so the shot fails loudly instead of showing
// nothing.
async function selectLogSources(page, names) {
  const panel = page.locator('div.card').filter({ hasText: 'Sources' }).first();
  if (!(await panel.count().catch(() => 0))) return false;
  let on = 0;
  for (const name of names) {
    const btn = panel.locator('button', { hasText: name }).first();
    if (!(await btn.count().catch(() => 0))) continue;
    // The selected state is the exact class token `bg-panel2`; the unselected one
    // carries `hover:bg-panel2/60`, which a substring test would confuse for it.
    const cls = ((await btn.getAttribute('class').catch(() => '')) || '').split(/\s+/);
    if (!cls.includes('bg-panel2')) {
      await btn.click().catch(() => {});
      await page.waitForTimeout(250);
    }
    on++;
  }
  return on > 0;
}

// Each agenda: the route to visit, the output filename, and an optional `prep`
// that opens a drawer/detail before the shot. `pick` resolves a dynamic id from
// the API for routes that need one (container detail, …).
const SHOTS = [
  // Host network throughput is a *rate*, so it needs two samples before it can
  // show anything — shot too early, the tile reads "Collecting…" instead.
  { name: 'dashboard', path: '/', settle: 20000 },
  { name: 'containers', path: '/containers' },
  { name: 'images', path: '/images' },
  { name: 'stacks', path: '/stacks' },
  { name: 'projects', path: '/projects' },
  { name: 'volumes', path: '/volumes' },
  { name: 'networks', path: '/networks' },
  { name: 'topology', path: '/topology' },
  {
    // A stream is only worth showing with several sources in it: the colour
    // coding by container is the point of the aggregated view.
    name: 'logs',
    path: '/logs',
    prep: (page) => selectLogSources(page, LOG_SOURCES),
    settle: 4000, // let lines from every selected source arrive
  },
  // The feed fills as Docker emits events, so it needs a moment to have anything
  // in it — an empty list is the one thing this picture must not show.
  { name: 'events', path: '/events', settle: 15000 },
  {
    // The Feed tab is empty until an alert actually fires; the Rules tab shows
    // configured rules, which is the more useful hero shot.
    name: 'alerts',
    path: '/alerts',
    prep: async (page) => {
      // Tab labels are lowercase in the DOM ("rules"), shown capitalised via CSS,
      // so match case-insensitively.
      const tab = page.locator('button', { hasText: /^rules$/i }).first();
      if (await tab.count().catch(() => 0)) {
        await tab.click().catch(() => {});
        await page.waitForTimeout(700);
      }
    },
  },
  { name: 'hosts', path: '/hosts' },
  { name: 'registries', path: '/registries' },
  { name: 'users', path: '/users' },
  { name: 'settings', path: '/settings' },
  // The Settings tabs each get their own picture; they are separate agendas in
  // the manual even though they share a route.
  { name: 'settings_security', path: '/settings', prep: (page) => openTab(page, /^security$/i) },
  { name: 'settings_ldap', path: '/settings', prep: (page) => openTab(page, /^ldap$/i) },
  { name: 'settings_email', path: '/settings', prep: (page) => openTab(page, /^e-?mail$/i) },
  { name: 'audit', path: '/audit' },
  { name: 'templates', path: '/templates' },
  { name: 'mcp', path: '/mcp-tokens' },
  { name: 'mcp_admin', path: '/mcp-admin' },
  {
    // Everything an account holds that decides how it signs in: live sessions,
    // paired authenticators and passkeys, and whether a passkey alone may be the
    // whole login. The tab is not the default one, so it has to be opened.
    name: 'profile_security',
    path: '/profile',
    prep: async (page) => {
      const tab = page.locator('button', { hasText: /^security$/i }).first();
      if (await tab.count().catch(() => 0)) {
        await tab.click().catch(() => {});
        await page.waitForTimeout(900);
      }
    },
  },

  // Detail views.
  {
    name: 'container_detail',
    pick: async (api) => {
      const list = await api('/api/containers');
      const running = list.filter((x) => (x.State || x.state) === 'running');
      const named = (c) => (c.Names?.[0] || c.name || '').replace(/^\//, '');
      // Prefer the container asked for: the shot is meant to show live CPU and
      // memory history, which an idle container renders as two flat lines.
      const c = running.find((x) => named(x).includes(FEATURED)) || running[0] || list[0];
      return c && `/containers/${c.Id || c.id}`;
    },
  },
  {
    // Network detail opens as a drawer from the Networks list (no own route).
    name: 'network_detail',
    path: '/networks',
    prep: async (page) => openFirstRow(page, NETWORKS),
  },
  {
    // …and the same drawer switched to its graph view. The toggle is an icon-only
    // button, so it is found by title: matching on text produced a byte-identical
    // copy of the shot above for as long as this file has existed.
    name: 'network_detail_graph',
    path: '/networks',
    prep: async (page) => {
      if (!(await openFirstRow(page, NETWORKS))) return false;
      return clickTitled(page, 'Graph view');
    },
  },
  {
    // The new-project dialog, opened from the Projects list.
    name: 'project_new',
    path: '/projects',
    prep: async (page) => {
      const btn = page.locator('button', { hasText: /new project/i }).first();
      if (await btn.count().catch(() => 0)) {
        await btn.click().catch(() => {});
        await page.waitForTimeout(900);
        return true;
      }
      return false;
    },
  },
  {
    // The project editor opens as a full drawer from the Projects list, via the
    // per-row "Edit files" button.
    name: 'project_editor',
    path: '/projects',
    prep: async (page) => {
      const edit = page.locator('button[title="Edit files"]').first();
      if (await edit.count().catch(() => 0)) {
        await edit.click().catch(() => {});
        await page.waitForTimeout(1400); // CodeMirror is lazy-loaded
        return true;
      }
      return openFirstRow(page);
    },
  },
];

// Click the first matching list row/card to open its drawer. Tries to match one
// of `prefer` substrings first, else falls back to the first clickable card/row.
// Returns true ONLY after a click actually succeeds — a failed/non-interactive
// candidate is skipped so we never report success on a screenshot that didn't
// open the drawer.
async function openFirstRow(page, prefer = []) {
  const tryClick = async (locator) => {
    if (!(await locator.count().catch(() => 0))) return false;
    try {
      await locator.click({ timeout: 2000 });
      await page.waitForTimeout(900);
      return true;
    } catch {
      return false; // not clickable / detached — keep looking
    }
  };
  for (const want of prefer) {
    if (await tryClick(page.getByText(want, { exact: false }).first())) return true;
  }
  const candidates = [
    'table tbody tr',
    '[role="row"]',
    'li button',
    'button[aria-label]',
    '.cursor-pointer',
  ];
  for (const sel of candidates) {
    if (await tryClick(page.locator(sel).first())) return true;
  }
  return false;
}

async function main() {
  const browser = await chromium.launch({
    executablePath: CHROME,
    headless: process.env.HEADED ? false : true,
  });
  const context = await browser.newContext({
    viewport: VIEWPORT,
    deviceScaleFactor: SCALE,
    colorScheme: 'dark',
  });
  const page = await context.newPage();

  // --- The sign-in screen, shot while still logged out.
  //
  // Only the first step is taken here. Submitting the form for the second-step
  // shot happens AFTER the API login below has confirmed the credentials — a
  // wrong password would otherwise cost two attempts per run instead of one, and
  // the server allows five per fifteen minutes before it stops answering. Two
  // runs with a typo used to be enough to lock the operator out of their own
  // instance, which is a poor thing for a screenshot tool to do.
  const wantsLogin = !ONLY.length || ONLY.includes('login') || ONLY.includes('login_2fa');
  if (wantsLogin) {
    // Deliberately NOT swallowed: a refused connection would otherwise be
    // screenshotted as a blank page and committed as documentation.
    await page.goto(BASE + '/', { waitUntil: 'networkidle' });
    await page.waitForTimeout(1200); // the passkey button waits on /webauthn/support
    await page.screenshot({ path: `${OUT}/login.png` });
    console.log('✓ login  →  login.png');
  }

  // --- Authenticate via the JSON API so the session cookie lands in the context.
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' });
  const login = await page.evaluate(
    async ({ user, pass }) => {
      const r = await fetch('/api/auth/login', {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ username: user, password: pass }),
      });
      return { status: r.status, body: await r.json().catch(() => ({})) };
    },
    { user: USER, pass: PASS },
  );
  if (login.status !== 200) {
    const hint = login.status === 429
      ? ' — the attempt budget is spent; restart the instance to clear it (the limiter is in memory)'
      : ' — check DC_USER / DC_PASS';
    throw new Error(`login failed (${login.status})${hint}: ${JSON.stringify(login.body)}`);
  }
  if (login.body.mfaRequired) {
    if (!TOTP) throw new Error('2FA required — set DC_TOTP (or enable the localhost exemption).');
    // The credentials are known good now, so driving the form costs a *successful*
    // login — which resets the attempt budget rather than spending it.
    if (wantsLogin) {
      const inputs = page.locator('form input');
      if (await inputs.count().catch(() => 0) >= 2) {
        await inputs.nth(0).fill(USER).catch(() => {});
        await inputs.nth(1).fill(PASS).catch(() => {});
        await page.locator('form button[type=submit], form button').first().click().catch(() => {});
        await page.waitForTimeout(1400);
        if (await page.locator('text=/two-factor/i').count().catch(() => 0)) {
          await page.screenshot({ path: `${OUT}/login_2fa.png` });
          console.log('✓ login_2fa  →  login_2fa.png');
        } else {
          console.warn('• skip login_2fa: the second step did not render');
        }
      }
    }
    const v = await page.evaluate(
      async ({ token, code }) => {
        const r = await fetch('/api/auth/2fa', {
          method: 'POST',
          headers: { 'content-type': 'application/json' },
          body: JSON.stringify({ mfaToken: token, code }),
        });
        return { status: r.status };
      },
      { token: login.body.mfaToken, code: TOTP },
    );
    if (v.status !== 200) throw new Error(`2FA verify failed (${v.status})`);
  }
  console.log(`✓ authenticated as ${USER}`);

  const api = async (path) =>
    page.evaluate(async (p) => (await fetch(p)).json(), path).catch(() => null);

  const todo = ONLY.length ? SHOTS.filter((s) => ONLY.includes(s.name)) : SHOTS;
  const done = [];
  const failed = [];

  for (const shot of todo) {
    try {
      let path = shot.path;
      if (shot.pick) {
        path = await shot.pick(api);
        if (!path) {
          console.warn(`• skip ${shot.name}: nothing to point at`);
          failed.push(shot.name);
          continue;
        }
      }
      // BrowserRouter (history) SPA: the server serves index.html for any path,
      // and the session cookie persists across full loads.
      await page.goto(BASE + path, { waitUntil: 'networkidle' }).catch(async () => {
        await page.goto(BASE + path, { waitUntil: 'domcontentloaded' });
      });
      await page.waitForTimeout(1400); // let data load + charts animate in
      await hideToasts(page); // re-applied per navigation: the style tag does not survive one
      // A prep that cannot find its control returns false. Treat that as a
      // failure rather than shooting anyway: the picture still looks plausible,
      // it just shows the wrong state, and nobody re-checks a green run.
      if (shot.prep && (await shot.prep(page)) === false) {
        throw new Error('prep could not find its control — selector out of date?');
      }
      if (shot.settle) {
        console.log(`  … settling ${shot.settle / 1000}s for ${shot.name}`);
      }
      await page.waitForTimeout(shot.settle ?? 600);
      const file = `${OUT}/${shot.name}.png`;
      await page.screenshot({ path: file });
      console.log(`✓ ${shot.name}  →  ${shot.name}.png`);
      done.push(shot.name);
    } catch (err) {
      console.warn(`✗ ${shot.name}: ${err.message}`);
      failed.push(shot.name);
    }
  }

  await browser.close();
  console.log(`\nDone: ${done.length} captured, ${failed.length} failed/skipped.`);
  if (failed.length) console.log(`Failed/skipped: ${failed.join(', ')}`);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
