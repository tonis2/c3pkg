// Browser tests: start the real server, seed it over HTTP, then drive Chromium.
// Needs the server binary (BIN, default /tmp/c3pkg-bin) and Chromium (CHROMIUM_CMD, default flatpak).
import { test, before, after } from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { mkdtempSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { chromium } from 'playwright-core';

const PORT = 18090, CDP = 19333;
const BASE = `http://localhost:${PORT}`;
const BIN = process.env.BIN || '/tmp/c3pkg-bin';
const SHOTS = process.env.SHOTS || '';
const C3L = process.env.C3L_SAMPLE || '';
const PASSWORD = 'correct-horse-1';

let server, chrome, browser, dataDir;

// ---- tiny cookie-keeping HTTP client for seeding ----
function client() {
  const jar = new Map();
  const cookie = () => [...jar].map(([k, v]) => `${k}=${v}`).join('; ');
  async function req(path, opts = {}) {
    const res = await fetch(BASE + path, { redirect: 'manual', ...opts, headers: { ...opts.headers, cookie: cookie() } });
    for (const c of res.headers.getSetCookie()) {
      const [kv] = c.split(';');
      const i = kv.indexOf('=');
      jar.set(kv.slice(0, i), kv.slice(i + 1));
    }
    return res;
  }
  return {
    req,
    async csrf() { if (!jar.has('c3_csrf')) await req('/login'); return jar.get('c3_csrf'); },
    async form(path, fields) {
      const body = new URLSearchParams({ csrf: await this.csrf(), ...fields });
      return req(path, { method: 'POST', body });
    },
    async multipart(path, fields, file) {
      const fd = new FormData();
      fd.set('csrf', await this.csrf());
      for (const [k, v] of Object.entries(fields)) fd.set(k, v);
      if (file) fd.set('file', new Blob([file.data]), file.name);
      return req(path, { method: 'POST', body: fd });
    },
  };
}

// A minimal stored (uncompressed) zip so the test needs no zip library.
function makeZip(files) {
  const enc = new TextEncoder();
  const crcTable = Array.from({ length: 256 }, (_, n) => { let c = n; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; return c >>> 0; });
  const crc = (b) => { let c = 0xffffffff; for (const x of b) c = crcTable[(c ^ x) & 0xff] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; };
  const parts = [], central = [];
  let offset = 0;
  for (const [name, text] of Object.entries(files)) {
    const n = enc.encode(name), d = enc.encode(text), c = crc(d);
    const lh = Buffer.alloc(30); lh.writeUInt32LE(0x04034b50, 0); lh.writeUInt16LE(20, 4); lh.writeUInt32LE(c, 14); lh.writeUInt32LE(d.length, 18); lh.writeUInt32LE(d.length, 22); lh.writeUInt16LE(n.length, 26);
    parts.push(lh, n, d);
    const ch = Buffer.alloc(46); ch.writeUInt32LE(0x02014b50, 0); ch.writeUInt16LE(20, 4); ch.writeUInt16LE(20, 6); ch.writeUInt32LE(c, 16); ch.writeUInt32LE(d.length, 20); ch.writeUInt32LE(d.length, 24); ch.writeUInt16LE(n.length, 28); ch.writeUInt32LE(offset, 42);
    central.push(ch, n);
    offset += lh.length + n.length + d.length;
  }
  const cd = Buffer.concat(central);
  const end = Buffer.alloc(22); end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(central.length / 2, 8); end.writeUInt16LE(central.length / 2, 10); end.writeUInt32LE(cd.length, 12); end.writeUInt32LE(offset, 16);
  return Buffer.concat([...parts, cd, end]);
}

const c3l = (name) => makeZip({ 'manifest.json': JSON.stringify({ provides: name, sources: ['src/**'], targets: { 'linux-x64': {} } }), [`src/${name}.c3`]: `module ${name};` });

async function waitFor(fn, what, ms = 15000) {
  const end = Date.now() + ms;
  for (;;) {
    try { if (await fn()) return; } catch { /* not up yet */ }
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 200));
  }
}

before(async () => {
  dataDir = mkdtempSync(join(tmpdir(), 'c3pkg-e2e-'));
  mkdirSync(join(dataDir, 'uploads'));
  server = spawn(BIN, [], { env: { ...process.env, C3PKG_ADDR: `:${PORT}`, C3PKG_BASE_URL: BASE, C3PKG_DATA_DIR: dataDir, C3PKG_SESSION_SECRET: 'x'.repeat(32), C3PKG_MAX_UPLOAD_MB: '5', GITHUB_CLIENT_ID: 'test', GITHUB_CLIENT_SECRET: 'test' }, stdio: 'ignore' });
  await waitFor(async () => (await fetch(BASE + '/healthz')).ok, 'server');

  const alice = client();
  await alice.form('/register', { email: 'alice@example.com', login: 'alice', name: 'Alice', password: PASSWORD, confirm: PASSWORD });
  const publish = (name, desc, tags, version = '1.0.0') => alice.multipart('/dashboard/packages/new',
    { version, c3_version: '0.7.5', description: desc, tags, license: 'MIT', git_url: `https://github.com/alice/${name}`, docs_url: `https://docs.example.com/${name}` },
    { name: `${name}.c3l`, data: c3l(name) });
  for (const [n, d, t] of [['vulkan', 'Vulkan bindings for C3', 'gpu, vulkan'], ['json', 'Fast JSON parser and writer', 'parsing'], ['http', 'A small HTTP client', 'net, parsing'], ['wgpu', 'WebGPU bindings', 'gpu']]) {
    const res = await publish(n, d, t);
    assert.equal(res.status, 303, `seed ${n}`);
  }
  await alice.multipart('/dashboard/packages/vulkan/edit', { description: 'Vulkan bindings for C3', tags: 'gpu, vulkan', license: 'MIT' }, null);
  if (C3L) {
    const res = await alice.multipart('/dashboard/packages/new', { version: '0.1.0', c3_version: '0.7.5', description: 'Real library', tags: 'gpu' }, { name: 'real.c3l', data: readFileSync(C3L) });
    assert.equal(res.status, 303, 'real library');
  }
  const bob = client();
  await bob.form('/register', { email: 'bob@example.com', login: 'bob', name: 'Bob', password: PASSWORD, confirm: PASSWORD });

  const cmd = (process.env.CHROMIUM_CMD || 'flatpak run org.chromium.Chromium').split(' ');
  chrome = spawn(cmd[0], [...cmd.slice(1), '--headless=new', '--no-sandbox', '--disable-gpu', `--remote-debugging-port=${CDP}`, '--window-size=1440,1000', 'about:blank'], { stdio: 'ignore' });
  await waitFor(async () => (await fetch(`http://127.0.0.1:${CDP}/json/version`)).ok, 'chromium');
  browser = await chromium.connectOverCDP(`http://127.0.0.1:${CDP}`);
});

after(async () => {
  await browser?.close().catch(() => {});
  chrome?.kill();
  // killing the flatpak wrapper leaves the sandboxed browser running
  if (!process.env.CHROMIUM_CMD) spawn('flatpak', ['kill', 'org.chromium.Chromium'], { stdio: 'ignore' });
  server?.kill();
});

async function page(opts = {}) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 }, permissions: ['clipboard-read', 'clipboard-write'], ...opts });
  const p = await ctx.newPage();
  p.errors = [];
  p.on('pageerror', (e) => p.errors.push(String(e)));
  p.on('console', (m) => { if (m.type() === 'error') p.errors.push(m.text()); });
  return p;
}
// The page's CSP forbids eval, which Playwright's waitForFunction relies on, so poll instead.
async function until(fn, what = 'condition', ms = 10000) {
  const end = Date.now() + ms;
  for (;;) {
    if (await fn()) return;
    if (Date.now() > end) throw new Error(`timed out waiting for ${what}`);
    await new Promise((r) => setTimeout(r, 100));
  }
}
const shot = async (p, name) => { if (SHOTS) await p.screenshot({ path: join(SHOTS, `${name}.png`), fullPage: true }); };
async function login(p, email = 'alice@example.com') {
  await p.goto(BASE + '/login');
  await p.fill('input[name=email]', email);
  await p.fill('input[name=password]', PASSWORD);
  await Promise.all([p.waitForURL('**/dashboard'), p.click('button[type=submit]')]);
}

test('components load without console errors or CSP violations', async () => {
  const p = await page();
  for (const path of ['/', '/packages/vulkan', '/?q=gpu']) {
    await p.goto(BASE + path);
    await until(() => p.evaluate(() => !!customElements.get('c3-search')), 'c3-search to be defined');
  }
  assert.deepEqual(p.errors, []);
});

test('search suggests packages and supports keyboard selection', async () => {
  const p = await page();
  await p.goto(BASE + '/');
  await shot(p, 'home');
  await p.locator('.header-search input[name=q]').pressSequentially('vul', { delay: 30 });
  const first = p.locator('.header-search .suggest li').first();
  await first.waitFor();
  assert.match(await first.innerText(), /vulkan/);
  assert.equal(await p.locator('.header-search input[name=q]').getAttribute('aria-expanded'), 'true');
  await shot(p, 'home-suggest');
  await p.keyboard.press('ArrowDown');
  await Promise.all([p.waitForURL('**/packages/vulkan'), p.keyboard.press('Enter')]);
  await p.goto(BASE + '/');
  await p.locator('.header-search input[name=q]').pressSequentially('zzzz');
  await p.waitForTimeout(500);
  assert.equal(await p.locator('.header-search .suggest').count(), 0, 'no suggestions for nonsense');
  // Escape closes the list
  await p.fill('.header-search input[name=q]', '');
  await p.locator('.header-search input[name=q]').pressSequentially('gpu');
  await p.locator('.header-search .suggest li').first().waitFor();
  await p.keyboard.press('Escape');
  assert.equal(await p.locator('.header-search .suggest').count(), 0);
  assert.deepEqual(p.errors, []);
});

test('"/" focuses the search box but not while typing elsewhere', async () => {
  const p = await page();
  await p.goto(BASE + '/?q=gpu');
  await p.locator('body').click({ position: { x: 5, y: 500 } });
  await p.keyboard.press('/');
  assert.equal(await p.evaluate(() => document.activeElement?.getAttribute('name')), 'q');
  await p.goto(BASE + '/login');
  await p.focus('input[name=email]');
  await p.keyboard.type('a/b');
  assert.equal(await p.inputValue('input[name=email]'), 'a/b');
});

test('login dialog: wrong password shows an error, right password signs in', async () => {
  const p = await page();
  await p.goto(BASE + '/packages/vulkan');
  await p.click('header a[href="/login"]');
  const dialog = p.locator('dialog.login-dialog');
  await dialog.waitFor({ state: 'visible' });
  assert.equal(p.url(), BASE + '/packages/vulkan', 'stays on the page');
  await shot(p, 'login-dialog');
  await dialog.locator('input[name=email]').fill('alice@example.com');
  await dialog.locator('input[name=password]').fill('wrong-password-1');
  await dialog.locator('button[type=submit]').click();
  await dialog.locator('.error').waitFor();
  assert.match(await dialog.locator('.error').innerText(), /Wrong email or password/);
  assert.equal(await dialog.locator('input[name=password]').inputValue(), '', 'password cleared');
  await dialog.locator('input[name=password]').fill(PASSWORD);
  await Promise.all([p.waitForNavigation(), dialog.locator('button[type=submit]').click()]);
  assert.equal(p.url(), BASE + '/packages/vulkan', 'returns to the same page');
  assert.ok(await p.locator('header form[action="/logout"]').count(), 'now logged in');
  assert.equal(await p.locator('c3-login-dialog').count(), 0, 'dialog is not rendered for signed-in users');
  assert.deepEqual(p.errors.filter((e) => !/401/.test(e)), []);
});

test('login dialog closes with Escape and a modifier-click still opens the full page', async () => {
  const p = await page();
  await p.goto(BASE + '/');
  await p.click('header a[href="/login"]');
  await p.locator('dialog.login-dialog').waitFor({ state: 'visible' });
  await p.keyboard.press('Escape');
  assert.equal(await p.locator('dialog.login-dialog[open]').count(), 0);
  // A shift-click means "open in a new window": the dialog must leave it alone.
  const prevented = await p.evaluate(() => new Promise((resolve) => {
    window.addEventListener('click', (e) => { resolve(e.defaultPrevented); e.preventDefault(); }, { once: true });
    document.querySelector('header a[href="/login"]').dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true, shiftKey: true, button: 0 }));
  }));
  assert.equal(prevented, false);
});

test('star button toggles in place via the API', async () => {
  const p = await page();
  await login(p, 'bob@example.com');
  await p.goto(BASE + '/packages/vulkan');
  const btn = p.locator('c3-star-button button');
  assert.match(await btn.innerText(), /☆ Star · 0/);
  let navigated = false;
  p.on('framenavigated', () => { navigated = true; });
  await btn.click();
  await until(async () => (await btn.innerText()).includes('Starred'), 'starred');
  assert.match(await btn.innerText(), /★ Starred · 1/);
  assert.equal(await btn.getAttribute('aria-pressed'), 'true');
  await btn.click();
  await until(async () => (await btn.innerText()).includes('☆'), 'unstarred');
  assert.match(await btn.innerText(), /☆ Star · 0/);
  assert.equal(navigated, false, 'no page navigation');
  await btn.click();
  await p.reload();
  assert.match(await p.locator('c3-star-button button').innerText(), /★ Starred · 1/, 'persisted');
  await p.click('c3-star-button button');
  assert.deepEqual(p.errors, []);
});

test('upload zone validates the file before submitting', async () => {
  const p = await page();
  await login(p);
  await p.goto(BASE + '/dashboard/packages/new');
  await shot(p, 'new-package');
  const input = p.locator('c3-upload input[type=file]');
  await input.setInputFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('hello') });
  await p.locator('c3-upload .error').waitFor();
  assert.match(await p.locator('c3-upload').innerText(), /\.c3l extension/);
  assert.equal(await input.evaluate((el) => el.validity.valid), false, 'form cannot be submitted');
  await input.setInputFiles({ name: 'big.c3l', mimeType: 'application/octet-stream', buffer: Buffer.alloc(6 * 1024 * 1024) });
  assert.match(await p.locator('c3-upload .error').innerText(), /limit is \d+ MB/);
  await input.setInputFiles({ name: 'ok.c3l', mimeType: 'application/octet-stream', buffer: c3l('okpkg') });
  await until(async () => (await p.locator('c3-upload.has-file').count()) === 1, 'has-file');
  assert.match(await p.locator('c3-upload').innerText(), /ok\.c3l/);
  assert.equal(await input.evaluate((el) => el.validity.valid), true);
  await shot(p, 'new-package-file');
  // submit for real
  await p.fill('input[name=version]', '1.0.0');
  await p.fill('input[name=c3_version]', '0.7.5');
  await p.fill('input[name=description]', 'Uploaded from the browser test');
  await Promise.all([p.waitForURL('**/packages/okpkg'), p.click('main button[type=submit]')]);
  assert.match(await p.locator('h1').first().innerText(), /okpkg/);
  assert.deepEqual(p.errors, []);
});

test('copy button puts the download command on the clipboard', async () => {
  const p = await page();
  await p.goto(BASE + '/packages/vulkan');
  await p.click('c3-install-box button.copy');
  await until(async () => (await p.locator('c3-install-box button').innerText()).includes('Copied'), 'Copied');
  const text = await p.evaluate(() => navigator.clipboard.readText());
  assert.equal(text, `curl -fL -o vulkan-1.0.0.c3l ${BASE}/packages/vulkan/1.0.0/download`);
});

test('pages work without JavaScript', async () => {
  const p = await page({ javaScriptEnabled: false });
  await p.goto(BASE + '/');
  await p.fill('.header-search input[name=q]', 'json');
  await Promise.all([p.waitForURL('**/?q=json'), p.press('.header-search input[name=q]', 'Enter')]);
  assert.match(await p.locator('.pkg-card').first().innerText(), /json/);
  await p.click('header a[href="/login"]');
  await p.waitForURL('**/login');
  assert.ok(await p.locator('form[action="/login"] input[name=password]').count());
});

test('screenshots of the main pages', async (t) => {
  if (!SHOTS) return t.skip('set SHOTS to a directory to capture screenshots');
  const p = await page();
  await p.goto(BASE + '/?q=gpu&sort=downloads'); await shot(p, 'search');
  await p.goto(BASE + '/packages/vulkan'); await shot(p, 'package');
  await p.goto(BASE + '/packages/vulkan/versions'); await shot(p, 'package-versions');
  await p.goto(BASE + '/register'); await shot(p, 'register');
  await login(p);
  await shot(p, 'dashboard');
  await p.goto(BASE + '/settings'); await shot(p, 'settings');
  await p.goto(BASE + '/dashboard/packages/vulkan/publish'); await shot(p, 'publish');
  await p.goto(BASE + '/users/alice'); await shot(p, 'user');
  await p.setViewportSize({ width: 390, height: 844 });
  await p.goto(BASE + '/'); await shot(p, 'mobile-home');
  const right = await p.evaluate(() => ['.header-search input', '.seg', '.pkg-card'].map(sel => document.querySelector(sel).getBoundingClientRect().right));
  assert.ok(right.every(r => r <= 390 - 16 + 0.5), `mobile home overflows the gutter: ${right}`);
  await p.goto(BASE + '/packages/vulkan'); await shot(p, 'mobile-package');
});
