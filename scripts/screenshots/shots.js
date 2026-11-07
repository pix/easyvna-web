// Regenerate the README screenshots (doc/screenshots/*.png).
//
// Run through scripts/screenshots.sh, which starts the server. Environment:
//   BASE   server URL (default http://127.0.0.1:18200/)
//   OUT    output directory (default doc/screenshots)
//   MODE   "mock" (full set, simulated antennas) or "hardware" (real
//          NanoVNA, server started with -readonly: no calibration, no
//          antenna swaps; HOLD/COMPARE need a person and are skipped)
//   PLAYWRIGHT_DIR  directory containing node_modules/playwright (optional)
'use strict';
const path = require('path');
const { chromium } = require(process.env.PLAYWRIGHT_DIR
  ? path.join(process.env.PLAYWRIGHT_DIR, 'node_modules', 'playwright') : 'playwright');

const BASE = process.env.BASE || 'http://127.0.0.1:18200/';
const OUT = process.env.OUT || path.join(__dirname, '..', '..', 'doc', 'screenshots');
const MODE = process.env.MODE || 'mock';
const HW = MODE === 'hardware';

(async () => {
  const browser = await chromium.launch({ headless: true });
  const errs = [];
  const ctx = await browser.newContext({ viewport: { width: 1280, height: 1000 }, colorScheme: 'light' });
  const page = await ctx.newPage();
  page.on('pageerror', (e) => errs.push(e.message));
  page.on('console', (m) => m.type() === 'error' && errs.push(m.text()));
  const api = (m, p, b) => page.evaluate(async ([m, p, b]) => {
    const r = await fetch(p, { method: m, headers: { 'Content-Type': 'application/json' }, body: b ? JSON.stringify(b) : undefined });
    return r.json();
  }, [m, p, b]);
  const wait = (ms = 700) => page.waitForTimeout(ms);
  const tidy = () => page.evaluate(() => {
    document.getElementById('toast').hidden = true;
    for (const d of document.querySelectorAll('details')) d.open = false; // no device info / paths
    window.scrollTo(0, 0);
  });
  const shot = async (name, opts = {}) => { await tidy(); await page.screenshot({ path: path.join(OUT, name), ...opts }); console.log('saved', name); };
  const profile = async (label) => { await page.locator('.profile', { hasText: label }).first().click(); await wait(HW ? 2500 : 900); };
  const antenna = (name) => (HW ? null : api('POST', 'api/mock/antenna', { name }));
  const calibrate = async () => { // simulator only: makes the calibration strip green
    if (HW) return;
    for (const s of ['open', 'short', 'load', 'done']) await api('POST', 'api/cal/step/' + s, s === 'open' ? { fresh: true } : {});
    await api('POST', 'api/cal/save', {});
  };

  await page.goto(BASE);
  await page.evaluate(() => { try { localStorage.removeItem('easyvna-theme'); } catch { /* ignore */ } });
  await page.reload(); await page.waitForSelector('.profile');
  const st = await api('GET', 'api/state');
  if (HW && (!st.connected || !st.readOnly)) throw new Error('hardware mode needs a connected device and a server started with -readonly');

  // 1. Hero: 869 profile, REF from a first sweep, then the current sweep.
  await antenna('869'); await profile('869'); await calibrate();
  await page.click('#aSweep'); await wait(HW ? 2500 : 700);
  await page.click('#aRef'); await wait(400);
  await antenna('869tuned'); await page.click('#aSweep'); await wait(HW ? 2500 : 900);
  await shot('hero.png', { clip: { x: 0, y: 0, width: 1280, height: 1000 } });

  // 2. Survey: identify the antenna.
  await antenna('dual'); await profile('Survey'); await calibrate(); await page.click('#aSweep'); await wait(HW ? 3000 : 900);
  await shot('survey-identify.png', { clip: { x: 0, y: 0, width: 1280, height: 1000 } });

  // 3. Fake antenna warning (simulator only).
  if (!HW) {
    await antenna('dummy'); await profile('Wide'); await calibrate(); await page.click('#aSweep'); await wait(900);
    await shot('fake-antenna-warning.png', { clip: { x: 0, y: 0, width: 1280, height: 760 } });
  }

  // 4. TUNE with the trim calculator.
  await antenna('869'); await profile('869');
  await page.click('#aTune'); await page.waitForSelector('#trimLen'); await wait(HW ? 4000 : 1500);
  if (!HW) {
    await page.fill('#trimName', 'whip-A'); await page.fill('#trimLen', '82'); await page.click('#trimStart'); await wait(1200);
    await page.click('#trimLog'); await wait(1800);
  }
  await page.setViewportSize({ width: 1280, height: 1000 }); await wait(600);
  await page.evaluate(() => { document.getElementById('toast').hidden = true; });
  await page.screenshot({ path: path.join(OUT, 'tune-trim.png') }); console.log('saved tune-trim.png');
  await page.click('#tuneClose'); await wait(800);

  // 5. COMPARE leaderboard (simulator only: needs antennas to be swapped).
  if (!HW) {
    await page.click('#aCompare'); await wait(1500);
    await page.fill('#cmpAName', 'stock whip'); await page.click('#cmpStoreA'); await wait(1000);
    for (const [ant, name] of [['869tuned', 'tuned whip'], ['dual', 'helical 433'], ['dummy', 'fake (dummy)']]) {
      await antenna(ant); await wait(1500);
      await page.fill('#cmpName', name); await page.click('#cmpAdd'); await wait(800);
    }
    await antenna('869tuned'); await wait(1500);
    await page.setViewportSize({ width: 1280, height: 1150 }); await wait(500);
    await page.evaluate(() => { document.getElementById('toast').hidden = true; });
    await page.screenshot({ path: path.join(OUT, 'compare.png') }); console.log('saved compare.png');
    await page.setViewportSize({ width: 1280, height: 1000 });
    await page.click('#cmpClose'); await wait(800);
  }

  // 6. HOLD stable positions (simulator only: needs a person bending the antenna).
  if (!HW) {
    await antenna('bend'); await profile('869');
    await page.click('#aHold'); await wait(32000); await page.click('#aLive'); await wait(800);
    const pm = page.locator('.pmain').nth(1); if (await pm.count()) { await pm.click(); await wait(900); }
    await page.setViewportSize({ width: 1280, height: 1600 }); await tidy(); await wait(600);
    const top = (await page.locator('.actions').boundingBox()).y - 12;
    const bottom = await page.evaluate(() => { const r = document.querySelector('.plotpanel').getBoundingClientRect(); return r.bottom + window.scrollY; });
    await page.screenshot({ path: path.join(OUT, 'hold.png'), clip: { x: 0, y: top, width: 1280, height: Math.min(1250, bottom - top + 12) } });
    console.log('saved hold.png');
    await page.setViewportSize({ width: 1280, height: 1000 });
    await page.click('#aHold'); await wait(300); await page.click('#askYes').catch(() => {}); await wait(500);
  }

  // 7. Report card: Wide + 869 of the same antenna (+ trim log in simulation).
  await antenna('869tuned');
  await profile('Wide'); await page.click('#aSweep'); await wait(HW ? 2500 : 900);
  await profile('869'); await page.click('#aSweep'); await wait(HW ? 2500 : 900);
  const rep = await api('POST', 'api/report', { name: HW ? 'my antenna' : 'whip-A', profiles: ['869', 'wide'], trim: HW ? '' : 'whip-A',
    notes: HW ? '' : '868 MHz quarter-wave whip, 10 cm RG316 pigtail (simulated)' });
  const rp = await ctx.newPage();
  await rp.setViewportSize({ width: 1000, height: 1500 });
  await rp.goto(BASE + rep.url); await rp.waitForTimeout(600);
  await rp.screenshot({ path: path.join(OUT, 'report.png') }); console.log('saved report.png');
  await rp.close();

  // 8. Phone.
  const ph = await browser.newContext({ viewport: { width: 390, height: 844 }, deviceScaleFactor: 2, colorScheme: 'light' });
  const p2 = await ph.newPage(); p2.on('pageerror', (e) => errs.push('phone: ' + e.message));
  await p2.goto(BASE); await p2.waitForSelector('.profile'); await p2.waitForTimeout(1200);
  await p2.evaluate(() => { document.getElementById('toast').hidden = true; });
  await p2.screenshot({ path: path.join(OUT, 'phone.png') }); console.log('saved phone.png');
  await p2.click('#aTune'); await p2.waitForTimeout(HW ? 4000 : 2500);
  await p2.evaluate(() => { document.getElementById('toast').hidden = true; });
  await p2.screenshot({ path: path.join(OUT, 'phone-tune.png') }); console.log('saved phone-tune.png');
  await p2.click('#tuneClose'); await p2.waitForTimeout(500);

  await browser.close();
  if (errs.length) { console.error('browser errors:', errs); process.exit(1); }
})().catch((e) => { console.error(e); process.exit(1); });
