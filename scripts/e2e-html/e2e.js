// End-to-end test of the single-file browser build (dist/easyvna-web.html)
// in headless Chromium. Run through scripts/e2e-html.sh.
//
//   node scripts/e2e-html/e2e.js dist/easyvna-web.html [screenshot-dir]
//
// For both file:// and http:// (a local static server that mimics GitHub
// Pages) it checks:
//   * the simulator (?demo): profiles, sweep, REF, AVG, HOLD, TUNE + trim,
//     COMPARE, snapshots and exports, report (A4/Letter, download),
//     Export data / Import data, persistence across a reload;
//   * the Web Serial path with a fake navigator.serial whose port is backed
//     by the Go NanoVNA simulator (EasyVNAHost.goMockShell): requestPort on
//     the Connect click, open, readable/writable, driver; unplug, replug,
//     reconnect after reload;
//   * the notice when the browser has no Web Serial;
//   * (http) "Download for offline use" gives a byte-identical file that runs;
//   * zero console errors, and no request other than the page itself
//     (everything else is blocked and reported).
// PLAYWRIGHT_DIR may point at a directory containing node_modules/playwright.
'use strict';
const fs = require('fs');
const os = require('os');
const path = require('path');
const http = require('http');
const crypto = require('crypto');
const { chromium } = require(process.env.PLAYWRIGHT_DIR
  ? path.join(process.env.PLAYWRIGHT_DIR, 'node_modules', 'playwright') : 'playwright');

const HTML = path.resolve(process.argv[2] || 'dist/easyvna-web.html');
const SHOTS = process.argv[3] ? path.resolve(process.argv[3]) : '';
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'easyvna-e2e-'));
const sha = (b) => crypto.createHash('sha256').update(b).digest('hex');
const PAGE_SHA = sha(fs.readFileSync(HTML));
// A small PNG "photo" for the antenna sheet.
const PHOTO = path.join(TMP, 'antenna.png');
fs.writeFileSync(PHOTO, Buffer.from('iVBORw0KGgoAAAANSUhEUgAAADAAAAAgCAIAAADbtmxLAAAAWElEQVR42mN8+OIDw2ACTAyDDAw6B7FQboS7gwWcvfPAidEoG3XQqINGHTTqoFEHjTpo1EGjDhp10KiDRh00qB3EiL/nalu0gRa2Hu4LGI2ykZKGRkOIAQC8qw5rNf7LwwAAAABJRU5ErkJggg==', 'base64'));
const pdfPages = (buf) => (buf.toString('latin1').match(/\/Type\s*\/Page[^s]/g) || []).length;
function pdfShot(pdf, name) { // PNG of page 1 when pdftoppm is installed
  if (!SHOTS) return;
  const p = path.join(SHOTS, name);
  fs.writeFileSync(p + '.pdf', pdf);
  try { require('child_process').execFileSync('pdftoppm', ['-r', '80', '-png', '-singlefile', p + '.pdf', p]); console.log('  saved ' + p + '.png'); } catch { /* optional */ }
}

let passed = 0;
const failures = [];
function check(name, ok, detail) {
  if (ok) { passed++; console.log('  ok   ' + name); } else { failures.push(name + (detail ? ': ' + detail : '')); console.log('  FAIL ' + name + (detail ? ': ' + detail : '')); }
}
async function step(name, fn) {
  try { await fn(); } catch (e) { check(name, false, e.message.split('\n')[0]); }
}

// Static server shaped like GitHub Pages: /easyvna-web/ serves the page.
function serve() {
  const body = fs.readFileSync(HTML);
  const srv = http.createServer((q, s) => {
    const p = q.url.split('?')[0];
    if (p === '/easyvna-web/' || p === '/easyvna-web/index.html' || p === '/easyvna-web/easyvna-web.html') {
      s.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' });
      s.end(body);
    } else { s.writeHead(404); s.end('not found'); }
  });
  return new Promise((resolve) => srv.listen(0, '127.0.0.1', () => resolve(srv)));
}

const strip = (u) => u.split('#')[0].split('?')[0];
// newContext with error and network watchers. Only the page URL itself may
// be requested; anything else is aborted and reported.
async function newCtx(browser, pageURL, label, opts = {}) {
  const ctx = await browser.newContext({ acceptDownloads: true, viewport: { width: 1360, height: 900 }, colorScheme: 'light', ...opts });
  // print() would open a dialog: count the calls instead.
  await ctx.addInitScript(() => { window.print = () => { window.__printed = (window.__printed || 0) + 1; }; });
  const errors = [], extra = [];
  const own = new Set([strip(pageURL)]);
  ctx.allow = (u) => own.add(strip(u));
  await ctx.route('**/*', (route) => {
    const u = route.request().url();
    if (own.has(strip(u))) return route.continue();
    extra.push(u);
    return route.abort();
  });
  ctx.on('request', (r) => {
    const u = r.url();
    if (u.startsWith('blob:') || u.startsWith('data:') || own.has(strip(u))) return;
    if (!extra.includes(u)) extra.push(u);
  });
  const watch = (pg, who) => {
    pg.on('console', (m) => { if (m.type() === 'error') errors.push(`${who}: ${m.text()}`); });
    pg.on('pageerror', (e) => errors.push(`${who}: ${e.message}`));
  };
  ctx.on('page', (pg) => watch(pg, label + (ctx.pages().length > 1 ? ' popup' : '')));
  ctx.report = () => {
    check(`${label}: no console errors`, errors.length === 0, errors.slice(0, 5).join(' | '));
    check(`${label}: no request besides the page itself`, extra.length === 0, extra.slice(0, 5).join(' | '));
  };
  return ctx;
}

const st = (page) => page.evaluate(() => S.state);
async function ready(page, demo) {
  await page.waitForSelector('#devtext', { timeout: 30000 });
  await page.waitForFunction(() => typeof S !== 'undefined' && S.state, null, { timeout: 30000 });
  if (demo) await page.waitForFunction(() => S.state.connected, null, { timeout: 30000 });
}
const api = (page, m, p, b) => page.evaluate(async ([m, p, b]) => {
  const r = await fetch(p, { method: m, headers: { 'Content-Type': 'application/json' }, body: b ? JSON.stringify(b) : undefined });
  return r.json();
}, [m, p, b]);
async function sweepDone(page, fn) {
  const seq = await page.evaluate(() => (S.res ? S.res.seq : 0));
  await fn();
  await page.waitForFunction((s) => S.res && S.res.seq > s, seq, { timeout: 20000 });
}
async function shot(page, name, full = true) {
  if (!SHOTS) return;
  await page.evaluate(() => { const t = document.getElementById('toast'); if (t) t.hidden = true; });
  await page.waitForTimeout(300);
  await page.screenshot({ path: path.join(SHOTS, name), fullPage: full });
  console.log('  saved ' + path.join(SHOTS, name));
}
async function download(page, action) {
  const [d] = await Promise.all([page.waitForEvent('download', { timeout: 20000 }), action()]);
  const p = path.join(TMP, `${Date.now()}-${d.suggestedFilename()}`);
  await d.saveAs(p);
  return { name: d.suggestedFilename(), file: p, body: fs.readFileSync(p) };
}

// ---------- simulator ----------
async function demoFlow(browser, base, label, shots) {
  const url = base + '?demo';
  const ctx = await newCtx(browser, url, label);
  const page = await ctx.newPage();
  const t0 = Date.now();
  await page.goto(url);
  await ready(page, true);
  console.log(`  (${label}: ready in ${Date.now() - t0} ms)`);
  let s = await st(page);
  check(`${label}: simulator connected`, s.connected && s.info.mock && /SIMULATED/.test(await page.textContent('#devtext')));
  check(`${label}: data dir is browser storage`, /this browser/.test(s.dataDir), s.dataDir);
  check(`${label}: host bar present`, await page.isVisible('#hostbar'));
  check(`${label}: offline button ${base.startsWith('file:') ? 'hidden on file://' : 'shown on http'}`,
    (await page.$('#hostOffline') !== null) === !base.startsWith('file:'));

  await step(`${label}: profiles`, async () => {
    for (const [lbl, id] of [['433', '433'], ['869', '869'], ['915', '915'], ['Survey', 'survey'], ['Wide', 'wide']]) {
      await sweepDone(page, () => page.locator('.profile', { hasText: lbl }).first().click());
      const r = await page.evaluate(() => ({ p: S.res.config.profile, n: S.res.freq.length }));
      if (r.p !== id || r.n < 101) throw new Error(`${lbl}: got ${r.p} with ${r.n} points`);
    }
    check(`${label}: profiles 433/869/915/Survey/Wide each sweep`, true);
  });
  await step(`${label}: sweep`, async () => {
    await sweepDone(page, () => page.locator('.profile', { hasText: '869' }).first().click());
    await sweepDone(page, () => page.click('#aSweep'));
    check(`${label}: SWEEP gives a verdict`, /869/.test(await page.textContent('#verdict')));
  });
  await step(`${label}: REF`, async () => {
    await page.click('#aRef');
    await page.waitForFunction(() => S.state.refs && S.state.refs['869'], null, { timeout: 10000 });
    await page.evaluate(() => fetch('api/mock/antenna', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"name":"869tuned"}' }));
    await sweepDone(page, () => page.click('#aSweep'));
    check(`${label}: REF stored and compared`, (await page.isVisible('#aRefClear')) && (await page.evaluate(() => !!S.res.ref)));
    if (shots) await shot(page, 'html-demo-main.png');
  });
  await step(`${label}: AVG`, async () => {
    await page.click('#aAvg');
    await page.waitForFunction(() => S.state.config.avg === 2, null, { timeout: 5000 });
    await api(page, 'POST', 'api/avg', { n: 1 });
    check(`${label}: AVG ×2 then back to ×1`, true);
  });
  await step(`${label}: HOLD`, async () => {
    await api(page, 'POST', 'api/mock/antenna', { name: 'bend' });
    await page.click('#aHold');
    await page.waitForFunction(() => S.state.hold.on && S.state.continuous, null, { timeout: 10000 });
    await page.waitForTimeout(6000);
    const ok = await page.isVisible('#holdPanel');
    if (shots) await shot(page, 'html-demo-hold.png', false);
    await page.click('#aHold');
    if (await page.isVisible('#askYes')) await page.click('#askYes');
    await page.waitForFunction(() => !S.state.hold.on, null, { timeout: 10000 });
    if ((await st(page)).continuous) await page.click('#aLive');
    await page.waitForFunction(() => !S.state.continuous, null, { timeout: 10000 });
    check(`${label}: HOLD on (live sweeps, panel) and off`, ok);
  });
  await step(`${label}: TUNE + trim`, async () => {
    await api(page, 'POST', 'api/mock/antenna', { name: '869' });
    await page.click('#aTune');
    await page.waitForSelector('#trimLen', { timeout: 15000 });
    await page.fill('#trimName', 'whip-e2e');
    await page.fill('#trimLen', '82');
    await page.click('#trimStart');
    await page.waitForSelector('#trimLog', { timeout: 15000 });
    await page.fill('#trimCut', '1');
    await page.click('#trimLog');
    await page.waitForFunction(() => S.trim && S.trim.entries.length === 2, null, { timeout: 20000 });
    await page.waitForFunction(() => /MHz/.test(document.getElementById('tRes').textContent), null, { timeout: 10000 });
    if (shots) {
      await page.evaluate(() => { document.getElementById('toast').hidden = true; });
      await page.waitForTimeout(800);
      await page.screenshot({ path: path.join(SHOTS, 'html-demo-tune.png') });
      console.log('  saved ' + path.join(SHOTS, 'html-demo-tune.png'));
    }
    await page.click('#tuneClose');
    await page.waitForFunction(() => !S.state.tune.active, null, { timeout: 10000 });
    check(`${label}: TUNE readout and two trim logs`, true);
  });
  await step(`${label}: COMPARE`, async () => {
    await page.click('#aCompare');
    await page.waitForSelector('#cmpStoreA', { timeout: 15000 });
    await page.waitForTimeout(1500);
    await page.fill('#cmpAName', 'stock');
    await page.click('#cmpStoreA');
    await page.waitForSelector('#cmpAdd', { timeout: 15000 });
    await api(page, 'POST', 'api/mock/antenna', { name: '869tuned' });
    await page.waitForTimeout(1500);
    await page.fill('#cmpName', 'tuned');
    await page.click('#cmpAdd');
    await page.waitForFunction(() => S.state.compare.leaderboard.length === 2, null, { timeout: 15000 });
    await page.click('#cmpClose');
    await page.waitForFunction(() => !S.state.continuous, null, { timeout: 10000 });
    check(`${label}: COMPARE A + leaderboard`, true);
  });
  await step(`${label}: snapshots and exports`, async () => {
    await sweepDone(page, () => page.click('#aSweep'));
    await page.click('#snapsPanel summary');
    await page.fill('#snapName', 'e2e snap');
    await page.click('#snapForm button[type=submit]');
    await page.waitForSelector('#snapList td:has-text("e2e snap")', { timeout: 10000 });
    const s1p = await download(page, () => page.click('#snapList a:has-text(".s1p")'));
    check(`${label}: snapshot .s1p download`, s1p.name === 'e2e_snap.s1p' && /^!/.test(s1p.body.toString()) && /# Hz S RI R 50/i.test(s1p.body.toString()), s1p.name);
    const csv = await download(page, () => page.click('.exports a:has-text("CSV")'));
    check(`${label}: last sweep CSV download`, csv.name.endsWith('.csv') && csv.body.toString().split('\n').length > 100, csv.name);
  });
  await step(`${label}: Ctrl+P prints every graph`, async () => {
    await page.evaluate(() => window.dispatchEvent(new Event('beforeprint')));
    await page.emulateMedia({ media: 'print' });
    const cv = await page.evaluate(() => ['swrPlot', 'smith', 'rlPlot', 'zPlot'].map((id) => {
      const c = document.getElementById(id), r = c.getBoundingClientRect(), d = c.getContext('2d').getImageData(0, 0, c.width, c.height).data;
      let ink = 0; for (let i = 0; i < d.length; i += 16) if (d[i + 3] > 0 && (d[i] < 200 || d[i + 1] < 200 || d[i + 2] < 200)) ink++;
      return { id, visible: r.width > 100 && r.height > 50, ink };
    }));
    if (shots) { await page.screenshot({ path: path.join(SHOTS, 'html-ctrlp-print.png'), fullPage: true }); console.log('  saved ' + path.join(SHOTS, 'html-ctrlp-print.png')); }
    await page.emulateMedia({ media: 'screen' });
    await page.evaluate(() => window.dispatchEvent(new Event('afterprint')));
    const back = await page.evaluate(() => document.getElementById('rlPlot').hidden && !document.documentElement.classList.contains('printing'));
    check(`${label}: print view shows SWR, Smith, return loss and R/X, all drawn; restored after`, cv.every((c) => c.visible && c.ink > 200) && back, JSON.stringify(cv));
  });
  await step(`${label}: PRINT antenna sheet`, async () => {
    await page.click('#aPrint');
    await page.waitForSelector('#sh_name', { timeout: 10000 });
    check(`${label}: sheet dialog pre-fills name and band`, (await page.inputValue('#sh_name')) === 'whip-e2e' && /869 MHz LoRa/.test(await page.inputValue('#sh_band')),
      await page.inputValue('#sh_name'));
    await page.fill('#sh_type', '¼λ whip');
    await page.fill('#sh_connector', 'SMA male');
    await page.fill('#sh_lengthMm', '81');
    await page.fill('#sh_cable', 'RG316, 10 cm');
    await page.fill('#sh_notes', 'e2e sheet. '.repeat(80));
    await page.setInputFiles('#sh_photo', PHOTO);
    await page.waitForSelector('#shPhotoPrev:not([hidden])');
    const photo = await page.evaluate(() => S.sheetPhoto);
    check(`${label}: photo downscaled to a JPEG data URL`, photo.startsWith('data:image/jpeg;base64,'));
    if (shots) await shot(page, 'html-sheet-dialog.png', false);
    const [sp] = await Promise.all([ctx.waitForEvent('page', { timeout: 20000 }), page.click('#shPrint')]);
    await sp.waitForURL(/#print$/, { timeout: 20000 });
    await sp.waitForSelector('.sheet');
    await sp.waitForFunction(() => window.__printed >= 1, null, { timeout: 5000 });
    const plots = await sp.evaluate(() => ['svg.swr', 'svg.smith', 'svg.rl', 'svg.z'].map((q) => {
      const el = document.querySelector(q); return el ? el.querySelectorAll('polyline').length : 0;
    }));
    const txt = await sp.textContent('.sheet');
    check(`${label}: sheet opens and prints by itself, with all 4 graphs`, plots.every((n) => n > 0) && /whip-e2e/.test(txt) && /SWR band centre/.test(txt) && /Trim log/.test(txt) && (await sp.$('.photo img')) !== null,
      JSON.stringify(plots));
    for (const paper of ['a4', 'letter']) {
      await sp.evaluate((p) => setPaper(p, false), paper);
      await sp.emulateMedia({ media: 'print' });
      const pdf = await sp.pdf({ preferCSSPageSize: true, printBackground: true });
      check(`${label}: sheet fits one ${paper === 'a4' ? 'A4' : 'Letter'} page`, pdfPages(pdf) === 1, String(pdfPages(pdf)));
      if (shots) pdfShot(pdf, `html-sheet-${paper}`);
    }
    await sp.close();
    await page.waitForFunction(() => document.getElementById('sheetView').hidden, null, { timeout: 5000 });
    // Save as HTML, then the fields come back for the same name.
    await page.click('#aPrint');
    await page.waitForSelector('#sh_name');
    check(`${label}: sheet fields remembered for the antenna`, (await page.inputValue('#sh_connector')) === 'SMA male' && (await page.evaluate(() => !!S.sheetPhoto)));
    const dl = await download(page, () => page.click('#shSave'));
    check(`${label}: Save sheet (HTML)`, /^whip-e2e-sheet-\d{8}-\d{6}\.html$/.test(dl.name) && /Antenna sheet/.test(dl.body.toString()), dl.name);
    await page.click('#shCancel');
  });
  await step(`${label}: report`, async () => {
    await page.click('#aReport');
    await page.waitForSelector('#repGo');
    check(`${label}: report dialog says where it is saved`, /saved in this browser/.test(await page.textContent('#repBody')));
    await page.fill('#repName', 'whip-e2e');
    await page.selectOption('#repTrim', 'whip-e2e');
    const [rp] = await Promise.all([ctx.waitForEvent('page', { timeout: 20000 }), page.click('#repGo')]);
    await rp.waitForURL(/^blob:/, { timeout: 20000 });
    await rp.waitForSelector('.paper', { timeout: 10000 });
    const txt = await rp.textContent('body');
    check(`${label}: report opens as a blob page, with the R/X plot`, rp.url().startsWith('blob:') && /whip-e2e/.test(txt) && (await rp.$('svg.z')) !== null, rp.url());
    await rp.click('.paperpick button[value=letter]');
    const letter = await rp.evaluate(() => document.documentElement.classList.contains('letter'));
    await rp.click('.paperpick button[value=a4]');
    const a4 = await rp.evaluate(() => !document.documentElement.classList.contains('letter'));
    check(`${label}: report A4 / Letter switch`, letter && a4);
    if (shots) { await rp.setViewportSize({ width: 1100, height: 900 }); await rp.waitForTimeout(300); await rp.screenshot({ path: path.join(SHOTS, 'html-report.png') }); console.log('  saved ' + path.join(SHOTS, 'html-report.png')); }
    // The report page asks the app page (its opener) to save the report.
    const dl = await download(page, () => rp.click('a:has-text("Download HTML")'));
    check(`${label}: report "Download HTML"`, /^whip-e2e-\d{8}-\d{6}\.html$/.test(dl.name) && /<html/i.test(dl.body.toString()) && /whip-e2e/.test(dl.body.toString()), dl.name);
    await rp.close();
    // Earlier reports: link opens a new blob page, "download" saves it.
    await page.click('#repClose');
    await page.click('#aReport');
    await page.waitForSelector('#repBody a[target=_blank]', { timeout: 10000 });
    const [rp2] = await Promise.all([ctx.waitForEvent('page', { timeout: 20000 }), page.click('#repBody a[target=_blank]')]);
    await rp2.waitForURL(/^blob:/, { timeout: 20000 });
    check(`${label}: earlier report link opens`, /whip-e2e/.test(await rp2.textContent('body')));
    await rp2.close();
    const dl2 = await download(page, () => page.click('#repBody a.btn:has-text("download")'));
    check(`${label}: earlier report download`, dl2.name.endsWith('.html') && /whip-e2e/.test(dl2.body.toString()), dl2.name);
    await page.click('#repClose');
  });
  let backupFile = '';
  await step(`${label}: export data`, async () => {
    const b = await download(page, () => page.click('#hostExport'));
    const bk = JSON.parse(b.body.toString());
    const names = bk.files.map((f) => f.name);
    const want = ['settings.json', 'refs/ref-869.json', 'trim/whip-e2e.json', 'snapshots/e2e_snap.json', 'compare.json', 'antennas/whip-e2e.json'];
    check(`${label}: Export data JSON`, bk.format === 'easyvna-web-backup' && want.every((n) => names.includes(n)) && names.some((n) => n.startsWith('reports/')),
      want.filter((n) => !names.includes(n)).join(','));
    backupFile = b.file;
  });
  await step(`${label}: reload persistence`, async () => {
    await page.reload();
    await ready(page, true);
    const s2 = await st(page);
    const snaps = await api(page, 'GET', 'api/snapshots');
    const trims = await api(page, 'GET', 'api/trim');
    const reps = await api(page, 'GET', 'api/reports');
    check(`${label}: data survives a reload (REF, snapshot, trim log, report, compare)`,
      !!s2.refs['869'] && snaps.some((x) => x.name === 'e2e snap') && trims.some((t) => t.name === 'whip-e2e') && reps.length >= 1 && s2.compare.leaderboard.length === 2);
  });
  ctx.report();
  await ctx.close();

  // A fresh browser profile (empty IndexedDB) imports the backup.
  await step(`${label}: import data`, async () => {
    const c2 = await newCtx(browser, url, label + ' import');
    const p2 = await c2.newPage();
    await p2.goto(url);
    await ready(p2, true);
    const before = await st(p2);
    await p2.setInputFiles('#hostImportFile', backupFile);
    await p2.waitForSelector('#askYes:visible', { timeout: 10000 });
    await Promise.all([p2.waitForEvent('load', { timeout: 20000 }), p2.click('#askYes')]);
    await ready(p2, true);
    const after = await st(p2);
    const snaps = await api(p2, 'GET', 'api/snapshots');
    check(`${label}: Import data into an empty browser, then reload`, !before.refs['869'] && !!after.refs['869'] && snaps.some((x) => x.name === 'e2e snap'));
    // A file that is not a backup is refused with a message.
    const bad = path.join(TMP, 'bad.json');
    fs.writeFileSync(bad, '{"hello":1}');
    await p2.setInputFiles('#hostImportFile', bad);
    await p2.waitForSelector('#askYes:visible', { timeout: 10000 });
    await p2.click('#askYes');
    await p2.waitForFunction(() => /backup/i.test(document.getElementById('toastText').textContent) && !document.getElementById('toast').hidden, null, { timeout: 10000 });
    check(`${label}: a non-backup file is refused`, true);
    c2.report();
    await c2.close();
  });
}

// ---------- Web Serial with a fake port backed by the Go simulator ----------
const FAKE_SERIAL = `(() => {
  const log = [];
  let plugged = true, granted = false;
  try { granted = sessionStorage.getItem('fakeGranted') === '1'; } catch (e) { return; } // not the app page
  class FakePort extends EventTarget {
    constructor() { super(); this.readable = null; this.writable = null; this.sh = null; }
    getInfo() { return { usbVendorId: 0x0483, usbProductId: 0x5740 }; }
    async open(o) {
      await Promise.resolve();
      if (!plugged) throw new DOMException('Failed to open serial port.', 'NetworkError');
      if (this.readable) throw new DOMException('The port is already open.', 'InvalidStateError');
      log.push('open ' + o.baudRate);
      const sh = this.sh = window.EasyVNAHost.goMockShell('869');
      this.readable = new ReadableStream({ async pull(c) { const b = await sh.read(); if (b) c.enqueue(b); else c.close(); } });
      this.writable = new WritableStream({ write(ch) { log.push('tx ' + ch.length); sh.write(ch); } });
    }
    async close() { log.push('close'); if (this.sh) this.sh.close(); this.sh = null; this.readable = this.writable = null; }
  }
  const port = new FakePort();
  const serial = new EventTarget();
  serial.getPorts = async () => (granted && plugged ? [port] : []);
  serial.requestPort = async (opts) => {
    log.push('requestPort active=' + navigator.userActivation.isActive + ' filters=' + JSON.stringify(opts.filters));
    if (!plugged) throw new DOMException('No port selected by the user.', 'NotFoundError');
    granted = true; try { sessionStorage.setItem('fakeGranted', '1'); } catch (e) { /* ignore */ }
    return port;
  };
  const fire = (type) => { const e = new Event(type); Object.defineProperty(e, 'target', { value: port }); serial.dispatchEvent(e); };
  window.__fake = { log, port,
    unplug() { plugged = false; if (port.sh) port.sh.close(); fire('disconnect'); },
    plug() { plugged = true; fire('connect'); } };
  Object.defineProperty(Navigator.prototype, 'serial', { get() { return serial; }, configurable: true });
})();`;

async function serialFlow(browser, base, label, shots) {
  const ctx = await newCtx(browser, base, label);
  await ctx.addInitScript(FAKE_SERIAL);
  const page = await ctx.newPage();
  await page.goto(base);
  await ready(page, false);
  await page.waitForFunction(() => S.state.error, null, { timeout: 15000 });
  let s = await st(page);
  check(`${label}: starts unconnected, asks to press Connect`, !s.connected && /press Connect/.test(s.error) && (await page.textContent('#btnConnect')) === 'Connect', s.error);
  await step(`${label}: Connect`, async () => {
    await page.click('#btnConnect');
    await page.waitForFunction(() => S.state.connected, null, { timeout: 20000 });
    s = await st(page);
    const log = await page.evaluate(() => __fake.log.slice());
    check(`${label}: requestPort called during the click, NanoVNA filter`, log.some((l) => /requestPort active=true filters=\[\{"usbVendorId":1155,"usbProductId":22336\}\]/.test(l)), log[0]);
    check(`${label}: port opened at 115200 and driven by the shell driver`, log.includes('open 115200') && log.some((l) => /^tx /.test(l)));
    check(`${label}: device identified through Web Serial`, s.info.port === 'USB 0483:5740 (Web Serial)' && !s.info.mock && /NanoVNA-H 4/.test(s.info.model), s.info.port);
  });
  await step(`${label}: sweep over Web Serial`, async () => {
    await sweepDone(page, () => page.locator('.profile', { hasText: '869' }).first().click());
    const n = await page.evaluate(() => S.res.freq.length);
    check(`${label}: 869 sweep over Web Serial (401 points)`, n === 401, String(n));
    if (shots) await shot(page, 'html-usb-connected.png', false);
  });
  await step(`${label}: unplug`, async () => {
    await page.evaluate(() => __fake.unplug());
    await page.waitForFunction(() => !S.state.connected, null, { timeout: 15000 });
    await page.waitForFunction(() => /disconnected/i.test(S.state.error || ''), null, { timeout: 15000 });
    check(`${label}: unplug shows "disconnected"`, true);
  });
  await step(`${label}: replug`, async () => {
    await page.evaluate(() => __fake.plug());
    await page.waitForFunction(() => S.state.connected, null, { timeout: 20000 });
    check(`${label}: replug reconnects by itself`, true);
  });
  await step(`${label}: reload`, async () => {
    await page.reload();
    await ready(page, false);
    await page.waitForFunction(() => S.state.connected, null, { timeout: 20000 });
    const log = await page.evaluate(() => __fake.log.slice());
    check(`${label}: after reload the granted port reconnects without a click`, !log.some((l) => l.startsWith('requestPort')));
  });
  ctx.report();
  await ctx.close();
}

async function noSerialFlow(browser, base, label, shots) {
  const ctx = await newCtx(browser, base, label);
  await ctx.addInitScript(() => { delete Navigator.prototype.serial; });
  const page = await ctx.newPage();
  await page.goto(base);
  await ready(page, false);
  const n = (await page.isVisible('#hostNotice')) ? await page.textContent('#hostNotice') : '';
  check(`${label}: notice "USB needs Chrome or Edge…"`, /USB needs Chrome or Edge on a computer; the simulator works everywhere/.test(n), n);
  if (shots) await shot(page, 'html-no-webserial.png', false);
  await step(`${label}: notice links to the simulator`, async () => {
    ctx.allow(base + '?demo');
    await Promise.all([page.waitForURL(/\?demo$/), page.click('#hostNotice a')]);
    await ready(page, true);
    check(`${label}: "Try the simulator" runs the simulator`, (await st(page)).info.mock);
  });
  ctx.report();
  await ctx.close();
}

async function offlineFlow(browser, base, label) {
  const ctx = await newCtx(browser, base, label);
  const page = await ctx.newPage();
  await page.goto(base);
  await ready(page, false);
  let file = '';
  await step(`${label}: download`, async () => {
    const d = await download(page, () => page.click('#hostOffline'));
    file = d.file;
    check(`${label}: offline copy is named easyvna-web.html and byte-identical`, d.name === 'easyvna-web.html' && sha(d.body) === PAGE_SHA, sha(d.body));
  });
  ctx.report();
  await ctx.close();
  if (!file) return;
  const local = 'file://' + file;
  const c2 = await newCtx(browser, local + '?demo', label + ' (downloaded copy)');
  const p2 = await c2.newPage();
  await p2.goto(local + '?demo');
  await ready(p2, true);
  await sweepDone(p2, () => p2.click('#aSweep'));
  check(`${label}: downloaded copy runs the simulator from file://`, (await p2.evaluate(() => S.res.freq.length)) > 100 && (await p2.$('#hostOffline')) === null);
  c2.report();
  await c2.close();
}

async function phone(browser, base) {
  if (!SHOTS) return;
  const ctx = await newCtx(browser, base + '?demo', 'phone', { viewport: { width: 390, height: 844 }, deviceScaleFactor: 2 });
  const page = await ctx.newPage();
  await page.goto(base + '?demo');
  await ready(page, true);
  await sweepDone(page, () => page.locator('.profile', { hasText: '869' }).first().click());
  await shot(page, 'html-phone.png', false);
  ctx.report();
  await ctx.close();
}

(async () => {
  if (SHOTS) fs.mkdirSync(SHOTS, { recursive: true });
  const browser = await chromium.launch({ headless: true });
  console.log(`page: ${HTML} (${fs.statSync(HTML).size} bytes), Chromium ${browser.version()}`);
  const srv = await serve();
  const httpBase = `http://127.0.0.1:${srv.address().port}/easyvna-web/`;
  const fileBase = 'file://' + HTML;
  try {
    // A fresh copy for file:// keeps its IndexedDB origin apart from other runs.
    for (const [base, label] of [[fileBase, 'file'], [httpBase, 'http']]) {
      console.log(`\n== simulator over ${label}`);
      await demoFlow(browser, base, label, label === 'http');
      console.log(`\n== Web Serial (fake port) over ${label}`);
      await serialFlow(browser, base, label + ' usb', label === 'http');
      console.log(`\n== no Web Serial over ${label}`);
      await noSerialFlow(browser, base, label + ' no-serial', label === 'http');
    }
    console.log('\n== offline download (http)');
    await offlineFlow(browser, httpBase, 'offline');
    await phone(browser, httpBase);
  } finally {
    await browser.close();
    srv.close();
  }
  console.log(`\n${passed} checks passed, ${failures.length} failed`);
  if (failures.length) { for (const f of failures) console.log('  - ' + f); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
