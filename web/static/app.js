'use strict';
// vnaview browser UI: vanilla JS + canvas, no external dependencies.

const $ = (id) => document.getElementById(id);
const S = {
  state: null, res: null, ref: null, refName: '',
  cursorIdx: null, tab: 'smith', swrMaxUser: null, snapshots: [], lastCfgKey: '',
};

// ---------- helpers ----------
const mhz = (f, d = 2) => (f / 1e6).toFixed(d);
const mhzR = (f) => String(+(f / 1e6).toFixed(3)); // 388.92, 1000
const sgn = (v, d = 1) => (v >= 0 ? '+' : '−') + Math.abs(v).toFixed(d);
function fmtZ(r, x) {
  const big = (v) => Math.abs(v) >= 1e4 ? v.toExponential(1) : v.toFixed(1);
  return `${big(r)} ${x < 0 ? '−' : '+'} j${big(Math.abs(x))} Ω`;
}
function fmtLC(x, f) {
  const w = 2 * Math.PI * f;
  if (x > 0) { const l = x / w; return l >= 1e-6 ? (l * 1e6).toFixed(2) + ' µH' : (l * 1e9).toFixed(2) + ' nH'; }
  if (x < 0) { const c = -1 / (w * x); return c >= 1e-9 ? (c * 1e9).toFixed(2) + ' nF' : (c * 1e12).toFixed(2) + ' pF'; }
  return '—';
}
function esc(s) { return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c])); }
function swrText(v) { return v >= 99 ? '>99' : v.toFixed(2); }

async function api(method, path, body) {
  const opt = { method, headers: {} };
  if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
  const r = await fetch(path, opt);
  const ct = r.headers.get('Content-Type') || '';
  const data = ct.includes('json') ? await r.json() : await r.text();
  if (!r.ok) throw new Error((data && data.error) || r.statusText);
  return data;
}
let toastTimer;
function toast(msg, err = false) {
  const t = $('toast');
  t.textContent = msg; t.className = 'toast' + (err ? ' err' : ''); t.hidden = false;
  clearTimeout(toastTimer); toastTimer = setTimeout(() => { t.hidden = true; }, err ? 6000 : 2500);
}
async function run(fn) { try { return await fn(); } catch (e) { toast(e.message, true); } }

function store(k, v) { try { if (v === undefined) return localStorage.getItem(k); localStorage.setItem(k, v); } catch { return null; } }

// ---------- theme ----------
function applyTheme(t) { if (t) document.documentElement.dataset.theme = t; }
applyTheme(store('vnaview-theme'));
$('btnTheme').onclick = () => {
  const cur = document.documentElement.dataset.theme ||
    (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
  const next = cur === 'dark' ? 'light' : 'dark';
  applyTheme(next); store('vnaview-theme', next); drawAll();
};
matchMedia('(prefers-color-scheme: dark)').addEventListener('change', drawAll);

// ---------- server events ----------
function connectEvents() {
  const es = new EventSource('api/events');
  es.addEventListener('state', (e) => { S.state = JSON.parse(e.data); renderState(); });
  es.addEventListener('sweep', (e) => { setResult(JSON.parse(e.data)); });
  es.addEventListener('snapshots', (e) => { S.snapshots = JSON.parse(e.data); renderSnaps(); });
  es.onerror = () => { showBanner('Lost connection to the vnaview server, retrying…'); };
  es.onopen = () => { showBanner(''); };
}
function showBanner(msg) { const b = $('banner'); b.textContent = msg; b.hidden = !msg; }

// ---------- state rendering ----------
const STEPS = ['open', 'short', 'load', 'done', 'save'];
const CUSTOM_SLOT = 2; // matches the PiX firmware's custom profile slot
const STEP_HELP = {
  open: 'Connect the OPEN standard at the end of the pigtail (where the antenna goes), then tap OPEN. OPEN starts a new calibration.',
  short: 'Connect the SHORT standard, then tap SHORT.',
  load: 'Connect the 50 Ω LOAD, then tap LOAD.',
  done: 'Tap DONE to compute the correction and apply it.',
  save: 'Tap SAVE to store the calibration in the slot for this profile.',
};

function currentProfile() {
  const st = S.state; if (!st) return null;
  return st.profiles.find((p) => p.id === st.config.profile) || null;
}

function renderState() {
  const st = S.state; if (!st) return;
  // device pill
  const pill = $('devpill');
  pill.classList.toggle('on', st.connected);
  if (st.connected && st.info) {
    const pix = st.info.fork && /^pix/i.test(st.info.fork);
    $('devtext').textContent = `${st.info.model} · fw ${st.info.version}${pix ? ' · PiX fork' : ''}${st.info.mock ? ' · MOCK' : ''}`;
  } else {
    $('devtext').textContent = 'not connected';
  }
  pill.title = st.info ? `${st.info.port}, ${st.info.maxPoints} points/scan${st.info.fork ? ', fork: ' + st.info.fork : ''}` : (st.error || '');
  const cal = st.cal.status;
  const cp = $('calpill');
  cp.className = 'pill ' + (!st.connected || !cal.known ? '' : cal.applied ? 'calon' : 'caloff');
  cp.textContent = !st.connected ? 'CAL –' : !cal.known ? 'CAL ?' : cal.applied ? 'CAL ON' : 'CAL OFF';
  cp.title = 'Firmware reports: ' + (cal.raw || '(nothing)');
  showBanner(!st.connected && st.error ? 'Device: ' + st.error : '');

  // continuous / busy
  const cont = $('btnCont');
  cont.setAttribute('aria-pressed', String(st.continuous));
  $('btnSingle').disabled = !!st.busy;

  renderProfiles();
  renderCal();
  renderAdvanced();
  $('tabS21').hidden = !st.config.s21;
  if (!st.config.s21 && S.tab === 's21') selectTab('smith');
  $('devinfo').textContent = st.info
    ? [`Driver: ${st.info.driver}`, `Port: ${st.info.port}`, `Max points per scan: ${st.info.maxPoints}`,
       `Calibration slots: ${st.info.calSlots}`, `Firmware cal status: ${cal.raw || '(none)'}`,
       st.mockAntenna ? `Mock antenna: ${st.mockAntenna}` : '',
       st.info.fork ? `Fork: ${st.info.fork}` : '',
       st.deviceProfiles ? 'Device profiles (firmware `profile`):\n' + [...st.deviceProfiles.profiles, ...(st.deviceProfiles.custom ? [st.deviceProfiles.custom] : [])]
         .map((d) => `  ${d.name.padEnd(6)} ${mhzR(d.start)}–${mhzR(d.stop)} MHz, ${d.points} pts, slot ${d.slot}${d.cal ? ', cal saved' : ''}${d.active ? ' [active]' : ''}`).join('\n') +
         (st.deviceProfiles.boot ? `\n  boot profile: ${st.deviceProfiles.boot.name}${st.deviceProfiles.boot.ask ? ' (asks)' : ''}` : '')
         : 'Device profiles: not supported by this firmware (using sweep + recall)', `Data dir: ${st.dataDir}`, '', ...(st.info.raw || [])].filter((l, i, a) => l !== '' || i === a.indexOf('')).join('\n')
    : (st.error || 'Not connected.') + `\nData dir: ${st.dataDir}`;
  const key = st.config.profile + st.config.start + st.config.stop;
  if (key !== S.lastCfgKey) { S.lastCfgKey = key; S.swrMaxUser = null; }
  updateScaleButtons();
}

function renderProfiles() {
  const st = S.state; const box = $('profiles');
  box.innerHTML = '';
  for (const p of st.profiles) {
    const b = document.createElement('button');
    b.type = 'button'; b.className = 'profile';
    b.setAttribute('aria-pressed', String(st.config.profile === p.id));
    const meta = st.cal.settings.slots && st.cal.settings.slots[p.calSlot];
    const dp = st.deviceProfiles && st.deviceProfiles.profiles.find((d) => d.name === p.id);
    const saved = dp ? dp.cal : !!(meta && meta.start === p.start && meta.stop === p.stop);
    const big = p.identify ? esc(p.label) : `${esc(p.label)}<small>MHz</small>`;
    b.innerHTML = `<span class="big">${big}</span><span class="sub">${esc(p.sub)}</span>
      <span class="calnote ${saved ? 'ok' : ''}">${saved ? '✓ cal in slot ' + p.calSlot : 'slot ' + p.calSlot + ': no saved cal'}${dp ? ' · on device' : ''}</span>`;
    b.onclick = () => selectProfile(p.id);
    box.appendChild(b);
  }
}

async function selectProfile(id) {
  await run(async () => {
    const r = await api('POST', 'api/profile', { id });
    if (r.message) toast(r.message);
    if (!r.state.continuous && r.state.connected) await api('POST', 'api/sweep');
  });
}

function renderCal() {
  const st = S.state; const c = st.cal; const cfg = st.config;
  const p = currentProfile();
  const name = p ? (p.identify ? 'Wide' : p.label) + ' profile' : 'custom sweep';
  $('calfor').textContent = `for the ${name} (${mhzR(cfg.start)}–${mhzR(cfg.stop)} MHz). Each profile needs its own calibration.`;
  const a = c.active;
  let act;
  if (!st.connected) act = '';
  else if (!a) act = c.status.applied ? '<span class="muted">Active cal: range unknown (loaded by firmware)</span>' : '<span class="muted">No calibration applied</span>';
  else if (c.matches) act = `<span style="color:var(--good-ink)">✓ Active cal: ${esc(a.source)}, ${mhzR(a.start)}–${mhzR(a.stop)} MHz, matches this sweep</span>`;
  else act = `<span style="color:var(--warn-ink)">! Active cal is for ${mhzR(a.start)}–${mhzR(a.stop)} MHz (${esc(a.source)}): recalibrate or recall this profile's slot</span>`;
  $('calactive').innerHTML = act;

  const flow = c.flow || { steps: {} };
  const flowCurrent = flow.profile === cfg.profile && flow.start === cfg.start && flow.stop === cfg.stop;
  const steps = flowCurrent ? flow.steps || {} : {};
  const busy = !!st.busy;
  let next = STEPS.find((s) => steps[s] !== 'done');
  for (const btn of document.querySelectorAll('.calstep')) {
    const s = btn.dataset.step;
    btn.classList.remove('done', 'error', 'running', 'next');
    if (steps[s]) btn.classList.add(steps[s]);
    if (s === next && !steps[s]) btn.classList.add('next');
    btn.disabled = busy || !st.connected ||
      (s === 'done' && !(steps.open === 'done' && steps.short === 'done' && steps.load === 'done')) ||
      (s === 'save' && steps.done !== 'done' && !(c.active && c.matches));
  }
  // slot select
  const sel = $('slotSel');
  const n = (st.info && st.info.calSlots) || 7;
  if (sel.options.length !== n) {
    sel.innerHTML = '';
    for (let i = 0; i < n; i++) sel.add(new Option(String(i), String(i)));
  }
  if (document.activeElement !== sel) sel.value = String(p ? p.calSlot : CUSTOM_SLOT);
  $('slotlbl').textContent = '→ ' + sel.value;
  $('btnRecall').disabled = busy || !st.connected;
  $('autoRecall').checked = !!c.settings.autoRecall;

  let msg = '';
  const running = Object.keys(steps).find((k) => steps[k] === 'running');
  if (running) msg = `Measuring ${running.toUpperCase()}…`;
  else if (flowCurrent && flow.message && /fail|did not/.test(flow.message)) msg = `<span class="bad">${esc(flow.message)}</span>`;
  else if (next) msg = (flowCurrent && flow.message ? esc(flow.message) + '. ' : '') + STEP_HELP[next];
  else msg = 'Calibration complete and saved.';
  if (!st.connected) msg = 'Connect a NanoVNA to calibrate.';
  $('calmsg').innerHTML = msg + (st.connected ? ` <span class="muted">Firmware: ${esc(c.status.raw || 'no cal data')}</span>` : '');
}

for (const btn of document.querySelectorAll('.calstep')) {
  btn.onclick = () => run(async () => {
    const s = btn.dataset.step;
    if (s === 'save') {
      await api('POST', 'api/cal/save', { slot: Number($('slotSel').value) });
      toast('Calibration saved to slot ' + $('slotSel').value);
      return;
    }
    await api('POST', 'api/cal/step/' + s, s === 'open' ? { fresh: true } : {});
    if (s === 'done' && S.state && !S.state.continuous) await api('POST', 'api/sweep');
  });
}
$('slotSel').onchange = () => run(async () => {
  const p = currentProfile();
  $('slotlbl').textContent = '→ ' + $('slotSel').value;
  if (p) await api('PUT', `api/profiles/${encodeURIComponent(p.id)}/slot`, { slot: Number($('slotSel').value) });
});
$('btnRecall').onclick = () => run(async () => {
  await api('POST', 'api/cal/recall', { slot: Number($('slotSel').value) });
  toast('Recalled slot ' + $('slotSel').value);
});
$('autoRecall').onchange = () => run(() => api('POST', 'api/settings', { autoRecall: $('autoRecall').checked }));

// ---------- sweep controls ----------
$('btnSingle').onclick = () => run(() => api('POST', 'api/sweep'));
$('btnCont').onclick = () => run(() => api('POST', 'api/continuous', { on: !(S.state && S.state.continuous) }));
$('btnConnect').onclick = () => run(async () => { await api('POST', 'api/connect'); toast('Connected'); });

// ---------- advanced ----------
function renderAdvanced() {
  const cfg = S.state.config;
  const dc = S.state.deviceProfiles && S.state.deviceProfiles.custom;
  const meta = S.state.cal.settings.slots && S.state.cal.settings.slots[CUSTOM_SLOT];
  const src = dc && dc.cal ? dc : (!S.state.deviceProfiles && meta ? meta : null);
  const cb = $('btnCustom');
  cb.hidden = !src;
  if (src) cb.textContent = `Load saved custom range ${mhzR(src.start)}–${mhzR(src.stop)} MHz (slot ${CUSTOM_SLOT})`;
  const form = $('advForm');
  if (form.contains(document.activeElement)) return;
  $('advStart').value = (cfg.start / 1e6).toString();
  $('advStop').value = (cfg.stop / 1e6).toString();
  const ps = $('advPoints');
  if (!ps.options.length) for (const n of S.state.pointChoices) ps.add(new Option(String(n), String(n)));
  if (![...ps.options].some((o) => o.value === String(cfg.points))) ps.add(new Option(String(cfg.points), String(cfg.points)));
  ps.value = String(cfg.points);
  $('advS21').checked = cfg.s21;
  updatePe();
}
function peDelayPs() {
  const len = parseFloat($('peLen').value) || 0, vf = parseFloat($('peVF').value) || 1;
  return len / 100 / (vf * 299792458) * 1e12;
}
function updatePe() { $('pePs').textContent = `= ${peDelayPs().toFixed(1)} ps one-way (active: ${S.state ? S.state.config.portExtPs.toFixed(1) : 0} ps)`; }
$('btnCustom').onclick = () => selectProfile('custom');
$('peLen').oninput = updatePe; $('peVF').oninput = updatePe;
$('advForm').onsubmit = (e) => {
  e.preventDefault();
  run(async () => {
    await api('POST', 'api/config', {
      start: Math.round(parseFloat($('advStart').value) * 1e6), stop: Math.round(parseFloat($('advStop').value) * 1e6),
      points: Number($('advPoints').value), s21: $('advS21').checked, portExtPs: peDelayPs(),
    });
    document.activeElement.blur();
    toast('Sweep settings applied');
    if (S.state && !S.state.continuous && S.state.connected) await api('POST', 'api/sweep');
  });
};

// ---------- snapshots ----------
async function loadSnaps() { S.snapshots = await api('GET', 'api/snapshots'); renderSnaps(); }
function renderSnaps() {
  const tb = $('snapList'); tb.innerHTML = '';
  if (!S.snapshots.length) { tb.innerHTML = '<tr><td colspan="5" class="muted">No snapshots yet. Save one to compare later sweeps against it.</td></tr>'; return; }
  for (const s of S.snapshots) {
    const tr = document.createElement('tr');
    const isRef = S.refName === s.name;
    if (isRef) tr.className = 'ref';
    const q = encodeURIComponent(s.name);
    tr.innerHTML = `<td>${esc(s.name)}</td><td>${new Date(s.time).toLocaleString()}</td><td>${esc(s.profile)}</td><td>${s.points}</td>
      <td class="act"><button class="ghost small" data-a="ref" type="button">${isRef ? 'Hide overlay' : 'Overlay'}</button>
      <a class="btn ghost small" href="api/export?format=s1p&snapshot=${q}" download>.s1p</a>
      <a class="btn ghost small" href="api/export?format=csv&snapshot=${q}" download>CSV</a>
      <button class="ghost small" data-a="del" type="button">Delete</button></td>`;
    tr.querySelector('[data-a=ref]').onclick = () => run(async () => {
      if (isRef) { S.ref = null; S.refName = ''; } else { S.ref = normalize(await api('GET', 'api/snapshots/' + q)); S.refName = s.name; }
      renderSnaps(); drawAll();
    });
    tr.querySelector('[data-a=del]').onclick = () => run(async () => {
      if (!confirm(`Delete snapshot "${s.name}"?`)) return;
      await api('DELETE', 'api/snapshots/' + q);
      if (isRef) { S.ref = null; S.refName = ''; drawAll(); }
    });
    tb.appendChild(tr);
  }
}
$('snapForm').onsubmit = (e) => {
  e.preventDefault();
  run(async () => {
    const r = await api('POST', 'api/snapshots', { name: $('snapName').value });
    toast('Saved snapshot ' + r.name); $('snapName').value = '';
  });
};

// ---------- results ----------
function normalize(r) {
  r.summary = r.summary || {};
  r.summary.dips = r.summary.dips || [];
  r.summary.bands = r.summary.bands || [];
  r.config.bands = r.config.bands || [];
  return r;
}
function setResult(r) {
  normalize(r);
  const first = !S.res || S.res.config.profile !== r.config.profile || S.res.freq.length !== r.freq.length;
  S.res = r;
  if (first || S.cursorIdx == null || S.cursorIdx >= r.freq.length) S.cursorIdx = r.summary.minSWRIndex;
  $('expS2p').hidden = !(r.s21 && r.s21.length);
  $('sweepinfo').textContent = `#${r.seq} · ${r.freq.length} pts · ${r.durationMs.toFixed(0)} ms · ${new Date(r.time).toLocaleTimeString()}`;
  renderVerdict();
  drawAll();
}

const VICON = { good: '✓', ok: '!', bad: '✕', 'n/a': '–' };
const VLABEL = { good: 'GOOD', ok: 'OK', bad: 'BAD', 'n/a': 'N/A' };
function badge(v) { const c = v === 'n/a' ? 'na' : v; return `<span class="badge ${c}">${VICON[v] || ''} ${VLABEL[v] || v}</span>`; }

function bandTitle(b) {
  if (b.name === '433' || b.name === '869' || b.name === '915') return `${b.name} MHz band`;
  return `${b.name} MHz sub-band`;
}

function renderVerdict() {
  const r = S.res; const box = $('verdict');
  if (!r) { box.innerHTML = '<div class="empty">Pick a profile above. Each tap runs one sweep.</div>'; return; }
  const sm = r.summary;
  if (r.config.identify) { box.innerHTML = identifyCard(r); wireDipRows(); return; }
  if (!sm.bands.length) {
    box.innerHTML = `<div class="vcard na wide"><div class="vtitle"><h3>Custom sweep</h3><span class="range">${mhz(r.freq[0], 1)}–${mhz(r.freq[r.freq.length - 1], 1)} MHz</span></div>
      <div class="vmain"><div class="stat"><div class="v">${swrText(sm.minSWR)}</div><div class="l">best SWR @ ${mhz(sm.minSWRFreq)} MHz</div></div>
      <div class="stat"><div class="v">${sm.bestRL.toFixed(1)}<small> dB</small></div><div class="l">best return loss</div></div></div>
      ${dipTable(sm.dips)}</div>`;
    wireDipRows(); return;
  }
  box.innerHTML = sm.bands.map((b, i) => bandCard(r, b, i === 0)).join('');
}

function edgeHint(r) {
  const sm = r.summary; const n = r.freq.length;
  if (sm.minSWRIndex === 0) return `Lowest SWR is at the low edge of the sweep (${mhz(r.freq[0], 1)} MHz): the resonance is below this range. Use Wide to find it.`;
  if (sm.minSWRIndex === n - 1) return `Lowest SWR is at the high edge of the sweep (${mhz(r.freq[n - 1], 1)} MHz): the resonance is above this range. Use Wide to find it.`;
  return 'No clear resonance dip in this sweep.';
}

function bandCard(r, b, primary) {
  const band = b.band; const v = b.verdict === 'n/a' ? 'na' : b.verdict;
  const ref = band.ref || (band.lo + band.hi) / 2;
  let res = '';
  if (b.dipFreq != null) {
    const pos = b.dipEdgeOffset === 0 ? 'inside the band' : `${Math.abs(b.dipEdgeOffset / 1e6).toFixed(1)} MHz ${b.dipEdgeOffset < 0 ? 'below' : 'above'} the band`;
    res = `Resonance <b>${mhz(b.dipFreq)} MHz</b> (SWR ${swrText(b.dipSWR)}): ${pos}, ${sgn(b.dipOffset / 1e6)} MHz from ${band.ref ? 'reference' : 'centre'} ${mhzR(ref)} MHz.`;
  } else res = edgeHint(r);
  const cov = b.covered ? '' : '<div class="vline" style="color:var(--warn-ink)">! The sweep does not cover the whole band.</div>';
  const advice = primary && b.dipAdvice && b.dipEdgeOffset !== 0 ? `<div class="advice">${esc(b.dipAdvice)}</div>` : '';
  return `<div class="vcard ${v} wide ${primary ? '' : 'compact'}">
    <div class="vtitle"><h3>${bandTitle(band)}</h3><span class="range">${mhz(band.lo, 2)}–${mhz(band.hi, 2)} MHz</span>${badge(b.verdict)}</div>
    <div class="vmain">
      <div class="stat"><div class="v">${swrText(b.worstSWR)}</div><div class="l">worst SWR in band @ ${mhz(b.worstSWRFreq)} MHz</div></div>
      <div class="stat"><div class="v">${swrText(b.bestSWR)}</div><div class="l">best SWR in band @ ${mhz(b.bestSWRFreq)} MHz</div></div>
      <div class="stat"><div class="v">${b.worstRL.toFixed(1)}<small> dB</small></div><div class="l">return loss, worst in band</div></div>
      ${primary ? `<div class="stat"><div class="v">${b.pctBelow2.toFixed(0)}<small> %</small></div><div class="l">of band with SWR &lt; 2</div></div>` : ''}
    </div>
    <div class="vline">${res}</div>${cov}${advice}</div>`;
}

function dipTable(dips) {
  if (!dips.length) return '<div class="vline">No clear resonance (needs ≥ 3 dB return loss and 3 dB prominence).</div>';
  const rows = dips.map((d, i) => `<tr class="click" data-idx="${d.index}">
    <td>${i + 1}</td><td><b>${mhz(d.freq)}</b></td><td>${swrText(d.swr)}</td><td class="hide-sm">${d.rl.toFixed(1)} dB</td>
    <td class="hide-sm">${fmtZ(d.r, d.x)}</td><td class="hide-sm">${d.bw2.found ? (d.bw2.width / 1e6).toFixed(1) + ' MHz' : '–'}</td>
    <td>${d.inBand ? `<span style="color:var(--good-ink)">✓ in ${esc(d.band)} band.</span> ` : ''}${esc(d.inBand ? d.advice.replace(/^In the .*? band\s*\([^)]*\)\.\s*/, '') : d.advice)}</td></tr>`).join('');
  return `<table class="dips"><thead><tr><th>#</th><th>MHz</th><th>SWR</th><th class="hide-sm">RL</th><th class="hide-sm">Z</th><th class="hide-sm">BW SWR&lt;2</th><th>LoRa band</th></tr></thead><tbody>${rows}</tbody></table>`;
}

function identifyCard(r) {
  const sm = r.summary;
  const chips = sm.bands.map((b) => `<span class="chip">${badge(b.verdict)} ${esc(b.band.name)} MHz: worst SWR ${swrText(b.worstSWR)}</span>`).join('');
  const nd = sm.dips.length;
  return `<div class="vcard na wide">
    <div class="vtitle"><h3>Antenna identification</h3><span class="range">${mhz(r.freq[0], 0)}–${mhz(r.freq[r.freq.length - 1], 0)} MHz</span></div>
    <div class="vmain"><div class="stat"><div class="v">${nd}</div><div class="l">resonance${nd === 1 ? '' : 's'} found</div></div>
    <div class="stat"><div class="v">${swrText(sm.minSWR)}</div><div class="l">best SWR @ ${mhz(sm.minSWRFreq)} MHz</div></div></div>
    ${dipTable(sm.dips)}
    <div class="bandchips">${chips}</div>
    <div class="vline muted small">Direction hints assume a length-tuned monopole, dipole or whip: resonance below the band means the radiator is too long.</div></div>`;
}
function wireDipRows() {
  for (const tr of document.querySelectorAll('.dips tr.click')) tr.onclick = () => { S.cursorIdx = Number(tr.dataset.idx); drawAll(); };
}

// ---------- drawing ----------
function css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
function setup(cv) {
  const dpr = window.devicePixelRatio || 1;
  const w = cv.clientWidth, h = cv.clientHeight;
  if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) { cv.width = Math.round(w * dpr); cv.height = Math.round(h * dpr); }
  const ctx = cv.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  ctx.clearRect(0, 0, w, h);
  return { ctx, w, h };
}
function niceTicks(lo, hi, maxN) {
  const span = hi - lo; if (!(span > 0)) return [lo];
  const raw = span / Math.max(1, maxN), mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) || 10 * mag;
  const out = []; for (let v = Math.ceil(lo / step) * step; v <= hi + step * 1e-9; v += step) out.push(+v.toFixed(10));
  return out;
}

// drawLine renders a frequency plot. o: {ys:[{y,color,width,dash,label}], ymin, ymax, yticks, yfmt,
// hlines:[{y,label}], bands, dips, ref}
function drawLine(cv, o) {
  const { ctx, w, h } = setup(cv);
  const r = S.res;
  const C = { grid: css('--grid'), axis: css('--axis'), base: css('--baseline'), ink: css('--ink'), ink2: css('--ink2'), band: css('--band'), band2: css('--band2'), muted: css('--muted') };
  const pad = { l: 46, r: 14, t: 18, b: 26 };
  const x0 = pad.l, x1 = w - pad.r, y0 = pad.t, y1 = h - pad.b;
  ctx.font = '11px system-ui, sans-serif';
  if (!r) { ctx.fillStyle = C.muted; ctx.textAlign = 'center'; ctx.fillText('No sweep yet', w / 2, h / 2); cv._map = null; return; }
  const xmin = r.freq[0], xmax = r.freq[r.freq.length - 1];
  const fx = (f) => x0 + (f - xmin) / (xmax - xmin) * (x1 - x0);
  const fy = (v) => y1 - (v - o.ymin) / (o.ymax - o.ymin) * (y1 - y0);
  // bands
  for (const b of r.config.bands || []) {
    if (b.hi < xmin || b.lo > xmax) continue;
    const a = fx(Math.max(b.lo, xmin)), z = fx(Math.min(b.hi, xmax));
    const sub = b.hi - b.lo < 1e6;
    ctx.fillStyle = sub ? C.band2 : C.band;
    ctx.fillRect(a, y0, Math.max(1.5, z - a), y1 - y0);
    if (!sub) { ctx.fillStyle = C.ink2; ctx.textAlign = 'left'; ctx.fillText(b.name, a + 3, y0 - 5); }
  }
  // grid + y axis
  ctx.strokeStyle = C.grid; ctx.lineWidth = 1; ctx.fillStyle = C.axis; ctx.textAlign = 'right'; ctx.textBaseline = 'middle';
  for (const t of o.yticks) {
    const y = Math.round(fy(t)) + 0.5; if (y < y0 - 1 || y > y1 + 1) continue;
    ctx.beginPath(); ctx.moveTo(x0, y); ctx.lineTo(x1, y); ctx.stroke();
    ctx.fillText(o.yfmt(t), x0 - 6, y);
  }
  ctx.textAlign = 'center'; ctx.textBaseline = 'top';
  const xt = niceTicks(xmin / 1e6, xmax / 1e6, Math.floor((x1 - x0) / 80));
  for (const t of xt) {
    const x = Math.round(fx(t * 1e6)) + 0.5;
    ctx.beginPath(); ctx.moveTo(x, y0); ctx.lineTo(x, y1); ctx.stroke();
    ctx.fillText(String(+t.toFixed(3)), x, y1 + 6);
  }
  ctx.textAlign = 'left'; ctx.fillText('MHz', 2, y1 + 6);
  ctx.strokeStyle = C.base; ctx.beginPath(); ctx.moveTo(x0, y1 + 0.5); ctx.lineTo(x1, y1 + 0.5); ctx.stroke();
  // threshold lines
  ctx.setLineDash([4, 4]); ctx.textBaseline = 'bottom'; ctx.textAlign = 'right';
  for (const hl of o.hlines || []) {
    const y = Math.round(fy(hl.y)) + 0.5; if (y < y0 || y > y1) continue;
    ctx.strokeStyle = C.axis; ctx.beginPath(); ctx.moveTo(x0, y); ctx.lineTo(x1, y); ctx.stroke();
    ctx.fillStyle = C.axis; ctx.fillText(hl.label, x1 - 2, y - 2);
  }
  ctx.setLineDash([]);
  // traces (clipped)
  ctx.save(); ctx.beginPath(); ctx.rect(x0, y0 - 1, x1 - x0, y1 - y0 + 2); ctx.clip();
  const plotSeries = (freq, ys, color, width, dash) => {
    ctx.strokeStyle = color; ctx.lineWidth = width; ctx.setLineDash(dash || []); ctx.lineJoin = 'round';
    ctx.beginPath();
    let pen = false;
    for (let i = 0; i < freq.length; i++) {
      const v = ys[i]; if (v == null || !isFinite(v)) { pen = false; continue; }
      const x = fx(freq[i]), y = fy(Math.max(o.ymin - (o.ymax - o.ymin), Math.min(o.ymax + (o.ymax - o.ymin), v)));
      if (pen) ctx.lineTo(x, y); else { ctx.moveTo(x, y); pen = true; }
    }
    ctx.stroke(); ctx.setLineDash([]);
  };
  if (o.ref && S.ref) plotSeries(S.ref.freq, o.ref(S.ref), css('--series2'), 2, [6, 4]);
  for (const s of o.ys) plotSeries(r.freq, s.y, s.color, s.width || 2, s.dash);
  ctx.restore();
  // dip labels
  if (o.dips) {
    // Resonance labels sit in a row at the top of the plot, pointing down at the dip.
    ctx.fillStyle = C.ink2; ctx.textAlign = 'center'; ctx.textBaseline = 'top';
    ctx.font = '600 11px system-ui, sans-serif';
    for (const d of r.summary.dips) {
      const x = fx(d.freq), lx = Math.min(x1 - 22, Math.max(x0 + 22, x));
      const t = mhz(d.freq, 1) + ' MHz', tw = ctx.measureText(t).width;
      ctx.fillStyle = css('--surface'); ctx.fillRect(lx - tw / 2 - 4, y0 + 3, tw + 8, 16);
      ctx.fillStyle = C.ink2; ctx.fillText(t, lx, y0 + 6);
      ctx.beginPath(); ctx.moveTo(x, y0 + 26); ctx.lineTo(x - 4, y0 + 20); ctx.lineTo(x + 4, y0 + 20); ctx.closePath(); ctx.fill();
    }
    ctx.font = '11px system-ui, sans-serif';
  }
  // in-plot legend for multi-series plots
  if (o.legend) {
    let lx = x0 + 8; ctx.textBaseline = 'middle'; ctx.textAlign = 'left';
    for (const s of o.ys) {
      ctx.strokeStyle = s.color; ctx.lineWidth = 2; ctx.beginPath(); ctx.moveTo(lx, y0 + 10); ctx.lineTo(lx + 16, y0 + 10); ctx.stroke();
      ctx.fillStyle = C.ink2; ctx.fillText(s.label, lx + 21, y0 + 10); lx += 30 + ctx.measureText(s.label).width;
    }
  }
  // cursor
  const ci = S.cursorIdx;
  if (ci != null && ci < r.freq.length) {
    const x = Math.round(fx(r.freq[ci])) + 0.5;
    ctx.strokeStyle = C.ink; ctx.lineWidth = 1; ctx.globalAlpha = 0.55;
    ctx.beginPath(); ctx.moveTo(x, y0); ctx.lineTo(x, y1); ctx.stroke(); ctx.globalAlpha = 1;
    for (const s of o.ys) {
      const v = s.y[ci]; if (v == null || !isFinite(v)) continue;
      const y = fy(Math.max(o.ymin, Math.min(o.ymax, v)));
      ctx.fillStyle = s.color; ctx.strokeStyle = css('--surface'); ctx.lineWidth = 2;
      ctx.beginPath(); ctx.arc(x, y, 4.5, 0, 2 * Math.PI); ctx.fill(); ctx.stroke();
    }
  }
  cv._map = { x0, x1, inv: (px) => xmin + (px - x0) / (x1 - x0) * (xmax - xmin) };
}

function nearestIdx(freq, f) {
  let lo = 0, hi = freq.length - 1;
  while (hi - lo > 1) { const m = (lo + hi) >> 1; if (freq[m] <= f) lo = m; else hi = m; }
  return Math.abs(freq[lo] - f) <= Math.abs(freq[hi] - f) ? lo : hi;
}

function swrMax() {
  if (S.swrMaxUser) return S.swrMaxUser;
  return S.res && S.res.config.identify ? 10 : 5;
}
function updateScaleButtons() {
  const m = swrMax();
  for (const b of document.querySelectorAll('#swrScale button')) b.setAttribute('aria-pressed', String(Number(b.dataset.max) === m));
}
for (const b of document.querySelectorAll('#swrScale button')) b.onclick = () => { S.swrMaxUser = Number(b.dataset.max); updateScaleButtons(); drawAll(); };

function drawSWR() {
  const m = swrMax();
  const ticks = m <= 3 ? [1, 1.5, 2, 2.5, 3] : m <= 5 ? [1, 1.5, 2, 3, 4, 5] : [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];
  drawLine($('swrPlot'), {
    ys: S.res ? [{ y: S.res.derived.swr, color: css('--series1'), label: 'SWR' }] : [],
    ymin: 1, ymax: m, yticks: ticks, yfmt: (t) => String(t),
    hlines: [{ y: 1.5, label: 'SWR 1.5' }, { y: 2, label: 'SWR 2' }],
    ref: (rr) => rr.derived.swr, dips: (d) => d.swr,
  });
  const lg = $('legend');
  lg.innerHTML = `<span><i style="border-color:var(--series1)"></i>Live</span>` +
    (S.ref ? `<span><i class="dash" style="border-color:var(--series2)"></i>Ref: ${esc(S.refName)}</span>` : '') +
    `<span class="band"><i></i>Band</span>`;
  updateScaleButtons();
}
function drawRL() {
  const r = S.res;
  let lo = -30;
  if (r) for (const v of r.derived.s11dB) if (v < lo) lo = v;
  lo = Math.max(-60, Math.floor(lo / 10) * 10);
  drawLine($('rlPlot'), {
    ys: r ? [{ y: r.derived.s11dB, color: css('--series1'), label: 'S11' }] : [],
    ymin: lo, ymax: 0, yticks: niceTicks(lo, 0, 6), yfmt: (t) => t + ' dB',
    hlines: [{ y: -9.54, label: 'SWR 2 (RL 9.5 dB)' }, { y: -13.98, label: 'SWR 1.5 (RL 14 dB)' }],
    ref: (rr) => rr.derived.s11dB, dips: (d) => -d.rl,
  });
}
function drawZ() {
  const r = S.res;
  let lo = -100, hi = 150;
  if (r) {
    for (const a of [r.derived.r, r.derived.x]) for (const v of a) { if (v > hi) hi = v; if (v < lo) lo = v; }
    hi = Math.min(hi, 600); lo = Math.max(lo, -600);
  }
  drawLine($('zPlot'), {
    ys: r ? [{ y: r.derived.r, color: css('--series1'), label: 'R (Ω)' }, { y: r.derived.x, color: '#1baf7a', label: 'X (Ω)' }] : [],
    ymin: lo, ymax: hi, yticks: niceTicks(lo, hi, 6), yfmt: (t) => String(t), hlines: [{ y: 50, label: '50 Ω' }, { y: 0, label: '' }], legend: true,
  });
}
function drawS21() {
  const r = S.res; if (!r || !r.derived.s21dB) return drawLine($('s21Plot'), { ys: [], ymin: -80, ymax: 0, yticks: [], yfmt: String });
  let lo = -60, hi = 0;
  for (const v of r.derived.s21dB) { if (v < lo) lo = v; if (v > hi) hi = v; }
  lo = Math.max(-120, Math.floor(lo / 10) * 10); hi = Math.ceil(hi / 10) * 10;
  drawLine($('s21Plot'), { ys: [{ y: r.derived.s21dB, color: css('--series1'), label: 'S21' }], ymin: lo, ymax: hi, yticks: niceTicks(lo, hi, 6), yfmt: (t) => t + ' dB' });
}

function drawSmith() {
  const cv = $('smith');
  const { ctx, w, h } = setup(cv);
  const C = { grid: css('--grid'), axis: css('--axis'), base: css('--baseline'), ink2: css('--ink2'), muted: css('--muted') };
  const R = Math.min(w, h) / 2 - 22, cx = w / 2, cy = h / 2;
  const px = (re, im) => [cx + re * R, cy - im * R];
  ctx.font = '10.5px system-ui, sans-serif';
  ctx.save(); ctx.beginPath(); ctx.arc(cx, cy, R, 0, 2 * Math.PI); ctx.clip();
  ctx.strokeStyle = C.grid; ctx.lineWidth = 1;
  for (const r of [0.2, 0.5, 1, 2, 5]) { ctx.beginPath(); ctx.arc(cx + R * r / (1 + r), cy, R / (1 + r), 0, 2 * Math.PI); ctx.stroke(); }
  for (const x of [0.2, 0.5, 1, 2, 5]) for (const s of [1, -1]) { ctx.beginPath(); ctx.arc(cx + R, cy - s * R / x, R / x, 0, 2 * Math.PI); ctx.stroke(); }
  ctx.beginPath(); ctx.moveTo(cx - R, cy); ctx.lineTo(cx + R, cy); ctx.stroke();
  // SWR 2 circle
  ctx.setLineDash([4, 4]); ctx.strokeStyle = C.axis; ctx.beginPath(); ctx.arc(cx, cy, R / 3, 0, 2 * Math.PI); ctx.stroke(); ctx.setLineDash([]);
  ctx.restore();
  ctx.strokeStyle = C.base; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.arc(cx, cy, R, 0, 2 * Math.PI); ctx.stroke();
  // labels
  ctx.fillStyle = C.axis; ctx.textAlign = 'center'; ctx.textBaseline = 'top';
  for (const r of [0, 0.2, 0.5, 1, 2, 5]) { const g = (r - 1) / (r + 1); ctx.fillText(String(r * 50), cx + g * R + (r === 0 ? 6 : 0), cy + 3); }
  for (const x of [0.2, 0.5, 1, 2, 5]) for (const s of [1, -1]) {
    const den = x * x + 1, gr = (x * x - 1) / den, gi = s * 2 * x / den;
    const [lx, ly] = px(gr * 1.07, gi * 1.07);
    ctx.textBaseline = 'middle'; ctx.fillText((s > 0 ? '+j' : '−j') + x * 50, lx, ly);
  }
  ctx.textAlign = 'left'; ctx.textBaseline = 'bottom'; ctx.fillText('SWR 2', cx + R / 3 * 0.72 + 3, cy - R / 3 * 0.72);
  const r = S.res;
  if (!r) return;
  const trace = (res, color, width, dash) => {
    ctx.strokeStyle = color; ctx.lineWidth = width; ctx.setLineDash(dash || []); ctx.beginPath();
    res.s11.forEach((g, i) => { const [x, y] = px(g[0], g[1]); if (i) ctx.lineTo(x, y); else ctx.moveTo(x, y); });
    ctx.stroke(); ctx.setLineDash([]);
  };
  if (S.ref) trace(S.ref, css('--series2'), 2, [6, 4]);
  trace(r, css('--series1'), 2);
  // in-band segments thicker
  const bands = r.config.bands || [];
  ctx.strokeStyle = css('--series1'); ctx.lineWidth = 5; ctx.lineCap = 'round';
  for (const b of bands) {
    ctx.beginPath();
    let pen = false;
    r.freq.forEach((f, i) => {
      if (f < b.lo || f > b.hi) { pen = false; return; }
      const [x, y] = px(r.s11[i][0], r.s11[i][1]); if (pen) ctx.lineTo(x, y); else { ctx.moveTo(x, y); pen = true; }
    });
    ctx.stroke();
  }
  ctx.lineCap = 'butt';
  const ci = S.cursorIdx;
  if (ci != null && ci < r.freq.length) {
    const [x, y] = px(r.s11[ci][0], r.s11[ci][1]);
    ctx.fillStyle = css('--series1'); ctx.strokeStyle = css('--surface'); ctx.lineWidth = 2;
    ctx.beginPath(); ctx.arc(x, y, 5.5, 0, 2 * Math.PI); ctx.fill(); ctx.stroke();
    ctx.fillStyle = C.ink2; ctx.textAlign = 'left'; ctx.textBaseline = 'middle';
    ctx.fillText(mhz(r.freq[ci]) + ' MHz', Math.min(x + 9, w - 70), y);
  }
  ctx.fillStyle = C.muted; ctx.textAlign = 'left'; ctx.textBaseline = 'top';
  ctx.fillText(bands.length ? 'Thick segments: inside ' + bands.map((b) => b.name).join(', ') + ' MHz' : '', 8, 6);
  cv._smith = { cx, cy, R };
}

function renderReadout() {
  const r = S.res; const el = $('readout');
  if (!r || S.cursorIdx == null) { el.innerHTML = '<span class="muted">Hover or tap a plot to read values. Arrow keys move the cursor.</span>'; return; }
  const i = S.cursorIdx, d = r.derived, f = r.freq[i];
  const parts = [
    `<span>Cursor <b>${mhz(f, 3)} MHz</b></span>`,
    `<span>SWR <b>${swrText(d.swr[i])}</b></span>`,
    `<span>Return loss <b>${d.rl[i].toFixed(1)} dB</b></span>`,
    `<span>|Γ| <b>${d.gammaMag[i].toFixed(3)}</b></span>`,
    `<span>Z <b>${fmtZ(d.r[i], d.x[i])}</b> (${d.x[i] >= 0 ? 'L' : 'C'} ${fmtLC(d.x[i], f)})</span>`,
  ];
  if (d.s21dB) parts.push(`<span>S21 <b>${d.s21dB[i].toFixed(1)} dB</b></span>`);
  if (S.ref) {
    const j = nearestIdx(S.ref.freq, f);
    if (Math.abs(S.ref.freq[j] - f) < (r.freq[1] - r.freq[0]) * 2) parts.push(`<span style="color:var(--ink2)">Ref SWR <b>${swrText(S.ref.derived.swr[j])}</b></span>`);
  }
  el.innerHTML = parts.join('');
}

let raf = 0;
function drawAll() {
  if (raf) return;
  raf = requestAnimationFrame(() => {
    raf = 0;
    drawSWR();
    if (S.tab === 'smith') drawSmith();
    else if (S.tab === 'rl') drawRL();
    else if (S.tab === 'z') drawZ();
    else if (S.tab === 's21') drawS21();
    renderReadout();
  });
}
window.addEventListener('resize', drawAll);

// ---------- interaction ----------
function attachHover(cv) {
  const move = (e) => {
    if (!S.res || !cv._map) return;
    const rect = cv.getBoundingClientRect(); const x = e.clientX - rect.left;
    if (x < cv._map.x0 - 10 || x > cv._map.x1 + 10) return;
    S.cursorIdx = nearestIdx(S.res.freq, cv._map.inv(x)); drawAll();
  };
  cv.addEventListener('pointermove', move); cv.addEventListener('pointerdown', move);
}
['swrPlot', 'rlPlot', 'zPlot', 's21Plot'].forEach((id) => attachHover($(id)));
$('smith').addEventListener('pointermove', (e) => {
  const sm = $('smith')._smith; if (!S.res || !sm) return;
  const rect = e.currentTarget.getBoundingClientRect();
  const gx = (e.clientX - rect.left - sm.cx) / sm.R, gy = -(e.clientY - rect.top - sm.cy) / sm.R;
  let best = -1, bd = 0.08 * 0.08;
  S.res.s11.forEach((g, i) => { const dd = (g[0] - gx) ** 2 + (g[1] - gy) ** 2; if (dd < bd) { bd = dd; best = i; } });
  if (best >= 0) { S.cursorIdx = best; drawAll(); }
});
$('swrPlot').addEventListener('keydown', (e) => {
  if (!S.res) return;
  const step = e.shiftKey ? 10 : 1;
  if (e.key === 'ArrowLeft') S.cursorIdx = Math.max(0, (S.cursorIdx || 0) - step);
  else if (e.key === 'ArrowRight') S.cursorIdx = Math.min(S.res.freq.length - 1, (S.cursorIdx || 0) + step);
  else return;
  e.preventDefault(); drawAll();
});
$('btnBest').onclick = () => { if (S.res) { S.cursorIdx = S.res.summary.minSWRIndex; drawAll(); } };

function selectTab(t) {
  S.tab = t;
  for (const b of document.querySelectorAll('.tabs button')) b.setAttribute('aria-selected', String(b.dataset.tab === t));
  for (const c of document.querySelectorAll('.tabbody canvas')) c.hidden = c.dataset.pane !== t;
  drawAll();
}
for (const b of document.querySelectorAll('.tabs button')) b.onclick = () => selectTab(b.dataset.tab);

// ---------- start ----------
selectTab('smith');
renderVerdict();
connectEvents();
run(loadSnaps);
