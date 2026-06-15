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
//   DC_USER      admin username         (default "filip")
//   DC_PASS      admin password         (required)
//   DC_TOTP      current 6-digit code   (only if localhost 2FA exemption is OFF)
//   CHROME_BIN   chrome executable      (default /usr/bin/google-chrome)
//   ONLY         comma list of shot names to (re)generate, e.g. ONLY=alerts,mcp
//   HEADED       set to 1 to watch the run in a visible window

import { chromium } from 'playwright-core';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const __dirname = dirname(fileURLToPath(import.meta.url));
const OUT = resolve(__dirname, '../../docs/images');

const BASE = (process.env.DC_BASE_URL || 'http://127.0.0.1:8470').replace(/\/$/, '');
const USER = process.env.DC_USER || 'filip';
const PASS = process.env.DC_PASS;
const TOTP = process.env.DC_TOTP || '';
const CHROME = process.env.CHROME_BIN || '/usr/bin/google-chrome';
const ONLY = (process.env.ONLY || '').split(',').map((s) => s.trim()).filter(Boolean);

// 1280x720 at deviceScaleFactor 2 → 2560x1440 PNGs (matches the existing docs).
const VIEWPORT = { width: 1280, height: 720 };
const SCALE = 2;

if (!PASS) {
  console.error('DC_PASS is required (the admin password for the target instance).');
  process.exit(2);
}

// Each agenda: the route to visit, the output filename, and an optional `prep`
// that opens a drawer/detail before the shot. `pick` resolves a dynamic id from
// the API for routes that need one (container detail, …).
const SHOTS = [
  { name: 'dashboard', path: '/' },
  { name: 'containers', path: '/containers' },
  { name: 'images', path: '/images' },
  { name: 'stacks', path: '/stacks' },
  { name: 'projects', path: '/projects' },
  { name: 'volumes', path: '/volumes' },
  { name: 'networks', path: '/networks' },
  { name: 'topology', path: '/topology' },
  { name: 'logs', path: '/logs' },
  { name: 'events', path: '/events' },
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
  { name: 'audit', path: '/audit' },
  { name: 'templates', path: '/templates' },
  { name: 'mcp', path: '/mcp-tokens' },
  { name: 'mcp_admin', path: '/mcp-admin' },

  // Detail views.
  {
    name: 'container_detail',
    pick: async (api) => {
      const list = await api('/api/containers');
      const c = list.find((x) => (x.State || x.state) === 'running') || list[0];
      return c && `/containers/${c.Id || c.id}`;
    },
  },
  {
    // Network detail opens as a drawer from the Networks list (no own route).
    name: 'network_detail',
    path: '/networks',
    prep: async (page) => openFirstRow(page, ['elastic', 'bridge']),
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
async function openFirstRow(page, prefer = []) {
  for (const want of prefer) {
    const el = page.getByText(want, { exact: false }).first();
    if (await el.count().catch(() => 0)) {
      await el.click().catch(() => {});
      await page.waitForTimeout(900);
      return true;
    }
  }
  const candidates = [
    'table tbody tr',
    '[role="row"]',
    'li button',
    'button[aria-label]',
    '.cursor-pointer',
  ];
  for (const sel of candidates) {
    const el = page.locator(sel).first();
    if (await el.count().catch(() => 0)) {
      await el.click().catch(() => {});
      await page.waitForTimeout(900);
      return true;
    }
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
    throw new Error(`login failed (${login.status}): ${JSON.stringify(login.body)}`);
  }
  if (login.body.mfaRequired) {
    if (!TOTP) throw new Error('2FA required — set DC_TOTP (or enable the localhost exemption).');
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
      if (shot.prep) await shot.prep(page);
      await page.waitForTimeout(600);
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
