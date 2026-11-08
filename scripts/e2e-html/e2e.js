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
const ONLY = process.env.E2E_ONLY || ''; // "flasher": only the FIRMWARE dialog checks
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'easyvna-e2e-'));
const sha = (b) => crypto.createHash('sha256').update(b).digest('hex');
const PAGE_SHA = sha(fs.readFileSync(HTML));
// A small PNG "photo" for the antenna sheet.
const PHOTO = path.join(TMP, 'antenna.png');
fs.writeFileSync(PHOTO, Buffer.from('iVBORw0KGgoAAAANSUhEUgAAADAAAAAgCAIAAADbtmxLAAAAWElEQVR42mN8+OIDw2ACTAyDDAw6B7FQboS7gwWcvfPAidEoG3XQqINGHTTqoFEHjTpo1EGjDhp10KiDRh00qB3EiL/nalu0gRa2Hu4LGI2ykZKGRkOIAQC8qw5rNf7LwwAAAABJRU5ErkJggg==', 'base64'));
// Hand-written measurement files (the Go parser tests use them too).
const FIXTURES = path.resolve(__dirname, '..', '..', 'internal', 'rf', 'testdata');
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
// opts.allowPrefix also allows every URL under that prefix (the native
// program's own server).
async function newCtx(browser, pageURL, label, opts = {}) {
  const { allowPrefix, ...ctxOpts } = opts;
  const ctx = await browser.newContext({ acceptDownloads: true, viewport: { width: 1360, height: 900 }, colorScheme: 'light', ...ctxOpts });
  // print() would open a dialog: count the calls instead.
  await ctx.addInitScript(() => { window.print = () => { window.__printed = (window.__printed || 0) + 1; }; });
  const errors = [], extra = [];
  const own = new Set([strip(pageURL)]);
  ctx.allow = (u) => own.add(strip(u));
  const ok = (u) => own.has(strip(u)) || (!!allowPrefix && u.startsWith(allowPrefix));
  await ctx.route('**/*', (route) => {
    const u = route.request().url();
    if (ok(u)) return route.continue();
    extra.push(u);
    return route.abort();
  });
  ctx.on('request', (r) => {
    const u = r.url();
    if (u.startsWith('blob:') || u.startsWith('data:') || ok(u)) return;
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
    const eff = (await page.isVisible('#verdict .effect')) ? await page.textContent('#verdict .effect') : '';
    check(`${label}: per-band effect vs REF in the main view`, /Per band vs REF/.test(eff) && /869: \+\d+\.\d MHz/.test(eff) && /SWR \d\.\d→\d\.\d/.test(eff), eff);
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
    await page.waitForFunction(() => /FIRST CUT|TRIM < 0\.5/.test(document.getElementById('cutBig').textContent), null, { timeout: 10000 });
    const first = await page.evaluate(() => ({ big: document.getElementById('cutBig').textContent, note: document.getElementById('cutNote').textContent,
      pre: Number(document.getElementById('trimCut').value), p: S.trim.prediction, dir: document.getElementById('tDir').textContent }));
    check(`${label}: first cut is cautious (60 %, 0.5 mm steps) and explained`, first.p.cautious && first.pre >= 0 && first.pre % 0.5 === 0 && first.pre < first.p.cutMm &&
      /full estimate/.test(first.note) && /cannot lengthen a cut wire/.test(first.note) && /first cut/.test(first.dir), JSON.stringify(first));
    if (shots) { await page.evaluate(() => { document.getElementById('toast').hidden = true; }); await page.screenshot({ path: path.join(SHOTS, 'html-tune-first-cut.png') }); console.log('  saved ' + path.join(SHOTS, 'html-tune-first-cut.png')); }
    await page.fill('#trimCut', '1');
    await page.click('#trimLog');
    await page.waitForFunction(() => S.trim && S.trim.entries.length === 2, null, { timeout: 20000 });
    check(`${label}: after a logged cut MHz/mm is learnt (no longer cautious)`, await page.evaluate(() => !S.trim.prediction.cautious && !/FIRST CUT/.test(document.getElementById('cutBig').textContent)));
    await page.waitForFunction(() => /MHz/.test(document.getElementById('tRes').textContent), null, { timeout: 10000 });
    await page.waitForFunction(() => /869:/.test(document.getElementById('tEffect').textContent), null, { timeout: 10000 });
    check(`${label}: per-band effect vs REF in TUNE`, true);
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
  let snapS1p = '';
  await step(`${label}: snapshots and exports`, async () => {
    await sweepDone(page, () => page.click('#aSweep'));
    await page.click('#snapsPanel summary');
    await page.fill('#snapName', 'e2e snap');
    await page.click('#snapForm button[type=submit]');
    await page.waitForSelector('#snapList td:has-text("e2e snap")', { timeout: 10000 });
    const s1p = await download(page, () => page.click('#snapList a:has-text(".s1p")'));
    check(`${label}: snapshot .s1p download`, s1p.name === 'e2e_snap.s1p' && /^!/.test(s1p.body.toString()) && /# Hz S RI R 50/i.test(s1p.body.toString()), s1p.name);
    snapS1p = s1p.file;
    const csv = await download(page, () => page.click('.exports a:has-text("CSV")'));
    check(`${label}: last sweep CSV download`, csv.name.endsWith('.csv') && csv.body.toString().split('\n').length > 100, csv.name);
  });
  await step(`${label}: open measurement files`, async () => {
    // The .s1p just exported, opened again as a file measurement.
    const named = path.join(TMP, 'whip 869.s1p');
    fs.copyFileSync(snapS1p, named);
    await page.setInputFiles('#fileInput', named);
    await page.waitForFunction(() => S.res && S.res.source && S.res.source.file === 'whip 869.s1p', null, { timeout: 15000 });
    const bar = await page.textContent('#filebar');
    const s2 = await st(page);
    check(`${label}: .s1p opens as a labelled file measurement (not live) with a verdict`, (await page.isVisible('#filebar')) && /NOT LIVE/.test(bar) && /whip 869\.s1p/.test(bar) &&
      /869 MHz band/.test(await page.textContent('#verdict')) && /File whip 869\.s1p/.test(await page.textContent('#legend')) && !s2.continuous, bar);
    check(`${label}: report dialog offers the file`, s2.reportChoices.some((c) => c.file && /whip 869/.test(c.label)));
    const ex = await download(page, () => page.click('.exports a:has-text(".s1p")'));
    check(`${label}: export of the file measurement`, ex.name === 'whip_869-easyvna.s1p' && /Opened from file whip 869\.s1p/.test(ex.body.toString()), ex.name);
    if (shots) { await page.evaluate(() => scrollTo(0, 0)); await shot(page, 'html-file-open.png', false); }
    await page.setInputFiles('#fileInput', path.join(FIXTURES, 'nanovna-saver.csv'));
    await page.waitForFunction(() => S.res && S.res.source && S.res.source.file === 'nanovna-saver.csv', null, { timeout: 15000 });
    check(`${label}: CSV with S21 opens`, await page.evaluate(() => S.res.freq.length === 4 && S.res.s21.length === 4 && !document.getElementById('expS2p').hidden));
    // A jittery measurement (bad connector) is flagged in plain words.
    const noisy = path.join(TMP, 'bad-connector.s1p');
    let rows = '# MHz S RI R 50\n', seed = 7;
    const rnd = () => { seed = (seed * 16807) % 2147483647; return seed / 2147483647 - 0.5; };
    for (let i = 0; i < 401; i++) {
      const f = 779 + 180 * i / 400, x = 45 * 14 * (f / 868 - 868 / f), re = 45, d = (re + 50) ** 2 + x * x;
      rows += `${f.toFixed(4)} ${(((re * re + x * x - 2500) / d) + 0.03 * rnd()).toFixed(6)} ${((100 * x / d) + 0.03 * rnd()).toFixed(6)}\n`;
    }
    fs.writeFileSync(noisy, rows);
    await page.setInputFiles('#fileInput', noisy);
    await page.waitForFunction(() => S.res && S.res.source && S.res.source.file === 'bad-connector.s1p', null, { timeout: 15000 });
    const wtxt = await page.textContent('#warnings');
    check(`${label}: noisy measurement warning ("check the connector and cable, then recalibrate"), verdict CHECK`,
      /Noisy measurement/.test(wtxt) && /check the connector and cable, then recalibrate/.test(wtxt) && /CHECK/.test(await page.textContent('#verdict')), wtxt);
    if (shots) { await page.evaluate(() => scrollTo(0, 0)); await shot(page, 'html-noisy-warning.png', false); }
    await sweepDone(page, () => page.click('#fileLive'));
    check(`${label}: clean simulator sweep has no noise or calibration warning`, await page.evaluate(() => !S.res.summary.warnings.some((w) => /noisy|calmismatch/.test(w.code))));
    check(`${label}: "Back to live" sweeps and hides the file bar`, await page.evaluate(() => !S.res.source && document.getElementById('filebar').hidden));
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
  await step(`${label}: antenna history and drift`, async () => {
    await api(page, 'POST', 'api/mock/antenna', { name: '869' });
    await sweepDone(page, () => page.locator('.profile', { hasText: '869' }).first().click());
    await page.click('#aHist');
    await page.waitForSelector('#histLog', { timeout: 10000 });
    check(`${label}: history opens on the sheet's antenna, with the sheet and report entries`, (await page.inputValue('#histName')) === 'whip-e2e' &&
      (await page.evaluate(() => S.hist && S.hist.history.some((e) => e.kind === 'sheet') && S.hist.history.some((e) => e.kind === 'report'))));
    // A new antenna name: log the slightly long whip, then the tuned one.
    await page.fill('#histName', 'drift-e2e');
    await page.dispatchEvent('#histName', 'change');
    await page.waitForFunction(() => S.hist && S.hist.name === 'drift-e2e', null, { timeout: 10000 });
    await page.click('#histLog');
    await page.waitForFunction(() => S.hist.history.length === 1, null, { timeout: 10000 });
    const n1 = 1;
    await page.click('#histClose');
    await api(page, 'POST', 'api/mock/antenna', { name: '869tuned' });
    await sweepDone(page, () => page.click('#aSweep'));
    await page.click('#aHist');
    await page.waitForSelector('#histLog', { timeout: 10000 });
    await page.click('#histLog');
    await page.waitForFunction((n) => S.hist.history.length === n + 1, n1, { timeout: 10000 });
    await page.waitForSelector('#histInfo .wbanner', { timeout: 10000 });
    const txt = await page.textContent('#histInfo');
    const drawn = await page.evaluate(() => ['histRes', 'histSWR'].every((id) => { const c = document.getElementById(id); return c && c.width > 50; }));
    check(`${label}: logged twice, the detuned antenna shows "Resonance moved", sparklines drawn`, /Resonance moved/.test(txt) && /Previous measurement/.test(txt) && drawn, txt.slice(0, 200));
    if (shots) await shot(page, 'html-history.png', false);
    const csv = await download(page, () => page.click('#histInfo a:has-text("Export history")'));
    check(`${label}: history CSV`, csv.name === 'drift-e2e-history.csv' && csv.body.toString().split('\n').length >= 3, csv.name);
    await page.click('#histClose');
    check(`${label}: HISTORY tile flags the drift`, /⚠/.test(await page.textContent('#aHist')));
  });
  let backupFile = '';
  await step(`${label}: export data`, async () => {
    const b = await download(page, () => page.click('#hostExport'));
    const bk = JSON.parse(b.body.toString());
    const names = bk.files.map((f) => f.name);
    const want = ['settings.json', 'refs/ref-869.json', 'trim/whip-e2e.json', 'snapshots/e2e_snap.json', 'compare.json', 'antennas/whip-e2e.json'];
    check(`${label}: Export data JSON`, bk.format === 'easyvna-web-backup' && want.every((n) => names.includes(n)) && names.some((n) => n.startsWith('reports/')),
      want.filter((n) => !names.includes(n)).join(','));
    const ant = bk.files.find((f) => f.name === 'antennas/whip-e2e.json');
    check(`${label}: Export data includes the antenna history`, !!ant && JSON.parse(ant.data).history.length >= 3);
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
      this.writable = new WritableStream({ write(ch) { log.push('tx ' + ch.length); if (ch.length < 64) log.push('cmd ' + new TextDecoder().decode(ch).trim()); sh.write(ch); } });
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
    check(`${label}: requestPort called during the click, NanoVNA-H and V2 filters`, log.some((l) => /requestPort active=true filters=\[\{"usbVendorId":1155,"usbProductId":22336\},\{"usbVendorId":1204,"usbProductId":8\}\]/.test(l)), log[0]);
    check(`${label}: port opened at 115200 and driven by the shell driver`, log.includes('open 115200') && log.some((l) => /^tx /.test(l)));
    check(`${label}: device identified through Web Serial`, s.info.port === 'USB 0483:5740 (Web Serial)' && !s.info.mock && /NanoVNA-H 4/.test(s.info.model), s.info.port);
  });
  await step(`${label}: sweep over Web Serial`, async () => {
    await sweepDone(page, () => page.locator('.profile', { hasText: '869' }).first().click());
    const n = await page.evaluate(() => S.res.freq.length);
    check(`${label}: 869 sweep over Web Serial (401 points)`, n === 401, String(n));
    if (shots) await shot(page, 'html-usb-connected.png', false);
  });
  await step(`${label}: Disconnect gives the NanoVNA back`, async () => {
    check(`${label}: connected: the button reads Disconnect`, (await page.textContent('#btnConnect')) === 'Disconnect');
    const n0 = await page.evaluate(() => __fake.log.length);
    await page.click('#btnConnect');
    await page.waitForFunction(() => !S.state.connected, null, { timeout: 15000 });
    await page.waitForTimeout(1500); // a sweep or reconnect would show up here
    const log = await page.evaluate((n) => __fake.log.slice(n), n0);
    s = await st(page);
    check(`${label}: Disconnect sends resume, closes the port, stays closed`, log.includes('cmd resume') && log.includes('close') &&
      log.indexOf('cmd resume') < log.indexOf('close') && !log.slice(log.indexOf('close')).some((l) => /^open|^tx/.test(l)) && !s.connected, log.join(' | '));
    check(`${label}: after Disconnect the button reads Connect, the banner says how to resume`,
      (await page.textContent('#btnConnect')) === 'Connect' && /Press Connect/.test(s.error), s.error);
    await page.click('#btnConnect');
    await page.waitForFunction(() => S.state.connected, null, { timeout: 20000 });
    check(`${label}: Connect after Disconnect reconnects`, (await page.textContent('#btnConnect')) === 'Disconnect');
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

// NanoVNA V2 (EXPERIMENTAL driver): the simulator with host calibration,
// and Web Serial with a fake 04b4:0008 port backed by the Go V2 simulator.
async function v2Flow(browser, base, label, shots) {
  const url = base + '?demo&firmware=v2';
  const ctx = await newCtx(browser, url, label);
  const page = await ctx.newPage();
  await page.goto(url);
  await ready(page, true);
  let s = await st(page);
  check(`${label}: V2 simulator connects, marked EXPERIMENTAL, raw data`, s.info.driver === 'nanovna-v2' && s.info.hostCal && /EXPERIMENTAL/.test(await page.textContent('#devtext')) && !s.cal.status.applied,
    JSON.stringify(s.info));
  await step(`${label}: host calibration`, async () => {
    await sweepDone(page, () => page.locator('.profile', { hasText: '869' }).first().click());
    const before = await page.evaluate(() => S.res.summary.minSWR);
    for (const std of ['open', 'short', 'load', 'done']) {
      await page.click(`.calstep[data-step=${std}]`);
      if (await page.isVisible('#askYes')) await page.click('#askYes');
      await page.waitForFunction((x) => S.state.cal.flow.steps[x] === 'done', std, { timeout: 20000 });
    }
    await page.click('.calstep[data-step=save]');
    await page.waitForSelector('#askYes:visible');
    const askText = await page.textContent('#askText');
    await page.click('#askYes');
    await page.waitForFunction(() => S.state.cal.flow.steps.save === 'done', null, { timeout: 10000 });
    await sweepDone(page, () => page.click('#aSweep'));
    const after = await page.evaluate(() => ({ applied: S.res.calApplied, min: S.res.summary.minSWR, f: S.res.summary.minSWRFreq, w: S.res.summary.warnings.map((x) => x.code) }));
    check(`${label}: OPEN/SHORT/LOAD/DONE/SAVE calibrate the raw V2 data on the host`, after.applied && after.f > 845e6 && after.f < 870e6 && after.min < 1.6 && !after.w.length &&
      /device itself stores none/.test(askText) && /calibrates it/.test(await page.textContent('#calsummary')), JSON.stringify({ before, after }));
    if (shots) { await page.evaluate(() => scrollTo(0, 0)); await shot(page, 'html-v2-demo.png', false); }
    await page.reload();
    await ready(page, true);
    await sweepDone(page, () => page.locator('.profile', { hasText: '869' }).first().click());
    check(`${label}: the saved host calibration comes back after a reload`, await page.evaluate(() => S.res.calApplied && S.state.cal.status.applied));
  });
  ctx.report();
  await ctx.close();

  const c2 = await newCtx(browser, base, label + ' usb');
  await c2.addInitScript(FAKE_SERIAL.replace('usbVendorId: 0x0483, usbProductId: 0x5740', 'usbVendorId: 0x04b4, usbProductId: 0x0008').replace("goMockShell('869')", "goMockShell('869', 'v2')"));
  const p2 = await c2.newPage();
  await p2.goto(base);
  await ready(p2, false);
  await step(`${label}: V2 over Web Serial`, async () => {
    await p2.click('#btnConnect');
    await p2.waitForFunction(() => S.state.connected, null, { timeout: 20000 });
    s = await st(p2);
    await sweepDone(p2, () => p2.locator('.profile', { hasText: '869' }).first().click());
    const n = await p2.evaluate(() => S.res.freq.length);
    check(`${label}: a 04b4:0008 port opens with the V2 driver and sweeps (401 points)`, s.info.driver === 'nanovna-v2' && s.info.port === 'USB 04b4:0008 (Web Serial)' && n === 401, `${s.info.driver} ${s.info.port} ${n}`);
  });
  c2.report();
  await c2.close();
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
  await step(`${label}: open a measurement file without Web Serial`, async () => {
    const [fc] = await Promise.all([page.waitForEvent('filechooser', { timeout: 10000 }), page.click('#hostOpenFile')]);
    await fc.setFiles(path.join(FIXTURES, 'whip-869.s1p'));
    await page.waitForFunction(() => S.res && S.res.source && S.res.source.file === 'whip-869.s1p', null, { timeout: 15000 });
    check(`${label}: file opens with no NanoVNA: verdict, plots, file bar`, (await page.isVisible('#filebar')) && /869 MHz band/.test(await page.textContent('#verdict')) &&
      (await page.evaluate(() => !S.state.connected && S.res.summary.bands.length > 0)));
    if (shots) await shot(page, 'html-no-webserial-file.png', true);
  });
  await step(`${label}: notice links to the simulator`, async () => {
    ctx.allow(base + '?demo');
    await Promise.all([page.waitForURL(/\?demo$/), page.click('#hostNotice a')]);
    await ready(page, true);
    check(`${label}: "Try the simulator" runs the simulator`, (await st(page)).info.mock);
  });
  ctx.report();
  await ctx.close();
}

// ---------- FIRMWARE dialog (flasher.js) with a simulated DFU device ----------
// navigator.usb is replaced by scripts/flasher/dfuse-sim.js (an STM32
// bootloader in DFU mode with 256 KB or 128 KB of flash, a fake firmware and
// a recognisable calibration area). No real USB device is ever opened.
const SIM = require('../flasher/dfuse-sim.js');
const SIM_SRC = fs.readFileSync(path.join(__dirname, '..', 'flasher', 'dfuse-sim.js'), 'utf8') + '\n;DfuSeSim.installFakeUSB(window);';
const FW_DIR = path.join(__dirname, '..', '..', 'web', 'static', 'firmware');
const FW_MANIFEST = JSON.parse(fs.readFileSync(path.join(FW_DIR, 'manifest.json'), 'utf8'));
const FW_BIN = fs.readFileSync(path.join(FW_DIR, FW_MANIFEST.file));
function intelHex(data, base) {
  const out = [];
  const rec = (type, addr, bytes) => {
    const b = [bytes.length, (addr >> 8) & 0xff, addr & 0xff, type, ...bytes];
    b.push((0x100 - (b.reduce((x, v) => x + v, 0) & 0xff)) & 0xff);
    out.push(':' + b.map((v) => v.toString(16).toUpperCase().padStart(2, '0')).join(''));
  };
  let upper = -1;
  for (let o = 0; o < data.length; o += 16) {
    const a = base + o;
    if (a >>> 16 !== upper) { upper = a >>> 16; rec(4, 0, [upper >> 8, upper & 0xff]); }
    rec(0, a & 0xffff, Array.from(data.subarray(o, o + 16)));
  }
  rec(1, 0, []);
  return out.join('\n') + '\n';
}
// simSha returns the SHA-256 of the current simulated device's flash [a, b).
const simSha = (page, a = 0, b) => page.evaluate(async ([a, b]) => {
  const f = window.__d.flash.slice(a, b === null ? undefined : b);
  return Array.from(new Uint8Array(await crypto.subtle.digest('SHA-256', f)), (v) => v.toString(16).padStart(2, '0')).join('');
}, [a, b === undefined ? null : b]);
const sim = (page, expr) => page.evaluate(expr);
async function plug(page, opts) {
  await page.evaluate((o) => { window.__d = __usbSim.plug(o); }, opts);
}
async function fwConnect(page) {
  await page.click('#fwConnect');
  await page.waitForSelector('#fwTarget', { timeout: 10000 });
}
async function fwFlash(page, confirmCheck) {
  await page.click('#fwFlash');
  await page.waitForSelector('#fwAsk', { timeout: 5000 });
  const text = await page.textContent('#fwAsk');
  if (confirmCheck) await page.check('#fwAskCheck');
  await page.click('#fwAskYes');
  return text;
}
// fwShot photographs the FIRMWARE dialog: the whole of it when full (the
// viewport grows to its height), else what is in view.
async function fwShot(page, name, full, sel) {
  if (!SHOTS) return;
  if (sel) { // just that element
    await page.evaluate(() => { const t = document.getElementById('toast'); if (t) t.hidden = true; });
    await page.waitForTimeout(200);
    await page.locator(sel).screenshot({ path: path.join(SHOTS, name) });
    console.log('  saved ' + path.join(SHOTS, name));
    return;
  }
  const vp = page.viewportSize();
  if (full) {
    const h = await page.evaluate(() => { const v = document.getElementById('fwView'); v.scrollTop = 0; return v.scrollHeight; });
    await page.setViewportSize({ width: vp.width, height: Math.max(vp.height, h) });
  }
  await shot(page, name, false);
  if (full) await page.setViewportSize(vp);
}
const fwIdle = (page) => page.waitForFunction(() => !EasyVNAFlasherUI.state.busy, null, { timeout: 60000 });

async function flasherFlow(browser, base, label, shots, ctxOpts = {}) {
  const ctx = await newCtx(browser, base, label, ctxOpts);
  await ctx.addInitScript({ content: SIM_SRC });
  const page = await ctx.newPage();
  await page.goto(base);
  await ready(page, false);
  const H4_FW_END = 0x23800;

  await step(`${label}: open the dialog`, async () => {
    await page.click('#btnFirmware');
    await page.waitForSelector('#fwView:not([hidden])');
    await page.waitForFunction(() => EasyVNAFlasherUI.state.fw || EasyVNAFlasherUI.state.fwErr, null, { timeout: 10000 });
    const t = await page.textContent('#fwBody');
    const fwErr = await page.evaluate(() => EasyVNAFlasherUI.state.fwErr);
    check(`${label}: bundled EasyVNA ${FW_MANIFEST.version} loaded, SHA-256 checked`, !fwErr && t.includes(`EasyVNA ${FW_MANIFEST.version} (bundled)`), fwErr);
    const lic = await page.getAttribute('.fwlic a', 'href');
    check(`${label}: GPL-3.0 notice links to the source tag`, /GPL-3\.0/.test(await page.textContent('.fwlic')) && lic === FW_MANIFEST.source, lic);
    check(`${label}: help: DFU entry, serial connection`, (await page.getAttribute('#fwHelp', 'open')) !== null &&
      /hold the jog wheel pushed in and switch it on/.test(t) && /reset dfu/.test(t) && /USB drivers and permissions/.test(t));
    // USB drivers: one tab per OS; Linux = udev rule (both modes), Windows = Zadig/WinUSB, macOS = nothing.
    await page.click('#fwDrivers > summary');
    const tab = async (os) => { await page.click(`.fwtab[data-os="${os}"]`); await page.waitForSelector(`#fwDriversBody[data-os="${os}"]`); return page.textContent('#fwDriversBody'); };
    const lin = await tab('linux');
    check(`${label}: drivers: Linux udev rule for 0483:5740 and 0483:df11, uaccess, reload`, (await page.getAttribute('#fwDrivers', 'open')) !== null &&
      /idProduct}=="5740".*TAG\+="uaccess"/s.test(lin) && /idProduct}=="df11", TAG\+="uaccess"/.test(lin) && /70-nanovna\.rules/.test(lin) &&
      /udevadm control --reload-rules/.test(lin) && /dialout/.test(lin) && /uucp/.test(lin));
    if (shots) await fwShot(page, 'flasher-drivers-linux.png', false, '#fwDrivers');
    const win = await tab('windows');
    check(`${label}: drivers: Windows Zadig, WinUSB, check 0483 DF11, way back`, /Zadig/.test(win) && /WinUSB/.test(win) && /0483 DF11/.test(win) && /Uninstall device/.test(win));
    if (shots) await fwShot(page, 'flasher-drivers-windows.png', false, '#fwDrivers');
    const mac = await tab('macos');
    check(`${label}: drivers: macOS nothing to install`, /Nothing to install/.test(mac));
    if (shots) await fwShot(page, 'flasher-drivers-macos.png', false, '#fwDrivers');
    await page.click('#fwDrivers > summary');
    if (shots) await fwShot(page, 'flasher-idle.png', true);
  });
  await step(`${label}: Connect without a device`, async () => {
    await page.click('#fwConnect');
    await page.waitForSelector('#fwError');
    check(`${label}: no device chosen: says how to enter DFU`, /No device chosen.*STM32 BOOTLOADER/.test(await page.textContent('#fwError')));
  });
  let dump = null, origSha = '';
  await step(`${label}: connect an H4 in DFU mode`, async () => {
    await plug(page, { variant: 'f303', pollErase: 2 });
    origSha = await simSha(page);
    await fwConnect(page);
    const log = await page.evaluate(() => __usbSim.log.slice());
    check(`${label}: requestDevice during the click, filter 0483:df11`, log.some((l) => l === 'requestDevice active=true filters=[{"vendorId":1155,"productId":57105}]'), log.join(' | '));
    check(`${label}: layout read: NanoVNA-H4, STM32F303, 256 KB`, (await page.textContent('#fwTarget')) === 'NanoVNA-H4 · STM32F303 · 256 KB' &&
      /128 × 2 KB = 256 KB · transfers of 2048 bytes/.test(await page.textContent('#fwLayout')));
    const checks = await page.$$eval('#fwChecks li', (li) => li.map((x) => x.className));
    check(`${label}: the bundled image passes every check on the H4`, checks.length >= 5 && checks.every((c) => c === 'ok'), checks.join());
    check(`${label}: plan: 60 pages`, /erase 60 pages \(120 KB\)/.test(await page.textContent('#fwPlan')));
    if (shots) await fwShot(page, 'flasher-connected.png', true);
    if (shots) {
      const vp = page.viewportSize();
      await page.setViewportSize({ width: 390, height: 844 });
      await fwShot(page, 'flasher-phone.png', true);
      await page.setViewportSize(vp);
    }
  });
  await step(`${label}: back up`, async () => {
    dump = await download(page, () => page.click('#fwBackup'));
    await fwIdle(page);
    check(`${label}: backup file name`, /^nanovna-backup-f303-256k-\d{4}-\d\d-\d\d-\d{6}\.bin$/.test(dump.name), dump.name);
    check(`${label}: backup == simulated flash (256 KB)`, dump.body.length === 262144 && sha(dump.body) === origSha, `${dump.body.length} ${sha(dump.body)}`);
    check(`${label}: backup kept in the browser`, (await page.$$('.fwbackups tr')).length === 1 && /backed up in this session/.test(await page.textContent('#fwBackupState')));
    const again = await download(page, () => page.click('.fwbackups [data-dl]'));
    check(`${label}: stored backup downloads again, same bytes`, again.name === dump.name && sha(again.body) === origSha);
  });
  await step(`${label}: flash the bundled EasyVNA`, async () => {
    const calBefore = await simSha(page, H4_FW_END);
    await page.evaluate(() => { __d.opts.pollWrite = 25; });
    const dl = [];
    page.on('download', (d) => dl.push(d.suggestedFilename()));
    const ask = await fwFlash(page);
    check(`${label}: confirmation: target, image, version, size, pages, calibration kept, already backed up`,
      /NanoVNA-H4 \(STM32F303, 256 KB\)/.test(ask) && ask.includes(`${FW_MANIFEST.file} · ${FW_MANIFEST.version} · ${FW_MANIFEST.size.toLocaleString('en-US')} bytes`) &&
      /Erase: 60 pages of 2 KB, 0x08000000–0x0801E000/.test(ask) && /Calibration and configuration are kept/.test(ask) && /Backed up in this session/.test(ask), ask);
    if (shots) {
      await page.waitForFunction(() => EasyVNAFlasherUI.state.phase === 'write' && EasyVNAFlasherUI.state.prog && EasyVNAFlasherUI.state.prog.done > 40000, null, { timeout: 20000 });
      await shot(page, 'flasher-progress.png', false);
    }
    await page.waitForSelector('#fwDone', { timeout: 60000 });
    await page.evaluate(() => { __d.opts.pollWrite = 1; });
    const st = await sim(page, () => ({ erased: __d.erased.slice(), mass: __d.massErase, unprot: __d.unprotect, viol: __d.pollViolations }));
    check(`${label}: flash == EasyVNA image`, (await simSha(page, 0, FW_BIN.length)) === FW_MANIFEST.sha256);
    check(`${label}: calibration and config area untouched`, (await simSha(page, H4_FW_END)) === calBefore);
    check(`${label}: only the 60 pages of the image erased, no mass erase`, st.erased.length === 60 && st.erased.every((a, i) => a === 0x08000000 + i * 2048) && st.mass === 0 && st.unprot === 0, JSON.stringify(st).slice(0, 200));
    check(`${label}: bwPollTimeout honoured`, st.viol === 0, String(st.viol));
    check(`${label}: no second backup (already done this session)`, dl.length === 0, dl.join());
    check(`${label}: done: verified, calibration kept`, /Flashed and verified: EasyVNA/.test(await page.textContent('#fwDone')) && /Calibration and configuration were kept/.test(await page.textContent('#fwDone')));
    if (shots) await fwShot(page, 'flasher-done.png', false);
  });
  await step(`${label}: start firmware`, async () => {
    await page.click('#fwStart2');
    await page.waitForSelector('#fwConnect', { timeout: 10000 });
    check(`${label}: Start firmware leaves DFU (set address, zero-length DNLOAD)`, await sim(page, () => __d.left && __d.gone && __d.log.includes('setaddr 0x8000000')));
  });
  await step(`${label}: flash a user .hex (mandatory backup first)`, async () => {
    await plug(page, { variant: 'f303', serial: 'SIM-HEX', pollErase: 1 });
    const before = await simSha(page);
    await fwConnect(page);
    const img = SIM.fakeFirmware(50000, 99);
    await page.setInputFiles('#fwFile', { name: 'my-firmware.hex', mimeType: 'text/plain', buffer: Buffer.from(intelHex(img, 0x08000000)) });
    await page.waitForSelector('#fwChecks');
    check(`${label}: .hex read: name, size, format`, /my-firmware\.hex · 50,000 bytes · hex/.test(await page.textContent('.fwchoices')));
    const [d] = await Promise.all([page.waitForEvent('download', { timeout: 20000 }), fwFlash(page)]);
    const p = path.join(TMP, 'auto-' + d.suggestedFilename());
    await d.saveAs(p);
    await page.waitForSelector('#fwDone', { timeout: 60000 });
    check(`${label}: automatic backup before writing == original flash`, sha(fs.readFileSync(p)) === before && /^nanovna-backup-f303-256k-/.test(d.suggestedFilename()));
    check(`${label}: flash == .hex image`, (await simSha(page, 0, 50000)) === sha(img) && (await sim(page, () => __d.erased.length)) === 25);
  });
  await step(`${label}: refuse an image overlapping the save area`, async () => {
    await page.setInputFiles('#fwFile', { name: 'too-big.bin', mimeType: 'application/octet-stream', buffer: Buffer.from(SIM.fakeFirmware(H4_FW_END + 2048)) });
    await page.waitForSelector('#fwChecks li.bad');
    check(`${label}: overlap refused, Flash disabled`, /inside the calibration and configuration area \(0x08023800/.test(await page.textContent('#fwChecks')) && await page.isDisabled('#fwFlash'));
  });
  await step(`${label}: verify failure`, async () => {
    await page.click('#fwDisconnect');
    await plug(page, { variant: 'f303', serial: 'SIM-BAD', corruptAt: 0x08001234, pollErase: 1 });
    await fwConnect(page);
    await page.check('input[name=fwChoice][value=easyvna]');
    await Promise.all([page.waitForEvent('download', { timeout: 20000 }), fwFlash(page)]);
    await page.waitForSelector('#fwError', { timeout: 60000 });
    const e = await page.textContent('#fwError');
    check(`${label}: verify failure reported with its address`, /Firmware update failed/.test(e) && /Verify failed: 1 byte differ.*0x08001234/.test(e) && /bootloader is in ROM/.test(e), e);
    if (shots) await fwShot(page, 'flasher-error-verify.png', false);
  });
  await step(`${label}: restore a full backup`, async () => {
    await page.click('#fwDisconnect');
    await plug(page, { variant: 'f303', serial: 'SIM-RESTORE', pollErase: 1 });
    await page.evaluate(() => { __d.flash.fill(0x00, 0x23800, 0x24000); __d.flash.fill(0x00, 0, 0x100); });
    await fwConnect(page);
    await page.setInputFiles('#fwBackupFile', dump.file);
    await page.waitForSelector('#fwChecks');
    check(`${label}: full backup accepted for restore`, (await page.textContent('#fwFlash')) === 'Restore…' && /restores the whole flash/.test(await page.textContent('#fwChecks')));
    await page.click('#fwFlash');
    await page.waitForSelector('#fwAsk');
    const ask = await page.textContent('#fwAsk');
    const locked = await page.isDisabled('#fwAskYes');
    if (shots) await fwShot(page, 'flasher-confirm-restore.png', false);
    await page.check('#fwAskCheck');
    await Promise.all([page.waitForEvent('download', { timeout: 20000 }), page.click('#fwAskYes')]);
    await page.waitForSelector('#fwDone', { timeout: 60000 });
    check(`${label}: restore needs the explicit checkbox, says the calibration is replaced`, locked && /calibration and configuration are replaced/.test(ask), ask);
    check(`${label}: restored flash == backup, 128 pages`, (await simSha(page)) === origSha && (await sim(page, () => __d.erased.length)) === 128);
  });
  await step(`${label}: 128 KB NanoVNA-H refuses the H4 image`, async () => {
    await page.click('#fwDisconnect');
    await plug(page, { variant: 'f072' });
    await fwConnect(page);
    await page.check('input[name=fwChoice][value=easyvna]');
    await page.waitForSelector('#fwChecks li.bad');
    check(`${label}: F072 detected, H4 image refused`, (await page.textContent('#fwTarget')) === 'NanoVNA-H · STM32F072 · 128 KB' &&
      /for the NanoVNA-H4 \(STM32F303, 256 KB\); this device has 128 KB/.test(await page.textContent('#fwChecks')) && await page.isDisabled('#fwFlash'));
    if (shots) await fwShot(page, 'flasher-f072-refused.png', false);
  });
  await step(`${label}: read-protected device`, async () => {
    await page.click('#fwDisconnect');
    await plug(page, { variant: 'f303', serial: 'SIM-RDP', protected: true });
    await fwConnect(page);
    await fwFlash(page);
    await page.waitForSelector('#fwError', { timeout: 20000 });
    const e = await page.textContent('#fwError');
    const st = await sim(page, () => ({ w: __d.writes, e: __d.erased.length, u: __d.unprotect, m: __d.massErase }));
    check(`${label}: read protection: clear message, nothing erased or written, no unprotect`, /read-protected \(RDP\)/.test(e) && /does not offer it/.test(e) && !st.w && !st.e && !st.u && !st.m, e + JSON.stringify(st));
    if (shots) await fwShot(page, 'flasher-error-protected.png', false);
  });
  await step(`${label}: close`, async () => {
    await page.keyboard.press('Escape');
    check(`${label}: Escape closes the dialog`, await page.isHidden('#fwView'));
  });
  ctx.report();
  await ctx.close();

  const c2 = await newCtx(browser, base, label + ' no-webusb', ctxOpts);
  await c2.addInitScript(() => { delete Navigator.prototype.usb; });
  const p2 = await c2.newPage();
  await p2.goto(base);
  await ready(p2, false);
  await step(`${label}: no WebUSB`, async () => {
    await p2.click('#btnFirmware');
    await p2.waitForSelector('#fwNoUSB');
    check(`${label}: no WebUSB: "needs WebUSB: Chrome or Edge"`, /Firmware update needs WebUSB: Chrome or Edge on a computer/.test(await p2.textContent('#fwNoUSB')) && !(await p2.$('#fwConnect')));
    if (shots) await fwShot(p2, 'flasher-no-webusb.png', false);
  });
  c2.report();
  await c2.close();
}

// "Reboot into DFU" over the app's Web Serial connection, then flash and
// Start firmware: the app reconnects by itself.
async function flasherSerialFlow(browser, base, label, shots) {
  const ctx = await newCtx(browser, base, label);
  await ctx.addInitScript(FAKE_SERIAL);
  await ctx.addInitScript({ content: SIM_SRC });
  const page = await ctx.newPage();
  await page.goto(base);
  await ready(page, false);
  await step(`${label}: reboot into DFU`, async () => {
    await page.click('#btnConnect');
    await page.waitForFunction(() => S.state.connected, null, { timeout: 20000 });
    await page.click('#btnFirmware');
    await page.waitForSelector('#fwRebootDFU');
    check(`${label}: step 1 says EasyVNA Web is connected, offers Reboot into DFU`, /EasyVNA Web is connected to NanoVNA-H 4 over USB serial now/.test(await page.textContent('#fwSerial')));
    if (shots) await fwShot(page, 'flasher-help-serial.png', false);
    await page.click('#fwRebootDFU');
    await page.waitForSelector('#askYes:visible');
    const askText = await page.textContent('#askText');
    await page.click('#askYes');
    await page.waitForFunction(() => !S.state.connected && /DFU/.test(S.state.error || ''), null, { timeout: 15000 });
    const log = await page.evaluate(() => __fake.log.slice());
    check(`${label}: confirmed first, then "reset dfu" sent over the serial port and the connection dropped`, /reset dfu/.test(askText) && log.includes('cmd reset dfu') && log.includes('close'), log.slice(-6).join(' | '));
  });
  await step(`${label}: flash, then Start firmware reconnects`, async () => {
    await plug(page, { variant: 'f303', serial: 'SIM-SERIAL', pollErase: 1 });
    await fwConnect(page);
    await Promise.all([page.waitForEvent('download', { timeout: 20000 }), fwFlash(page)]);
    await page.waitForSelector('#fwDone', { timeout: 60000 });
    await page.click('#fwStart2');
    await page.waitForFunction(() => S.state.connected, null, { timeout: 20000 });
    check(`${label}: after Start firmware the app reconnects over Web Serial`, await sim(page, () => __d.left));
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

// The native program (go:embed UI and firmware), when NATIVE_BIN is set:
// started with -mock on a free port, the flasher flow runs against it.
async function nativeFlasher(browser) {
  const bin = process.env.NATIVE_BIN;
  if (!bin) { console.log('\n(NATIVE_BIN not set: native FIRMWARE flow skipped)'); return; }
  const port = await new Promise((resolve) => { const s = http.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)); }); });
  const base = `http://127.0.0.1:${port}/`;
  const child = require('child_process').spawn(bin, ['-mock', '-no-browser', '-listen', `127.0.0.1:${port}`, '-data', path.join(TMP, 'native-data')], { stdio: 'ignore' });
  try {
    for (let i = 0; i < 100; i++) {
      const up = await new Promise((resolve) => http.get(base + 'api/state', (r) => { r.resume(); resolve(r.statusCode === 200); }).on('error', () => resolve(false)));
      if (up) break;
      await new Promise((r) => setTimeout(r, 100));
    }
    console.log(`\n== FIRMWARE (simulated DFU device) in the native program (${base})`);
    await flasherFlow(browser, base, 'native flasher', false, { allowPrefix: base });
  } finally { child.kill(); }
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
      if (ONLY === 'flasher') { // E2E_ONLY=flasher: just the FIRMWARE dialog
        console.log(`\n== FIRMWARE (simulated DFU device) over ${label}`);
        await flasherFlow(browser, base, label + ' flasher', label === 'http' && !!SHOTS);
        await flasherSerialFlow(browser, base, label + ' flasher-serial', label === 'http' && !!SHOTS);
        continue;
      }
      console.log(`\n== simulator over ${label}`);
      await demoFlow(browser, base, label, label === 'http' && !!SHOTS);
      console.log(`\n== Web Serial (fake port) over ${label}`);
      await serialFlow(browser, base, label + ' usb', label === 'http' && !!SHOTS);
      console.log(`\n== NanoVNA V2 (experimental) over ${label}`);
      await v2Flow(browser, base, label + ' v2', label === 'http' && !!SHOTS);
      console.log(`\n== no Web Serial over ${label}`);
      await noSerialFlow(browser, base, label + ' no-serial', label === 'http' && !!SHOTS);
      console.log(`\n== FIRMWARE (simulated DFU device) over ${label}`);
      await flasherFlow(browser, base, label + ' flasher', label === 'http' && !!SHOTS);
      await flasherSerialFlow(browser, base, label + ' flasher-serial', label === 'http' && !!SHOTS);
    }
    await nativeFlasher(browser);
    if (ONLY !== 'flasher') {
      console.log('\n== offline download (http)');
      await offlineFlow(browser, httpBase, 'offline');
      await phone(browser, httpBase);
    }
  } finally {
    await browser.close();
    srv.close();
  }
  console.log(`\n${passed} checks passed, ${failures.length} failed`);
  if (failures.length) { for (const f of failures) console.log('  - ' + f); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
