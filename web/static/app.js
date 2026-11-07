'use strict';
// vnaview browser UI: vanilla JS + canvas, no external dependencies.

const $ = (id) => document.getElementById(id);
const S = {
  state: null, res: null, ref: null, refKey: '', refName: '',
  cursorIdx: null, tab: 'smith', swrMaxUser: null, snapshots: [], lastCfgKey: '',
  hold: { version: -1, traces: {}, highlight: 0, lastCaptured: 0 },
  calOpen: false, trim: null, trimList: [], tone: null, cmpName: 'B', cmpAName: 'A',
};
const CUSTOM_SLOT = 2; // matches the PiX firmware's custom profile slot
const LORA_TARGETS = [433.92e6, 869e6, 915e6];

// ---------- helpers ----------
const mhz = (f, d = 2) => (f / 1e6).toFixed(d);
const mhzR = (f) => String(+(f / 1e6).toFixed(3));
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
function mlFromSWR(s) { const g = (s - 1) / (s + 1); return g >= 1 ? 60 : -10 * Math.log10(1 - g * g); }
function grade(swr) { return swr <= 0 ? 'na' : swr < 1.5 ? 'good' : swr < 2 ? 'ok' : 'bad'; }
const hhmm = (t) => new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });

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
function toast(msg, opts = {}) {
  const t = $('toast');
  $('toastText').textContent = msg;
  t.className = 'toast' + (opts.err ? ' err' : '');
  const u = $('toastUndo');
  u.hidden = !opts.undo;
  u.onclick = opts.undo ? async () => { t.hidden = true; await run(opts.undo); } : null;
  t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, opts.undo ? 9000 : opts.err ? 7000 : 2800);
}
async function run(fn) { try { return await fn(); } catch (e) { toast(e.message, { err: true }); } }
function store(k, v) { try { if (v === undefined) return localStorage.getItem(k); localStorage.setItem(k, v); } catch { return null; } }

// ask shows what an action will do and resolves true on OK.
function ask(title, text, ok = 'OK') {
  return new Promise((resolve) => {
    $('askTitle').textContent = title; $('askText').textContent = text; $('askYes').textContent = ok;
    const v = $('askView'); v.hidden = false; $('askYes').focus();
    const done = (x) => { v.hidden = true; resolve(x); };
    $('askYes').onclick = () => done(true); $('askNo').onclick = () => done(false);
  });
}

// ---------- theme ----------
function applyTheme(t) { if (t) document.documentElement.dataset.theme = t; }
applyTheme(store('vnaview-theme'));
$('btnTheme').onclick = () => {
  const cur = document.documentElement.dataset.theme || (matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
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
const STEP_HELP = {
  open: 'Fit the OPEN standard at the end of the pigtail (where the antenna goes), then tap OPEN. OPEN starts a new calibration.',
  short: 'Fit the SHORT, then tap SHORT.',
  load: 'Fit the 50 Ω LOAD, then tap LOAD.',
  done: 'Tap DONE to compute and apply the correction.',
  save: 'Tap SAVE to store it in this profile\'s slot.',
};
function currentProfile() {
  const st = S.state; if (!st) return null;
  return st.profiles.find((p) => p.id === st.config.profile) || null;
}

function renderState() {
  const st = S.state; if (!st) return;
  const pill = $('devpill');
  pill.classList.toggle('on', st.connected);
  if (st.connected && st.info) {
    const pix = st.info.fork && /^pix/i.test(st.info.fork);
    $('devtext').textContent = `${st.info.model} · fw ${st.info.version}${pix ? ' · PiX fork' : ''}${st.info.mock ? ' · MOCK' : ''}`;
  } else $('devtext').textContent = 'not connected';
  pill.title = st.info ? `${st.info.port}, ${st.info.maxPoints} points/scan${st.info.fork ? ', fork: ' + st.info.fork : ''}` : (st.error || '');
  $('ropill').hidden = !st.readOnly;
  const cal = st.cal.status, cp = $('calpill');
  cp.className = 'pill ' + (!st.connected || !cal.known ? '' : cal.applied ? 'calon' : 'caloff');
  cp.textContent = !st.connected ? 'CAL –' : !cal.known ? 'CAL ?' : cal.applied ? 'CAL ON' : 'CAL OFF';
  cp.title = 'Firmware reports: ' + (cal.raw || '(nothing)');
  showBanner(!st.connected && st.error ? 'Device: ' + st.error : '');

  renderProfiles(); renderCal(); renderTiles(); renderAdvanced(); renderDevice();
  $('tabS21').hidden = !st.config.s21;
  if (!st.config.s21 && S.tab === 's21') selectTab('smith');
  const key = st.config.profile + st.config.start + st.config.stop;
  if (key !== S.lastCfgKey && !st.tune.active) { S.lastCfgKey = key; S.swrMaxUser = null; }
  syncRef();
  if (!$('tuneView').hidden) renderTune();
  if (!st.tune.active && !$('tuneView').hidden && Date.now() - (S.tuneOpenedAt || 0) > 2000) closeTuneUI();
  if (!$('cmpView').hidden) renderCompare();
  updateScaleButtons();
}

function renderProfiles() {
  const st = S.state, box = $('profiles');
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
      <span class="calnote ${saved ? 'ok' : ''}">${saved ? '✓ cal in slot ' + p.calSlot : 'slot ' + p.calSlot + ': no cal yet'}</span>`;
    b.onclick = () => selectProfile(p.id);
    box.appendChild(b);
  }
}
async function selectProfile(id) {
  await run(async () => {
    const r = await api('POST', 'api/profile', { id });
    if (r.message) toast(r.message);
    S.hold.key = '';
    if (!r.state.continuous && r.state.connected) await api('POST', 'api/sweep');
  });
}

// ---------- calibration strip ----------
function renderCal() {
  const st = S.state, c = st.cal, cfg = st.config, p = currentProfile();
  const name = p ? (p.identify ? p.label : p.label + ' MHz') + ' profile' : 'custom range';
  const a = c.active;
  const good = st.connected && a && c.matches && c.status.applied;
  const flow = c.flow || { steps: {} };
  const flowCurrent = flow.profile === cfg.profile && flow.start === cfg.start && flow.stop === cfg.stop;
  const steps = flowCurrent ? flow.steps || {} : {};
  const inFlow = Object.keys(steps).length > 0 && steps.save !== 'done';
  const open = !st.readOnly && st.connected && (S.calOpen || !good || inFlow);
  const strip = $('calstrip');
  strip.classList.toggle('ok', !!good);
  strip.classList.toggle('need', st.connected && !good && !st.readOnly);
  let sum;
  if (!st.connected) sum = '<span class="muted">Calibration: connect a NanoVNA.</span>';
  else if (good) sum = `<b style="color:var(--good-ink)">✓ Calibrated</b> for the ${esc(name)} <span class="muted">(${esc(a.source)}, ${mhzR(a.start)}–${mhzR(a.stop)} MHz)</span>`;
  else if (a) sum = `<b style="color:var(--warn-ink)">! Calibrate the ${esc(name)}</b>: the active calibration is for ${mhzR(a.start)}–${mhzR(a.stop)} MHz. Each profile needs its own.`;
  else sum = c.status.applied ? `<b style="color:var(--warn-ink)">! Calibration range unknown</b> (loaded by the firmware). Calibrate the ${esc(name)} to be sure.`
    : `<b style="color:var(--bad-ink)">! Not calibrated</b>. Calibrate the ${esc(name)}.`;
  if (st.readOnly) sum += ' <span class="muted">· read-only: calibration disabled</span>';
  $('calsummary').innerHTML = sum;
  const tg = $('btnCalToggle');
  tg.hidden = !good || st.readOnly || inFlow;
  tg.textContent = S.calOpen ? 'Hide' : 'Recalibrate';
  $('calbody').hidden = !open;

  const busy = !!st.busy;
  const next = STEPS.find((s) => steps[s] !== 'done');
  for (const btn of document.querySelectorAll('.calstep')) {
    const s = btn.dataset.step;
    btn.classList.remove('done', 'error', 'running', 'next');
    if (steps[s]) btn.classList.add(steps[s]);
    if (s === next && !steps[s]) btn.classList.add('next');
    btn.disabled = busy || !st.connected ||
      (s === 'done' && !(steps.open === 'done' && steps.short === 'done' && steps.load === 'done')) ||
      (s === 'save' && steps.done !== 'done' && !(c.active && c.matches));
  }
  const sel = $('slotSel'), n = (st.info && st.info.calSlots) || 7;
  if (sel.options.length !== n) { sel.innerHTML = ''; for (let i = 0; i < n; i++) sel.add(new Option(String(i), String(i))); }
  if (document.activeElement !== sel) sel.value = String(p ? p.calSlot : CUSTOM_SLOT);
  $('slotlbl').textContent = '→ ' + sel.value;
  $('btnRecall').disabled = busy || !st.connected;
  $('autoRecall').checked = !!c.settings.autoRecall;
  let msg;
  const running = Object.keys(steps).find((k) => steps[k] === 'running');
  if (running) msg = `Measuring ${running.toUpperCase()}…`;
  else if (flowCurrent && flow.message && /fail|did not/.test(flow.message)) msg = `<span class="bad">${esc(flow.message)}</span>`;
  else if (next) msg = (flowCurrent && flow.message ? esc(flow.message) + '. ' : '') + STEP_HELP[next];
  else msg = 'Calibration complete and saved.';
  $('calmsg').innerHTML = msg + ` <span class="muted small">Firmware: ${esc(c.status.raw || 'no cal data')}</span>`;
}
$('btnCalToggle').onclick = () => { S.calOpen = !S.calOpen; renderCal(); };
for (const btn of document.querySelectorAll('.calstep')) {
  btn.onclick = () => run(async () => {
    const s = btn.dataset.step;
    if (s === 'save') {
      const slot = $('slotSel').value;
      const dp = S.state.deviceProfiles;
      if (!(await ask(`Save to slot ${slot}?`, `This overwrites whatever calibration is stored in slot ${slot} on the NanoVNA${dp ? ' (the device profile using this slot will load it)' : ''}. It cannot be undone from here.`, `Save to slot ${slot}`))) return;
      await api('POST', 'api/cal/save', { slot: Number(slot) });
      toast('Calibration saved to slot ' + slot); S.calOpen = false;
      return;
    }
    if (s === 'open' && S.state.cal.status.applied &&
        !(await ask('Start a new calibration?', 'OPEN resets the calibration in the NanoVNA\'s memory (the saved slots are untouched until you tap SAVE).', 'Reset and measure OPEN'))) return;
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
  const slot = $('slotSel').value;
  if (!(await ask(`Recall slot ${slot}?`, `Loads slot ${slot} into the NanoVNA (replaces the calibration in use, nothing is saved).`, 'Recall'))) return;
  await api('POST', 'api/cal/recall', { slot: Number(slot) });
  toast('Recalled slot ' + slot);
});
$('autoRecall').onchange = () => run(() => api('POST', 'api/settings', { autoRecall: $('autoRecall').checked }));

// ---------- action tiles ----------
function tile(id, title, sub, cls) {
  const t = $(id);
  t.querySelector('b').innerHTML = title;
  t.querySelector('span').textContent = sub;
  t.classList.remove('on', 'set');
  if (cls) t.classList.add(cls);
}
function renderTiles() {
  const st = S.state, prof = st.config.profile;
  $('aSweep').disabled = !!st.busy;
  tile('aLive', '<i class="led"></i>LIVE', st.continuous ? 'sweeping · tap to stop' : 'off · tap to start', st.continuous ? 'on' : '');
  $('aLive').setAttribute('aria-pressed', String(st.continuous));
  const tgt = defaultTarget();
  tile('aTune', 'TUNE', st.tune.active ? 'open · ' + mhzR(st.tune.target) + ' MHz' : tgt ? 'to ' + mhzR(tgt) + ' MHz' : 'trim to target', st.tune.active ? 'on' : '');
  const ref = st.refs[prof];
  let refSub = 'tap: store this trace';
  if (ref) {
    const d = S.res && S.res.ref;
    refSub = `set ${hhmm(ref.time)}` + (d && d.df != null ? ` · Δf ${sgn(d.df / 1e6, 2)} MHz` : '') + ' · tap: replace';
  }
  tile('aRef', ref ? 'REF ✓' : 'REF', refSub, ref ? 'set' : '');
  $('aRefClear').hidden = !ref;
  const hv = st.hold, hl = S.res && S.res.hold;
  let hsub = 'stable positions · off';
  if (hv.on) {
    if (!hl) hsub = 'on · waiting for a sweep';
    else if (hl.status === 'moving') hsub = `moving… · ${hl.positions} position${hl.positions === 1 ? '' : 's'}`;
    else if (hl.status === 'settling' || hl.status === 'waiting') hsub = `settling ${hl.run}/${hl.need} · keep still`;
    else hsub = `${hl.positions} position${hl.positions === 1 ? '' : 's'}` + (hl.positions > 1 ? ` · Δres ${(hl.resSpread / 1e6).toFixed(1)} MHz` : '');
  }
  tile('aHold', 'HOLD', hsub, hv.on ? 'on' : '');
  $('aHold').setAttribute('aria-pressed', String(hv.on));
  renderHoldPanel();
  const avg = st.config.avg || 1;
  tile('aAvg', `AVG ×${avg}`, avg > 1 ? `averaging ${avg} · tap: ×${nextAvg(avg)}` : 'off · tap: ×2', avg > 1 ? 'on' : '');
  const c = st.compare;
  tile('aCompare', 'COMPARE', c.hasA ? `A: ${c.aName} · ${Math.max(0, c.leaderboard.length - 1)} tested` : 'S21 A vs B', c.active ? 'on' : c.hasA ? 'set' : '');
  tile('aReport', 'REPORT', st.reportChoices.length ? `${st.reportChoices.length} profile sweep${st.reportChoices.length > 1 ? 's' : ''} ready` : 'sweep first', '');
  const hint = $('hint');
  hint.innerHTML = ref
    ? '<b>Detuning test:</b> REF in free air, then hold the antenna or put it in its enclosure and read <b>Δf</b> below.'
    : '<b>Tip:</b> REF stores this trace as a dashed overlay. Use it before/after a trim, or for a detuning test (free air → hand/enclosure).';
}
const AVGS = [1, 2, 4, 8, 16];
const nextAvg = (n) => AVGS[(AVGS.indexOf(n) + 1) % AVGS.length];
$('aSweep').onclick = () => run(() => api('POST', 'api/sweep'));
$('aLive').onclick = () => run(() => api('POST', 'api/continuous', { on: !(S.state && S.state.continuous) }));
$('aAvg').onclick = () => run(() => api('POST', 'api/avg', { n: nextAvg((S.state && S.state.config.avg) || 1) }));
$('aHold').onclick = () => run(async () => {
  const on = !S.state.hold.on;
  if (!on && S.state.hold.positions > 0 &&
      !(await ask('Turn HOLD off?', `The ${S.state.hold.positions} captured position(s) are discarded.`, 'Turn off'))) return;
  const r = await api('POST', 'api/hold', { on });
  S.hold.highlight = 0; S.hold.traces = {};
  toast(r.message);
  if (on && !r.state.continuous) await api('POST', 'api/continuous', { on: true });
});
$('aRef').onclick = () => run(async () => {
  const r = await api('POST', 'api/ref');
  toast(r.message, { undo: async () => { await api('POST', 'api/ref/undo'); toast('REF restored'); } });
});
$('aRefClear').onclick = () => run(async () => {
  await api('DELETE', 'api/ref');
  toast('REF cleared', { undo: async () => { await api('POST', 'api/ref/undo'); toast('REF restored'); } });
});
$('aTune').onclick = () => openTune();
$('aCompare').onclick = () => openCompare();
$('aReport').onclick = () => openReport();
$('btnConnect').onclick = () => run(async () => { await api('POST', 'api/connect'); toast('Connected'); });

// REF overlay follows the current profile's stored REF.
async function syncRef() {
  const st = S.state, ref = st.refs[st.config.profile];
  const key = ref ? st.config.profile + ref.time : '';
  if (key === S.refKey) return;
  S.refKey = key;
  if (!ref) { S.ref = null; S.refName = ''; drawAll(); return; }
  try { S.ref = normalize(await api('GET', 'api/ref?profile=' + encodeURIComponent(st.config.profile))); S.refName = 'REF ' + hhmm(ref.time); } catch { S.ref = null; }
  drawAll();
}

// ---------- HOLD: stable positions (motion-rejecting envelope, server side) ----------
function onHoldResult(r) {
  const h = r.hold; if (!h) return;
  if (h.captured && h.captured !== S.hold.lastCaptured) {
    S.hold.lastCaptured = h.captured;
    toast(`position ${h.captured} captured` + (h.positions > 1 ? ` · resonance spread ${(h.resSpread / 1e6).toFixed(1)} MHz` : ''));
  }
  if (h.version !== S.hold.version) {
    S.hold.version = h.version;
    if (S.hold.highlight && !h.list.some((p) => p.id === S.hold.highlight)) S.hold.highlight = 0;
    if (S.hold.highlight) loadHoldTraces();
  }
}
async function loadHoldTraces() {
  try {
    const r = await api('GET', 'api/hold');
    S.hold.traces = {}; for (const p of r.positions) S.hold.traces[p.id] = p.swr;
  } catch { S.hold.traces = {}; }
  drawAll();
}
function renderHoldPanel() {
  const st = S.state, box = $('holdPanel');
  box.hidden = !st.hold.on;
  if (box.hidden) return;
  const h = S.res && S.res.hold;
  const n = h ? h.positions : 0;
  const status = !h ? 'waiting for a sweep' : h.status === 'moving' ? '<b class="mv">moving…</b> (sweep rejected)'
    : h.status === 'settling' || h.status === 'waiting' ? `settling ${h.run}/${h.need}: keep it still`
    : h.status === 'captured' ? `<b class="ok">position ${h.captured} captured</b>`
    : h.status === 'duplicate' ? `steady, same as position ${h.sameAs}` : h.status === 'full' ? 'position list full: clear some'
    : `steady at position ${h.sameAs || n}: now bend or move it to the next position`;
  const key = JSON.stringify([h && h.version, n, S.hold.highlight, st.hold.n, st.hold.sensitivity]);
  if (key === S.hold.panelKey && $('holdStatus')) { $('holdStatus').innerHTML = status; return; }
  S.hold.panelKey = key;
  const seg = (key, vals, cur) => vals.map((v) => `<button type="button" data-${key}="${v}" aria-pressed="${String(v) === String(cur)}">${v}</button>`).join('');
  const chips = h && h.list.length ? h.list.map((p) => `<span class="pchip ${S.hold.highlight === p.id ? 'hl' : ''}" data-id="${p.id}">
      <button type="button" class="pmain" data-id="${p.id}" title="Highlight on the plot">#${p.id} <b>${mhz(p.resFreq)}</b>${p.bandWorst ? ` · worst ${swrText(p.bandWorst)}` : ` · SWR ${swrText(p.minSWR)}`}</button>
      <button type="button" class="px" data-rm="${p.id}" aria-label="Remove position ${p.id}">×</button></span>`).join('') : '<span class="muted">No positions yet.</span>';
  box.innerHTML = `<div class="holdgrid">
    <div class="hstat"><div class="tl">Positions</div><div class="hv">${n}</div></div>
    <div class="hstat"><div class="tl">Resonance spread</div><div class="hv">${n > 1 ? (h.resSpread / 1e6).toFixed(1) + '<small> MHz</small>' : '–'}</div>
      <div class="small muted">${n ? `${mhz(h.resMin)}–${mhz(h.resMax)} MHz` : ''}</div></div>
    <div class="hstat ${n && h.worstBand ? grade(h.worstBand) : ''}"><div class="tl">Worst in-band SWR</div><div class="hv">${n && h.worstBand ? swrText(h.worstBand) : '–'}</div>
      <div class="small muted">${n && h.worstBandId ? 'position #' + h.worstBandId : ''}</div></div>
    <div class="hctl"><div class="hstatus" id="holdStatus">${status}</div>
      <div class="row small">steady sweeps <span class="seg" id="holdN">${seg('n', [2, 3, 4, 5], st.hold.n)}</span>
      motion <span class="seg" id="holdSens">${seg('s', ['low', 'normal', 'high'], st.hold.sensitivity)}</span>
      <button type="button" class="ghost small" id="holdClear" ${n ? '' : 'disabled'}>Clear</button></div></div>
  </div>
  <div class="pchips">${chips}</div>
  <div class="small muted">Bend or move the antenna, hold it still for a moment, repeat. Only steady positions count. Sweeps taken while it moves are rejected.</div>`;
  for (const b of box.querySelectorAll('[data-n]')) b.onclick = () => run(() => api('POST', 'api/hold', { n: Number(b.dataset.n) }));
  for (const b of box.querySelectorAll('[data-s]')) b.onclick = () => run(() => api('POST', 'api/hold', { sensitivity: b.dataset.s }));
  for (const b of box.querySelectorAll('.pmain')) b.onclick = () => { const id = Number(b.dataset.id); S.hold.highlight = S.hold.highlight === id ? 0 : id; if (S.hold.highlight && !S.hold.traces[id]) loadHoldTraces(); renderHoldPanel(); drawAll(); };
  for (const b of box.querySelectorAll('[data-rm]')) b.onclick = () => run(async () => {
    const r = await api('POST', 'api/hold', { remove: Number(b.dataset.rm) });
    toast(r.message, { undo: async () => { await api('POST', 'api/hold', { undo: true }); } });
  });
  const cl = $('holdClear');
  if (cl) cl.onclick = () => run(async () => {
    const r = await api('POST', 'api/hold', { clear: true });
    toast(r.message, { undo: async () => { await api('POST', 'api/hold', { undo: true }); } });
  });
}

// ---------- advanced ----------
function renderAdvanced() {
  const st = S.state, cfg = st.config;
  const dc = st.deviceProfiles && st.deviceProfiles.custom;
  const meta = st.cal.settings.slots && st.cal.settings.slots[CUSTOM_SLOT];
  const src = dc && dc.cal ? dc : (!st.deviceProfiles && meta ? meta : null);
  const cb = $('btnCustom');
  cb.hidden = !src;
  if (src) cb.textContent = `Load saved custom range ${mhzR(src.start)}–${mhzR(src.stop)} MHz (slot ${CUSTOM_SLOT})`;
  if ($('advForm').contains(document.activeElement) || st.tune.active) return;
  $('advStart').value = String(cfg.start / 1e6);
  $('advStop').value = String(cfg.stop / 1e6);
  const ps = $('advPoints');
  if (!ps.options.length) for (const n of st.pointChoices) ps.add(new Option(String(n), String(n)));
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

function renderDevice() {
  const st = S.state, cal = st.cal.status;
  const mb = $('mockbox');
  mb.hidden = !(st.mockScenarios && st.mockScenarios.length);
  if (!mb.hidden) {
    const box = $('mockBtns');
    if (box.childElementCount !== st.mockScenarios.length) {
      box.innerHTML = '';
      for (const n of st.mockScenarios) {
        const b = document.createElement('button'); b.type = 'button'; b.className = 'ghost small'; b.textContent = n; b.dataset.n = n;
        b.onclick = () => run(async () => { await api('POST', 'api/mock/antenna', { name: n }); if (!S.state.continuous) await api('POST', 'api/sweep'); });
        box.appendChild(b);
      }
    }
    for (const b of box.children) b.setAttribute('aria-pressed', String(b.dataset.n === st.mockAntenna));
  }
  $('devinfo').textContent = st.info
    ? [`Driver: ${st.info.driver}`, `Port: ${st.info.port}`, `Max points per scan: ${st.info.maxPoints}`,
       `Calibration slots: ${st.info.calSlots}`, `Firmware cal status: ${cal.raw || '(none)'}`,
       st.info.fork ? `Fork: ${st.info.fork}` : '', st.readOnly ? 'READ-ONLY mode: cal/save/recall/profile switching disabled' : '',
       st.deviceProfiles ? 'Device profiles (firmware `profile`):\n' + [...st.deviceProfiles.profiles, ...(st.deviceProfiles.custom ? [st.deviceProfiles.custom] : [])]
         .map((d) => `  ${d.name.padEnd(6)} ${mhzR(d.start)}–${mhzR(d.stop)} MHz, ${d.points} pts, slot ${d.slot}${d.cal ? ', cal saved' : ''}${d.active ? ' [active]' : ''}`).join('\n') +
         (st.deviceProfiles.boot ? `\n  boot profile: ${st.deviceProfiles.boot.name}${st.deviceProfiles.boot.ask ? ' (asks)' : ''}` : '')
         : 'Device profiles: not supported by this firmware (using sweep + recall)',
       `Data dir: ${st.dataDir}`, '', ...(st.info.raw || [])].filter((l) => l !== '').join('\n')
    : (st.error || 'Not connected.') + `\nData dir: ${st.dataDir}`;
}

// ---------- snapshots ----------
async function loadSnaps() { S.snapshots = await api('GET', 'api/snapshots'); renderSnaps(); }
function renderSnaps() {
  const tb = $('snapList'); tb.innerHTML = '';
  if (!S.snapshots.length) { tb.innerHTML = '<tr><td colspan="5" class="muted">No snapshots yet. For a quick before/after use REF instead.</td></tr>'; return; }
  for (const s of S.snapshots) {
    const tr = document.createElement('tr'), q = encodeURIComponent(s.name);
    tr.innerHTML = `<td>${esc(s.name)}</td><td>${new Date(s.time).toLocaleString()}</td><td>${esc(s.profile)}</td><td>${s.points}</td>
      <td class="act"><a class="btn ghost small" href="api/export?format=s1p&snapshot=${q}" download>.s1p</a>
      <a class="btn ghost small" href="api/export?format=csv&snapshot=${q}" download>CSV</a>
      <button class="ghost small" data-a="del" type="button">Delete</button></td>`;
    tr.querySelector('[data-a=del]').onclick = () => run(async () => {
      if (!(await ask(`Delete snapshot "${s.name}"?`, 'The file is removed from the data directory. This cannot be undone.', 'Delete'))) return;
      await api('DELETE', 'api/snapshots/' + q);
    });
    tb.appendChild(tr);
  }
}
$('snapForm').onsubmit = (e) => {
  e.preventDefault();
  run(async () => {
    const name = $('snapName').value.trim();
    if (S.snapshots.some((x) => x.name === name) && !(await ask(`Replace "${name}"?`, 'A snapshot with this name exists and will be overwritten.', 'Replace'))) return;
    const r = await api('POST', 'api/snapshots', { name });
    toast('Saved snapshot ' + r.name); $('snapName').value = '';
  });
};

// ---------- results ----------
function normalize(r) {
  r.summary = r.summary || {};
  r.summary.dips = r.summary.dips || [];
  r.summary.bands = r.summary.bands || [];
  r.summary.warnings = r.summary.warnings || [];
  r.config.bands = r.config.bands || [];
  return r;
}
function setResult(r) {
  normalize(r);
  const tuneRes = !!r.tune;
  const first = !S.res || S.res.config.profile !== r.config.profile || S.res.freq.length !== r.freq.length || !!S.res.tune !== tuneRes;
  S.res = r;
  onHoldResult(r);
  if (first || S.cursorIdx == null || S.cursorIdx >= r.freq.length) S.cursorIdx = r.summary.minSWRIndex;
  $('expS2p').hidden = !(r.s21 && r.s21.length);
  renderWarnings(); renderVerdict(); renderTiles();
  if (!$('tuneView').hidden) renderTune();
  if (!$('cmpView').hidden) renderCompare();
  drawAll();
}

function renderWarnings() {
  const r = S.res, box = $('warnings');
  box.innerHTML = (r ? r.summary.warnings : []).map((w) =>
    `<div class="wbanner ${w.level}"><span class="wi">${w.level === 'bad' ? '!' : '?'}</span><div><b>${esc(w.title)}</b>${esc(w.text)}</div></div>`).join('');
}

const VICON = { good: '✓', ok: '!', bad: '✕', 'n/a': '–' };
const VLABEL = { good: 'GOOD', ok: 'OK', bad: 'BAD', 'n/a': 'N/A' };
function badge(v) {
  if (suspect()) return '<span class="badge na" title="See the warning at the top: this verdict is not trustworthy">? CHECK</span>';
  const c = v === 'n/a' ? 'na' : v; return `<span class="badge ${c}">${VICON[v] || ''} ${VLABEL[v] || v}</span>`;
}
// suspect is true while an automatic warning says the measurement is not an antenna (or not calibrated).
function suspect() { return !!(S.res && !S.res.tune && S.res.summary.warnings.length); }
function bandTitle(b) { return ['433', '869', '915'].includes(b.name) ? `${b.name} MHz band` : `${b.name} MHz sub-band`; }

function renderVerdict() {
  const r = S.res, box = $('verdict');
  if (!r) { box.innerHTML = '<div class="empty">Pick a profile above. Each tap runs one sweep.</div>'; return; }
  if (r.tune) return; // TUNE results do not replace the profile verdict
  const sm = r.summary;
  let html;
  if (r.config.identify) html = identifyCard(r);
  else if (!sm.bands.length) html = `<div class="vcard na wide"><div class="vtitle"><h3>Custom sweep</h3><span class="range">${mhzR(r.freq[0])}–${mhzR(r.freq[r.freq.length - 1])} MHz</span></div>
      <div class="vmain"><div class="stat"><div class="v">${swrText(sm.minSWR)}</div><div class="l">best SWR @ ${mhz(sm.minSWRFreq)} MHz</div></div>
      <div class="stat"><div class="v">${sm.mlAtMin.toFixed(2)}<small> dB</small></div><div class="l">mismatch loss</div></div></div>${dipTable(sm.dips)}</div>`;
  else html = sm.bands.map((b, i) => bandCard(r, b, i === 0)).join('');
  box.innerHTML = refRow(r) + html;
  for (const tr of document.querySelectorAll('.dips tr.click')) tr.onclick = () => { S.cursorIdx = Number(tr.dataset.idx); drawAll(); };
}

function refRow(r) {
  const d = r.ref; if (!d) return '';
  const parts = [`<span class="muted">vs REF ${hhmm(d.refTime)}</span>`];
  if (d.df != null) parts.push(`<span>Δf <span class="big">${sgn(d.df / 1e6, 2)}</span> MHz <span class="muted">(${mhz(d.refDipFreq)} → ${mhz(d.dipFreq)})</span></span>`);
  parts.push(`<span>ΔSWR @ ${mhzR(d.at)} MHz <b>${sgn(d.dswr, 2)}</b> <span class="muted">(${swrText(d.refSWR)} → ${swrText(d.swr)})</span></span>`);
  if (d.dBandWorst != null) parts.push(`<span>band worst <b>${sgn(d.dBandWorst, 2)}</b></span>`);
  return `<div class="refrow">${parts.join('')}</div>`;
}

function edgeHint(r) {
  const sm = r.summary, n = r.freq.length;
  if (sm.minSWRIndex === 0) return `Lowest SWR is at the low edge (${mhz(r.freq[0], 1)} MHz): the resonance is below this range. Try Wide or Survey.`;
  if (sm.minSWRIndex === n - 1) return `Lowest SWR is at the high edge (${mhz(r.freq[n - 1], 1)} MHz): the resonance is above this range. Try Wide or Survey.`;
  return 'No clear resonance dip in this sweep.';
}
function bandCard(r, b, primary) {
  const band = b.band, v = suspect() || b.verdict === 'n/a' ? 'na' : b.verdict, ref = band.ref || (band.lo + band.hi) / 2;
  let res;
  if (b.dipFreq != null) {
    const pos = b.dipEdgeOffset === 0 ? 'inside the band' : `${Math.abs(b.dipEdgeOffset / 1e6).toFixed(1)} MHz ${b.dipEdgeOffset < 0 ? 'below' : 'above'} the band`;
    const q = primary && r.summary.q ? ` · Q ≈ ${r.summary.q.toFixed(0)}` : '';
    res = `Resonance <b>${mhz(b.dipFreq)} MHz</b> (SWR ${swrText(b.dipSWR)}): ${pos}, ${sgn(b.dipOffset / 1e6)} MHz from ${band.ref ? 'reference' : 'centre'} ${mhzR(ref)} MHz${q}.`;
  } else res = edgeHint(r);
  const cov = b.covered ? '' : '<div class="vline" style="color:var(--warn-ink)">! The sweep does not cover the whole band.</div>';
  const advice = primary && b.dipAdvice && b.dipEdgeOffset !== 0 ? `<div class="advice">${esc(b.dipAdvice)}</div>` : '';
  return `<div class="vcard ${v} wide ${primary ? '' : 'compact'}">
    <div class="vtitle"><h3>${bandTitle(band)}</h3><span class="range">${mhz(band.lo, 2)}–${mhz(band.hi, 2)} MHz</span>${badge(b.verdict)}</div>
    <div class="vmain">
      <div class="stat"><div class="v">${swrText(b.worstSWR)}</div><div class="l">worst SWR in band @ ${mhz(b.worstSWRFreq)} MHz</div></div>
      <div class="stat"><div class="v">${b.worstML.toFixed(2)}<small> dB</small></div><div class="l">mismatch loss at worst</div></div>
      <div class="stat"><div class="v">${b.worstRL.toFixed(1)}<small> dB</small></div><div class="l">return loss, worst</div></div>
      <div class="stat"><div class="v">${swrText(b.bestSWR)}</div><div class="l">best SWR in band</div></div>
      ${primary ? `<div class="stat"><div class="v">${b.pctBelow2.toFixed(0)}<small> %</small></div><div class="l">of band with SWR &lt; 2</div></div>` : ''}
    </div>
    <div class="vline">${res}</div>${cov}${advice}${suspect() && primary ? '<div class="vline" style="color:var(--warn-ink)"><b>! Not trustworthy:</b> see the warning at the top of the page.</div>' : ''}</div>`;
}
function dipTable(dips) {
  if (!dips.length) return '<div class="vline">No clear resonance (needs ≥ 3 dB return loss and 3 dB prominence).</div>';
  const rows = dips.map((d, i) => `<tr class="click" data-idx="${d.index}">
    <td>${i + 1}</td><td><b>${mhz(d.freq)}</b></td><td>${swrText(d.swr)}</td><td>${d.ml.toFixed(2)} dB</td><td class="hide-sm">${d.rl.toFixed(1)} dB</td>
    <td class="hide-sm">${fmtZ(d.r, d.x)}</td><td class="hide-sm">${d.bw2.found ? (d.bw2.width / 1e6).toFixed(1) + ' MHz' : '–'}</td><td class="hide-sm">${d.q ? d.q.toFixed(0) : '–'}</td>
    <td>${d.inBand ? `<span style="color:var(--good-ink)">✓ in ${esc(d.band)} band.</span> ` : ''}${esc(d.inBand ? d.advice.replace(/^In the .*? band\s*\([^)]*\)\.\s*/, '') : d.advice)}</td></tr>`).join('');
  return `<table class="dips"><thead><tr><th>#</th><th>MHz</th><th>SWR</th><th>ML</th><th class="hide-sm">RL</th><th class="hide-sm">Z</th><th class="hide-sm">BW SWR&lt;2</th><th class="hide-sm">Q</th><th>Nearest LoRa band</th></tr></thead><tbody>${rows}</tbody></table>`;
}
function identifyCard(r) {
  const sm = r.summary, nd = sm.dips.length;
  const chips = sm.bands.map((b) => `<span class="chip">${badge(b.verdict)} ${esc(b.band.name)} MHz: worst SWR ${swrText(b.worstSWR)}</span>`).join('');
  return `<div class="vcard na wide">
    <div class="vtitle"><h3>Antenna identification</h3><span class="range">${mhzR(r.freq[0])}–${mhzR(r.freq[r.freq.length - 1])} MHz</span></div>
    <div class="vmain"><div class="stat"><div class="v">${nd}</div><div class="l">resonance${nd === 1 ? '' : 's'} found</div></div>
    <div class="stat"><div class="v">${swrText(sm.minSWR)}</div><div class="l">best SWR @ ${mhz(sm.minSWRFreq)} MHz</div></div>
    <div class="stat"><div class="v">${sm.mlAtMin.toFixed(2)}<small> dB</small></div><div class="l">mismatch loss there</div></div></div>
    ${dipTable(sm.dips)}<div class="bandchips">${chips}</div>
    <div class="vline muted small">Trim hints assume a length-tuned monopole, dipole or whip: resonance below the band means the radiator is too long.</div></div>`;
}

// ---------- drawing ----------
function css(name) { return getComputedStyle(document.documentElement).getPropertyValue(name).trim(); }
function setup(cv) {
  const dpr = window.devicePixelRatio || 1, w = cv.clientWidth, h = cv.clientHeight;
  if (cv.width !== Math.round(w * dpr) || cv.height !== Math.round(h * dpr)) { cv.width = Math.round(w * dpr); cv.height = Math.round(h * dpr); }
  const ctx = cv.getContext('2d');
  ctx.setTransform(dpr, 0, 0, dpr, 0, 0); ctx.clearRect(0, 0, w, h);
  return { ctx, w, h };
}
function niceTicks(lo, hi, maxN) {
  const span = hi - lo; if (!(span > 0)) return [lo];
  const raw = span / Math.max(1, maxN), mag = Math.pow(10, Math.floor(Math.log10(raw)));
  const step = [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw) || 10 * mag;
  const out = []; for (let v = Math.ceil(lo / step) * step; v <= hi + step * 1e-9; v += step) out.push(+v.toFixed(10));
  return out;
}

// drawLine renders a frequency plot of result r (default S.res).
function drawLine(cv, o) {
  const { ctx, w, h } = setup(cv);
  const r = o.r || S.res;
  const C = { grid: css('--grid'), axis: css('--axis'), base: css('--baseline'), ink: css('--ink'), ink2: css('--ink2'), band: css('--band'), band2: css('--band2'), muted: css('--muted') };
  const pad = { l: 46, r: 14, t: 18, b: 26 };
  const x0 = pad.l, x1 = w - pad.r, y0 = pad.t, y1 = h - pad.b;
  ctx.font = '11px system-ui, sans-serif';
  if (!r) { ctx.fillStyle = C.muted; ctx.textAlign = 'center'; ctx.fillText('No sweep yet', w / 2, h / 2); cv._map = null; return; }
  const xmin = r.freq[0], xmax = r.freq[r.freq.length - 1];
  const fx = (f) => x0 + (f - xmin) / (xmax - xmin) * (x1 - x0);
  const fy = (v) => y1 - (v - o.ymin) / (o.ymax - o.ymin) * (y1 - y0);
  const clampY = (v) => fy(Math.max(o.ymin - (o.ymax - o.ymin), Math.min(o.ymax + (o.ymax - o.ymin), v)));
  for (const b of r.config.bands || []) {
    if (b.hi < xmin || b.lo > xmax) continue;
    const a = fx(Math.max(b.lo, xmin)), z = fx(Math.min(b.hi, xmax)), sub = b.hi - b.lo < 1e6;
    ctx.fillStyle = sub ? C.band2 : C.band; ctx.fillRect(a, y0, Math.max(1.5, z - a), y1 - y0);
    if (!sub && !o.noBandLabels) { ctx.fillStyle = C.ink2; ctx.textAlign = 'left'; ctx.fillText(b.name, a + 3, y0 - 5); }
  }
  ctx.strokeStyle = C.grid; ctx.lineWidth = 1; ctx.fillStyle = C.axis; ctx.textAlign = 'right'; ctx.textBaseline = 'middle';
  for (const t of o.yticks) {
    const y = Math.round(fy(t)) + 0.5; if (y < y0 - 1 || y > y1 + 1) continue;
    ctx.beginPath(); ctx.moveTo(x0, y); ctx.lineTo(x1, y); ctx.stroke(); ctx.fillText(o.yfmt(t), x0 - 6, y);
  }
  ctx.textAlign = 'center'; ctx.textBaseline = 'top';
  for (const t of niceTicks(xmin / 1e6, xmax / 1e6, Math.floor((x1 - x0) / 80))) {
    const x = Math.round(fx(t * 1e6)) + 0.5;
    ctx.beginPath(); ctx.moveTo(x, y0); ctx.lineTo(x, y1); ctx.stroke(); ctx.fillText(String(+t.toFixed(3)), x, y1 + 6);
  }
  ctx.textAlign = 'left'; ctx.fillText('MHz', 2, y1 + 6);
  ctx.strokeStyle = C.base; ctx.beginPath(); ctx.moveTo(x0, y1 + 0.5); ctx.lineTo(x1, y1 + 0.5); ctx.stroke();
  ctx.setLineDash([4, 4]); ctx.textBaseline = 'bottom'; ctx.textAlign = 'right';
  for (const hl of o.hlines || []) {
    const y = Math.round(fy(hl.y)) + 0.5; if (y < y0 || y > y1) continue;
    ctx.strokeStyle = C.axis; ctx.beginPath(); ctx.moveTo(x0, y); ctx.lineTo(x1, y); ctx.stroke();
    ctx.fillStyle = C.axis; ctx.fillText(hl.label, x1 - 2, y - 2);
  }
  ctx.setLineDash([]);
  if (o.vline) { // target marker
    const x = Math.round(fx(o.vline.f)) + 0.5;
    if (x >= x0 && x <= x1) {
      ctx.strokeStyle = css('--accent'); ctx.lineWidth = 2; ctx.setLineDash([6, 3]);
      ctx.beginPath(); ctx.moveTo(x, y0); ctx.lineTo(x, y1); ctx.stroke(); ctx.setLineDash([]);
      ctx.fillStyle = css('--accent'); ctx.textAlign = 'center'; ctx.textBaseline = 'bottom'; ctx.font = '600 11px system-ui, sans-serif';
      ctx.fillText(o.vline.label, Math.min(x1 - 30, Math.max(x0 + 30, x)), y0 - 3); ctx.font = '11px system-ui, sans-serif';
    }
  }
  ctx.save(); ctx.beginPath(); ctx.rect(x0, y0 - 1, x1 - x0, y1 - y0 + 2); ctx.clip();
  if (o.envelope) { // HOLD min/max
    const { min, max } = o.envelope;
    ctx.fillStyle = css('--band2'); ctx.beginPath();
    r.freq.forEach((f, i) => { const x = fx(f), y = clampY(max[i]); if (i) ctx.lineTo(x, y); else ctx.moveTo(x, y); });
    for (let i = r.freq.length - 1; i >= 0; i--) ctx.lineTo(fx(r.freq[i]), clampY(min[i]));
    ctx.closePath(); ctx.fill();
  }
  const plotSeries = (freq, ys, color, width, dash) => {
    ctx.strokeStyle = color; ctx.lineWidth = width; ctx.setLineDash(dash || []); ctx.lineJoin = 'round'; ctx.beginPath();
    let pen = false;
    for (let i = 0; i < freq.length; i++) {
      const v = ys[i]; if (v == null || !isFinite(v)) { pen = false; continue; }
      const x = fx(freq[i]), y = clampY(v);
      if (pen) ctx.lineTo(x, y); else { ctx.moveTo(x, y); pen = true; }
    }
    ctx.stroke(); ctx.setLineDash([]);
  };
  if (o.ref && S.ref) plotSeries(S.ref.freq, o.ref(S.ref), css('--series2'), 2, [6, 4]);
  for (const s of o.ys) plotSeries(r.freq, s.y, s.color, s.width || 2, s.dash);
  ctx.restore();
  if (o.dips) {
    ctx.textAlign = 'center'; ctx.textBaseline = 'top'; ctx.font = '600 11px system-ui, sans-serif';
    for (const d of r.summary.dips) {
      const x = fx(d.freq), lx = Math.min(x1 - 22, Math.max(x0 + 22, x));
      const t = mhz(d.freq, 1) + ' MHz', tw = ctx.measureText(t).width;
      ctx.fillStyle = css('--surface'); ctx.fillRect(lx - tw / 2 - 4, y0 + 3, tw + 8, 16);
      ctx.fillStyle = C.ink2; ctx.fillText(t, lx, y0 + 6);
      ctx.beginPath(); ctx.moveTo(x, y0 + 26); ctx.lineTo(x - 4, y0 + 20); ctx.lineTo(x + 4, y0 + 20); ctx.closePath(); ctx.fill();
    }
    ctx.font = '11px system-ui, sans-serif';
  }
  if (o.legend) {
    let lx = x0 + 8; ctx.textBaseline = 'middle'; ctx.textAlign = 'left';
    for (const s of o.ys) {
      ctx.strokeStyle = s.color; ctx.lineWidth = 2; ctx.beginPath(); ctx.moveTo(lx, y0 + 10); ctx.lineTo(lx + 16, y0 + 10); ctx.stroke();
      ctx.fillStyle = C.ink2; ctx.fillText(s.label, lx + 21, y0 + 10); lx += 30 + ctx.measureText(s.label).width;
    }
  }
  const ci = o.noCursor ? null : S.cursorIdx;
  if (ci != null && ci < r.freq.length) {
    const x = Math.round(fx(r.freq[ci])) + 0.5;
    ctx.strokeStyle = C.ink; ctx.lineWidth = 1; ctx.globalAlpha = 0.55;
    ctx.beginPath(); ctx.moveTo(x, y0); ctx.lineTo(x, y1); ctx.stroke(); ctx.globalAlpha = 1;
    for (const s of o.ys) {
      const v = s.y[ci]; if (v == null || !isFinite(v)) continue;
      ctx.fillStyle = s.color; ctx.strokeStyle = css('--surface'); ctx.lineWidth = 2;
      ctx.beginPath(); ctx.arc(x, fy(Math.max(o.ymin, Math.min(o.ymax, v))), 4.5, 0, 2 * Math.PI); ctx.fill(); ctx.stroke();
    }
  }
  cv._map = { x0, x1, inv: (px) => xmin + (px - x0) / (x1 - x0) * (xmax - xmin) };
}
function nearestIdx(freq, f) {
  let lo = 0, hi = freq.length - 1;
  while (hi - lo > 1) { const m = (lo + hi) >> 1; if (freq[m] <= f) lo = m; else hi = m; }
  return Math.abs(freq[lo] - f) <= Math.abs(freq[hi] - f) ? lo : hi;
}
function swrMax() { return S.swrMaxUser || (S.res && S.res.config.identify ? 10 : 5); }
function updateScaleButtons() {
  const m = swrMax();
  for (const b of document.querySelectorAll('#swrScale button')) b.setAttribute('aria-pressed', String(Number(b.dataset.max) === m));
}
for (const b of document.querySelectorAll('#swrScale button')) b.onclick = () => { S.swrMaxUser = Number(b.dataset.max); updateScaleButtons(); drawAll(); };
const swrTicks = (m) => m <= 3 ? [1, 1.5, 2, 2.5, 3] : m <= 5 ? [1, 1.5, 2, 3, 4, 5] : [1, 2, 3, 4, 5, 6, 7, 8, 9, 10];

function drawSWR() {
  const m = swrMax(), r = S.res;
  const hl = r && r.hold, holdOK = !!(hl && hl.envMin && hl.envMin.length === r.freq.length);
  const hiTrace = hl && S.hold.highlight && S.hold.traces[S.hold.highlight];
  const ys = r ? [{ y: r.derived.swr, color: css('--series1'), label: 'SWR' }] : [];
  if (hiTrace && hiTrace.length === r.freq.length) ys.unshift({ y: hiTrace, color: '#1baf7a', label: '#' + S.hold.highlight, width: 3 });
  drawLine($('swrPlot'), {
    ys,
    ymin: 1, ymax: m, yticks: swrTicks(m), yfmt: (t) => String(t),
    hlines: [{ y: 1.5, label: 'SWR 1.5' }, { y: 2, label: 'SWR 2' }],
    ref: (rr) => rr.derived.swr, dips: (d) => d.swr, envelope: holdOK ? { min: hl.envMin, max: hl.envMax } : null,
  });
  $('legend').innerHTML = `<span><i style="border-color:var(--series1)"></i>Live${r && r.avgN > 1 ? ` (avg ${r.avgN})` : ''}</span>` +
    (S.ref ? `<span><i class="dash" style="border-color:var(--series2)"></i>${esc(S.refName)}</span>` : '') +
    (holdOK ? `<span class="band"><i style="background:var(--band2)"></i>HOLD: ${hl.positions} steady position${hl.positions === 1 ? '' : 's'}</span>` : '') +
    (hiTrace ? `<span><i style="border-color:#1baf7a"></i>position #${S.hold.highlight}</span>` : '') +
    `<span class="band"><i></i>Band</span>`;
  updateScaleButtons();
}
function drawRL() {
  const r = S.res; let lo = -30;
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
  const r = S.res; let lo = -100, hi = 150;
  if (r) { for (const a of [r.derived.r, r.derived.x]) for (const v of a) { if (v > hi) hi = v; if (v < lo) lo = v; } hi = Math.min(hi, 600); lo = Math.max(lo, -600); }
  drawLine($('zPlot'), {
    ys: r ? [{ y: r.derived.r, color: css('--series1'), label: 'R (Ω)' }, { y: r.derived.x, color: '#1baf7a', label: 'X (Ω)' }] : [],
    ymin: lo, ymax: hi, yticks: niceTicks(lo, hi, 6), yfmt: (t) => String(t), hlines: [{ y: 50, label: '50 Ω' }, { y: 0, label: '' }], legend: true,
  });
}
function drawS21() {
  const r = S.res;
  if (!r || !r.derived.s21dB) return drawLine($('s21Plot'), { ys: [], ymin: -80, ymax: 0, yticks: [], yfmt: String });
  let lo = -60, hi = 0;
  for (const v of r.derived.s21dB) { if (v < lo) lo = v; if (v > hi) hi = v; }
  lo = Math.max(-120, Math.floor(lo / 10) * 10); hi = Math.ceil(hi / 10) * 10;
  drawLine($('s21Plot'), { ys: [{ y: r.derived.s21dB, color: css('--series1'), label: 'S21' }], ymin: lo, ymax: hi, yticks: niceTicks(lo, hi, 6), yfmt: (t) => t + ' dB' });
}
function drawSmith() {
  const cv = $('smith'), { ctx, w, h } = setup(cv);
  const C = { grid: css('--grid'), axis: css('--axis'), base: css('--baseline'), ink2: css('--ink2'), muted: css('--muted') };
  const R = Math.min(w, h) / 2 - 22, cx = w / 2, cy = h / 2, px = (re, im) => [cx + re * R, cy - im * R];
  ctx.font = '10.5px system-ui, sans-serif';
  ctx.save(); ctx.beginPath(); ctx.arc(cx, cy, R, 0, 2 * Math.PI); ctx.clip();
  ctx.strokeStyle = C.grid; ctx.lineWidth = 1;
  for (const r of [0.2, 0.5, 1, 2, 5]) { ctx.beginPath(); ctx.arc(cx + R * r / (1 + r), cy, R / (1 + r), 0, 2 * Math.PI); ctx.stroke(); }
  for (const x of [0.2, 0.5, 1, 2, 5]) for (const s of [1, -1]) { ctx.beginPath(); ctx.arc(cx + R, cy - s * R / x, R / x, 0, 2 * Math.PI); ctx.stroke(); }
  ctx.beginPath(); ctx.moveTo(cx - R, cy); ctx.lineTo(cx + R, cy); ctx.stroke();
  ctx.setLineDash([4, 4]); ctx.strokeStyle = C.axis; ctx.beginPath(); ctx.arc(cx, cy, R / 3, 0, 2 * Math.PI); ctx.stroke(); ctx.setLineDash([]);
  ctx.restore();
  ctx.strokeStyle = C.base; ctx.lineWidth = 1.5; ctx.beginPath(); ctx.arc(cx, cy, R, 0, 2 * Math.PI); ctx.stroke();
  ctx.fillStyle = C.axis; ctx.textAlign = 'center'; ctx.textBaseline = 'top';
  for (const r of [0, 0.2, 0.5, 1, 2, 5]) { const g = (r - 1) / (r + 1); ctx.fillText(String(r * 50), cx + g * R + (r === 0 ? 6 : 0), cy + 3); }
  for (const x of [0.2, 0.5, 1, 2, 5]) for (const s of [1, -1]) {
    const den = x * x + 1, [lx, ly] = px((x * x - 1) / den * 1.07, s * 2 * x / den * 1.07);
    ctx.textBaseline = 'middle'; ctx.fillText((s > 0 ? '+j' : '−j') + x * 50, lx, ly);
  }
  ctx.textAlign = 'left'; ctx.textBaseline = 'bottom'; ctx.fillText('SWR 2', cx + R / 3 * 0.72 + 3, cy - R / 3 * 0.72);
  const r = S.res; if (!r) return;
  const trace = (res, color, width, dash) => {
    ctx.strokeStyle = color; ctx.lineWidth = width; ctx.setLineDash(dash || []); ctx.beginPath();
    res.s11.forEach((g, i) => { const [x, y] = px(g[0], g[1]); if (i) ctx.lineTo(x, y); else ctx.moveTo(x, y); });
    ctx.stroke(); ctx.setLineDash([]);
  };
  if (S.ref) trace(S.ref, css('--series2'), 2, [6, 4]);
  trace(r, css('--series1'), 2);
  const bands = r.config.bands || [];
  ctx.strokeStyle = css('--series1'); ctx.lineWidth = 5; ctx.lineCap = 'round';
  for (const b of bands) {
    ctx.beginPath(); let pen = false;
    r.freq.forEach((f, i) => { if (f < b.lo || f > b.hi) { pen = false; return; } const [x, y] = px(r.s11[i][0], r.s11[i][1]); if (pen) ctx.lineTo(x, y); else { ctx.moveTo(x, y); pen = true; } });
    ctx.stroke();
  }
  ctx.lineCap = 'butt';
  const ci = S.cursorIdx;
  if (ci != null && ci < r.freq.length) {
    const [x, y] = px(r.s11[ci][0], r.s11[ci][1]);
    ctx.fillStyle = css('--series1'); ctx.strokeStyle = css('--surface'); ctx.lineWidth = 2;
    ctx.beginPath(); ctx.arc(x, y, 5.5, 0, 2 * Math.PI); ctx.fill(); ctx.stroke();
    ctx.fillStyle = C.ink2; ctx.textAlign = 'left'; ctx.textBaseline = 'middle'; ctx.fillText(mhz(r.freq[ci]) + ' MHz', Math.min(x + 9, w - 70), y);
  }
  ctx.fillStyle = C.muted; ctx.textAlign = 'left'; ctx.textBaseline = 'top';
  ctx.fillText(bands.length ? 'Thick segments: inside ' + bands.map((b) => b.name).join(', ') + ' MHz' : '', 8, 6);
  cv._smith = { cx, cy, R };
}
function renderReadout() {
  const r = S.res, el = $('readout');
  if (!r || S.cursorIdx == null) { el.innerHTML = '<span class="muted">Hover or tap a plot to read values. Arrow keys move the cursor.</span>'; return; }
  const i = S.cursorIdx, d = r.derived, f = r.freq[i];
  const parts = [`<span>Cursor <b>${mhz(f, 3)} MHz</b></span>`, `<span>SWR <b>${swrText(d.swr[i])}</b></span>`,
    `<span>Mismatch loss <b>${d.ml[i].toFixed(2)} dB</b></span>`, `<span>Return loss <b>${d.rl[i].toFixed(1)} dB</b></span>`,
    `<span>|Γ| <b>${d.gammaMag[i].toFixed(3)}</b></span>`, `<span>Z <b>${fmtZ(d.r[i], d.x[i])}</b> (${d.x[i] >= 0 ? 'L' : 'C'} ${fmtLC(d.x[i], f)})</span>`];
  if (d.s21dB) parts.push(`<span>S21 <b>${d.s21dB[i].toFixed(1)} dB</b></span>`);
  if (r.hold && r.hold.envMin && r.hold.envMin.length === r.freq.length) parts.push(`<span>HOLD <b>${swrText(r.hold.envMin[i])}–${swrText(r.hold.envMax[i])}</b></span>`);
  if (S.ref) {
    const j = nearestIdx(S.ref.freq, f);
    if (Math.abs(S.ref.freq[j] - f) < (r.freq[1] - r.freq[0]) * 2) parts.push(`<span style="color:var(--ink2)">REF SWR <b>${swrText(S.ref.derived.swr[j])}</b></span>`);
  }
  el.innerHTML = parts.join('');
}

let raf = 0;
function drawAll() {
  if (raf) return;
  raf = requestAnimationFrame(() => {
    raf = 0;
    if (!$('tuneView').hidden) { drawTunePlot(); drawSpark(); return; }
    if (!$('cmpView').hidden) { drawCmpPlot(); return; }
    drawSWR();
    if (S.tab === 'smith') drawSmith(); else if (S.tab === 'rl') drawRL(); else if (S.tab === 'z') drawZ(); else if (S.tab === 's21') drawS21();
    renderReadout();
  });
}
window.addEventListener('resize', drawAll);

// ---------- interaction ----------
function attachHover(cv) {
  const move = (e) => {
    if (!S.res || !cv._map) return;
    const x = e.clientX - cv.getBoundingClientRect().left;
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
function lockBody() { document.body.classList.toggle('locked', !$('tuneView').hidden || !$('cmpView').hidden || !$('repView').hidden); }

// ---------- TUNE ----------
function defaultTarget() {
  const st = S.state; if (!st) return 0;
  if (st.tune.active) return st.tune.target;
  const b = st.config.bands || [];
  if (!st.config.identify && b.length) return b[0].ref || (b[0].lo + b[0].hi) / 2;
  return 0;
}
async function openTune() {
  await run(async () => {
    S.tuneOpenedAt = Date.now();
    if (!S.state.tune.active) S.state = await api('POST', 'api/tune', { on: true });
    S.trimKey = '';
    $('tuneView').hidden = false; lockBody();
    if (S.state.trimActive) await loadTrim(S.state.trimActive); else await loadTrimList();
    renderTune(); drawAll();
  });
}
function closeTuneUI() { $('tuneView').hidden = true; toneOff(); lockBody(); drawAll(); }
$('tuneClose').onclick = () => run(async () => { closeTuneUI(); await api('POST', 'api/tune', { on: false }); });
document.addEventListener('keydown', (e) => {
  if (e.key !== 'Escape') return;
  if (!$('askView').hidden) return $('askNo').click();
  if (!$('tuneView').hidden) $('tuneClose').click(); else if (!$('cmpView').hidden) $('cmpClose').click(); else if (!$('repView').hidden) $('repClose').click();
});

function renderTune() {
  const st = S.state; if (!st) return;
  const t = st.tune;
  // target chips (only bands inside the profile)
  const tg = $('tuneTargets');
  const prof = currentProfile();
  const pr = prof ? [prof.start, prof.stop] : [st.config.start, st.config.stop];
  const chips = LORA_TARGETS.filter((f) => f >= pr[0] && f <= pr[1]);
  const key = chips.join(',') + '|' + t.target;
  if (tg.dataset.key !== key) {
    tg.dataset.key = key; tg.innerHTML = '<span class="muted small">Target</span>';
    for (const f of chips) {
      const b = document.createElement('button'); b.type = 'button'; b.textContent = mhzR(f);
      b.setAttribute('aria-pressed', String(Math.abs(f - t.target) < 1)); b.onclick = () => setTarget(f); tg.appendChild(b);
    }
    const inp = document.createElement('input'); inp.type = 'number'; inp.step = '0.001'; inp.placeholder = 'other MHz'; inp.title = 'Other target (MHz), Enter to apply';
    if (!chips.some((f) => Math.abs(f - t.target) < 1)) inp.value = mhzR(t.target);
    inp.onchange = () => { const v = parseFloat(inp.value); if (v > 0) setTarget(Math.round(v * 1e6)); };
    tg.appendChild(inp);
  }
  const r = S.res, ti = r && r.tune;
  const cell = $('tDirCell');
  cell.className = 'tcell dir';
  if (!ti) { $('tRes').textContent = '…'; $('tOff').textContent = ''; $('tDir').textContent = 'measuring'; return; }
  $('tRes').innerHTML = ti.found ? `${(ti.resFreq / 1e6).toFixed(2)}<small> MHz</small>` : '–';
  $('tOffLbl').textContent = `Offset to ${mhzR(ti.target)} MHz`;
  const arrows = { shorten: '▼ SHORTEN', lengthen: '▲ LENGTHEN', ok: '✓ ON TARGET', unknown: 'no resonance in view' };
  if (ti.found) $('tOff').innerHTML = `${sgn(ti.offset / 1e6, 2)}<small> MHz</small>`;
  else $('tOff').innerHTML = ti.edgeLow ? '<small>below window</small>' : ti.edgeHigh ? '<small>above window</small>' : '–';
  $('tDir').innerHTML = esc(arrows[ti.direction]) + (ti.direction === 'shorten' ? '<small> too long</small>' : ti.direction === 'lengthen' ? '<small> too short</small>' : '');
  cell.classList.add(ti.direction);
  $('tSwrCell').className = 'ts ' + grade(ti.swrAtTarget);
  $('tSwr').textContent = swrText(ti.swrAtTarget);
  $('tRl').innerHTML = `${ti.rlAtTarget.toFixed(1)}<small> dB</small>`;
  $('tMl').innerHTML = `${ti.mlAtTarget.toFixed(2)}<small> dB</small>`;
  $('tSwrRes').textContent = ti.found ? swrText(ti.swrAtRes) : '–';
  toneUpdate(ti.swrAtTarget);
  renderTrim();
  updateCutBig();
}
function setTarget(f) {
  run(async () => {
    await api('POST', 'api/tune', { on: true, target: f });
    if (S.trim && Math.abs(S.trim.target - f) > 1) S.trim = await api('POST', `api/trim/${encodeURIComponent(S.trim.name)}/target`, { target: f });
  });
}
function drawTunePlot() {
  const r = S.res; if (!r || !r.tune) return;
  const m = Math.max(3, Math.min(10, Math.ceil(Math.max(...r.derived.swr.filter((v) => v < 20)) || 3)));
  drawLine($('tunePlot'), {
    ys: [{ y: r.derived.swr, color: css('--series1'), label: 'SWR', width: 2.5 }],
    ymin: 1, ymax: Math.min(m, 10), yticks: swrTicks(Math.min(m, 10)), yfmt: String,
    hlines: [{ y: 1.5, label: 'SWR 1.5' }, { y: 2, label: 'SWR 2' }],
    ref: (rr) => rr.derived.swr, vline: { f: r.tune.target, label: 'target ' + mhzR(r.tune.target) }, noCursor: true, noBandLabels: true,
  });
}

// Audio tone: pitch follows SWR at target (high = good). Off by default.
function toneOn() {
  try {
    const ac = new (window.AudioContext || window.webkitAudioContext)();
    const osc = ac.createOscillator(), gain = ac.createGain();
    osc.type = 'sine'; gain.gain.value = 0.0001; osc.connect(gain).connect(ac.destination); osc.start();
    gain.gain.setTargetAtTime(0.06, ac.currentTime, 0.05);
    S.tone = { ac, osc, gain };
    if (S.res && S.res.tune) toneUpdate(S.res.tune.swrAtTarget);
  } catch (e) { toast('Audio not available: ' + e.message, { err: true }); }
  $('tuneTone').setAttribute('aria-pressed', String(!!S.tone));
}
function toneOff() {
  if (S.tone) { try { S.tone.osc.stop(); S.tone.ac.close(); } catch { /* ignore */ } S.tone = null; }
  $('tuneTone').setAttribute('aria-pressed', 'false');
}
function toneUpdate(swr) {
  if (!S.tone) return;
  const x = Math.max(0, Math.min(1, (4 - Math.min(swr, 4)) / 3)); // SWR 1 → 1, SWR ≥ 4 → 0
  const f = 220 * Math.pow(2, x * 2.5); // 220 Hz … ~1245 Hz
  S.tone.osc.frequency.setTargetAtTime(f, S.tone.ac.currentTime, 0.08);
}
$('tuneTone').onclick = () => (S.tone ? toneOff() : toneOn());

// ---------- trim calculator ----------
async function loadTrimList() { try { S.trimList = await api('GET', 'api/trim'); } catch { S.trimList = []; } }
async function loadTrim(name) {
  try { S.trim = await api('GET', 'api/trim/' + encodeURIComponent(name)); } catch { S.trim = null; }
  await loadTrimList();
}
function renderTrim(force) {
  const box = $('trimPanel'), t = S.trim, tgt = S.state.tune.target;
  // Rebuild only when the log changes, never on every sweep (keeps taps and typing intact).
  const key = JSON.stringify([t && t.name, t && t.target, t && t.entries.length, t && t.entries.length && t.entries[t.entries.length - 1].time, S.trimList.length, tgt]);
  if (!force && key === S.trimKey) return;
  S.trimKey = key;
  const others = S.trimList.filter((x) => !t || x.name !== t.name);
  const switcher = others.length ? `<label class="small">Switch antenna <select id="trimSwitch"><option value="">–</option>${others.map((x) => `<option>${esc(x.name)}</option>`).join('')}</select></label>` : '';
  if (!t || !t.entries.length) {
    const defName = t ? t.name : 'antenna-' + new Date().toISOString().slice(5, 10);
    box.innerHTML = `<h3>Trim calculator</h3>
      <div class="cutnote">Measure the radiating element once, enter its length and tap <b>Start</b>. vnaview predicts the cut and learns this antenna's MHz/mm from every cut you log.</div>
      <div class="row"><input id="trimName" class="wide" value="${esc(defName)}" maxlength="80" aria-label="Antenna name">
      <input id="trimLen" type="number" step="0.1" min="1" placeholder="length mm" aria-label="Element length in mm">
      <button id="trimStart" class="primary bigbtn" type="button">Start</button></div><div class="row" style="margin-top:8px">${switcher}</div>`;
    $('trimStart').onclick = () => run(async () => {
      const name = $('trimName').value.trim(), len = parseFloat($('trimLen').value);
      if (!name) throw new Error('give the antenna a name');
      if (!(len > 0)) throw new Error('enter the element length in mm');
      S.trim = await api('POST', `api/trim/${encodeURIComponent(name)}/log`, { target: tgt, lengthMm: len });
      await loadTrimList(); renderTrim(true);
    });
  } else {
    const p = t.prediction, last = t.entries[t.entries.length - 1];
    let note = '';
    const { big, cls, cut } = liveCut();
    if (p) {
      note = `→ ${(last.lengthMm - cut).toFixed(1)} mm for ${mhzR(t.target)} MHz · ` + (p.method === 'fit'
        ? `${p.mhzPerMm.toFixed(2)} MHz/mm learnt from ${p.points} logs (${Math.round(p.confidence * 100)} % from this antenna, textbook ${p.priorMHzPerMm.toFixed(2)})`
        : `first estimate L × f_res / f_target (${p.mhzPerMm.toFixed(2)} MHz/mm). It learns this antenna from every cut you log.`);
      if (p.note) note += ' · ' + p.note;
      if (cut < -0.15) note += ' · Cannot un-cut: add a short extension or solder on wire, or accept the offset.';
    }
    const sugg = cut > 0.15 ? Math.round(cut * 0.8 * 10) / 10 : 0;
    const rows = t.entries.map((e, i) => `<tr><td>${i + 1}</td><td>${e.lengthMm.toFixed(1)}</td><td>${e.cutMm ? e.cutMm.toFixed(1) : '–'}</td><td>${mhz(e.freqHz)}</td><td>${sgn((e.freqHz - t.target) / 1e6, 2)}</td><td>${swrText(e.swrAtTarget)}</td></tr>`).join('');
    box.innerHTML = `<h3>Trim calculator · ${esc(t.name)} <span class="muted small">now ${last.lengthMm.toFixed(1)} mm</span></h3>
      <div class="trimgrid"><div>
        <div class="cutbig ${cls}" id="cutBig">${big}</div><div class="cutnote" id="cutNote">${esc(note)}</div>
        <div class="row"><span>I cut</span><input id="trimCut" type="number" step="0.1" min="0" value="${sugg}" aria-label="mm cut">
        <span>mm</span><button id="trimLog" class="primary bigbtn" type="button">Measure &amp; log</button></div>
        <div class="cutnote">Tip: cut a bit less than suggested (prefilled at 80 %), log, and repeat. Each log takes a fresh sweep.</div>
        <div class="row"><button id="trimUndo" class="ghost small" type="button">Undo last log</button>
        <button id="trimNew" class="ghost small" type="button">New antenna</button>${switcher}</div>
      </div><div>
        <canvas id="trimSpark" class="spark"></canvas>
        <table class="trimlog"><thead><tr><th>#</th><th>mm</th><th>cut</th><th>f res</th><th>Δ target</th><th>SWR@tgt</th></tr></thead><tbody>${rows}</tbody></table>
      </div></div>`;
    $('trimLog').onclick = () => run(async () => {
      const cut = parseFloat($('trimCut').value);
      if (!(cut >= 0)) throw new Error('enter how many mm you cut (0 if you only re-measure)');
      $('trimLog').disabled = true; $('trimLog').textContent = 'Measuring…';
      try { S.trim = await api('POST', `api/trim/${encodeURIComponent(t.name)}/log`, { cutMm: cut }); }
      finally { renderTrim(true); }
      toast(`Logged ${S.trim.entries[S.trim.entries.length - 1].lengthMm.toFixed(1)} mm → ${mhz(S.trim.entries[S.trim.entries.length - 1].freqHz)} MHz`,
        { undo: async () => { S.trim = await api('POST', `api/trim/${encodeURIComponent(t.name)}/undo`); renderTrim(true); } });
    });
    $('trimUndo').onclick = () => run(async () => {
      const e = t.entries[t.entries.length - 1];
      if (!(await ask('Remove the last log entry?', `${e.lengthMm.toFixed(1)} mm → ${mhz(e.freqHz)} MHz will be removed from ${t.name}.`, 'Remove'))) return;
      S.trim = await api('POST', `api/trim/${encodeURIComponent(t.name)}/undo`); renderTrim(true);
    });
    $('trimNew').onclick = () => { S.trim = null; renderTrim(true); };
  }
  const sw = $('trimSwitch');
  if (sw) sw.onchange = () => run(async () => { if (!sw.value) return; await api('POST', `api/trim/${encodeURIComponent(sw.value)}/select`); await loadTrim(sw.value); renderTrim(true); });
  drawSpark();
}
// liveCut turns the live TUNE resonance into a cut using the learnt MHz/mm,
// so the big number always agrees with the TUNE readout above it.
function liveCut() {
  const t = S.trim, p = t && t.prediction, ti = S.res && S.res.tune;
  if (!p) return { big: '–', cls: '', cut: 0 };
  let cut = p.cutMm;
  if (ti && ti.found && Math.abs(ti.target - t.target) < 1) {
    if (ti.direction === 'ok') return { big: '✓ leave it', cls: 'ok', cut: 0 };
    cut = (t.target - ti.resFreq) / 1e6 / p.mhzPerMm;
  }
  if (Math.abs(cut) < 0.15) return { big: '✓ leave it', cls: 'ok', cut };
  if (cut > 0) return { big: `CUT ${cut.toFixed(1)} mm`, cls: '', cut };
  return { big: `LENGTHEN ${(-cut).toFixed(1)} mm`, cls: 'lengthen', cut };
}
function updateCutBig() {
  const el = $('cutBig'); if (!el) return;
  const { big, cls } = liveCut();
  el.textContent = big; el.className = 'cutbig ' + cls;
}
function drawSpark() {
  const cv = $('trimSpark'), t = S.trim; if (!cv || !t || !t.entries.length) return;
  const { ctx, w, h } = setup(cv);
  const fs = t.entries.map((e) => e.freqHz);
  let lo = Math.min(t.target, ...fs), hi = Math.max(t.target, ...fs);
  const pad = Math.max((hi - lo) * 0.15, 1e6); lo -= pad; hi += pad;
  const fx = (i) => fs.length === 1 ? w / 2 : 14 + i / (fs.length - 1) * (w - 28), fy = (f) => h - 14 - (f - lo) / (hi - lo) * (h - 26);
  ctx.strokeStyle = css('--accent'); ctx.setLineDash([5, 3]); ctx.lineWidth = 1.5;
  ctx.beginPath(); ctx.moveTo(0, fy(t.target)); ctx.lineTo(w, fy(t.target)); ctx.stroke(); ctx.setLineDash([]);
  ctx.fillStyle = css('--accent'); ctx.font = '11px system-ui, sans-serif'; ctx.textAlign = 'right'; ctx.textBaseline = 'bottom';
  ctx.fillText('target ' + mhzR(t.target), w - 2, fy(t.target) - 2);
  ctx.strokeStyle = css('--series1'); ctx.lineWidth = 2; ctx.beginPath();
  fs.forEach((f, i) => (i ? ctx.lineTo(fx(i), fy(f)) : ctx.moveTo(fx(i), fy(f)))); ctx.stroke();
  ctx.fillStyle = css('--series1');
  fs.forEach((f, i) => { ctx.beginPath(); ctx.arc(fx(i), fy(f), 3.5, 0, 2 * Math.PI); ctx.fill(); });
  ctx.fillStyle = css('--ink2'); ctx.textAlign = 'left'; ctx.textBaseline = 'top'; ctx.fillText('resonance per log', 4, 2);
}

// ---------- COMPARE wizard ----------
async function openCompare() {
  await run(async () => {
    if (S.state.tune.active) throw new Error('close TUNE first');
    await api('POST', 'api/compare', { on: true });
    S.cmpKey = ''; S.cmpWasCont = S.state.continuous;
    $('cmpView').hidden = false; lockBody();
    if (!S.state.continuous) await api('POST', 'api/continuous', { on: true });
    renderCompare();
  });
}
$('cmpClose').onclick = () => run(async () => { $('cmpView').hidden = true; lockBody(); await api('POST', 'api/compare', { on: false }); if (!S.cmpWasCont) await api('POST', 'api/continuous', { on: false }); drawAll(); });
function renderCompare() {
  const st = S.state, c = st.compare, r = S.res, live = r && r.compare;
  const body = $('cmpBody');
  const key = JSON.stringify([c.hasA, c.aName, c.aTime, c.hasUndo, c.leaderboard.map((e) => e.name + e.time)]);
  if (key === S.cmpKey) { updateCompareLive(); drawAll(); return; }
  S.cmpKey = key;
  $('cmpBand').textContent = live ? `${live.band.name === 'sweep' ? 'whole sweep' : live.band.name + ' MHz band'} ${mhzR(live.band.lo)}–${mhzR(live.band.hi)} MHz` : '';
  const note = `<div class="note">Relative measurement: only the difference between antennas counts, so <b>no THRU calibration is needed</b>. Keep the reference antenna, the cables and the distance exactly the same for every antenna.</div>`;
  const undo = c.hasUndo ? '<button id="cmpUndo" class="ghost small" type="button">Undo last change</button>' : '';
  if (!c.hasA) {
    body.innerHTML = `<div class="step"><span class="num">1</span>Port 1 → fixed reference antenna.<br>Port 2 → antenna <b>A</b> (your baseline).</div>
      <div class="stepsub">When the reading below is steady, name it and tap <b>Store A</b>.</div>
      <div><div class="tl">Live S21 in band</div><div class="livebig" id="cmpLive">…</div></div>
      <div class="row"><input id="cmpAName" class="wide" value="${esc(S.cmpAName)}" maxlength="40" aria-label="Name of antenna A">
      <button id="cmpStoreA" class="primary bigbtn" type="button">Store A</button>${undo}</div>${note}`;
    $('cmpStoreA').onclick = () => run(async () => {
      S.cmpAName = $('cmpAName').value.trim() || 'A';
      const res = await api('POST', 'api/compare/a', { name: S.cmpAName });
      toast(res.message, { undo: async () => { await api('POST', 'api/compare/undo'); } });
    });
  } else {
    const rows = c.leaderboard.map((e, i) => {
      const isA = e.name === c.aName && i === c.leaderboard.findIndex((x) => x.name === c.aName) && e.meanDB === 0 && e.worstDB === 0;
      const cl = e.meanDB > 0.05 ? 'pos' : e.meanDB < -0.05 ? 'neg' : '';
      return `<tr><td>${i + 1}</td><td><b>${esc(e.name)}</b>${isA ? ' <span class="muted">(A)</span>' : ''}</td><td class="${cl}">${isA ? '0.0 (ref)' : sgn(e.meanDB) + ' dB'}</td><td>${isA ? '–' : sgn(e.worstDB) + ' dB'}</td><td>${isA ? '–' : sgn(e.bestDB) + ' dB'}</td></tr>`;
    }).join('');
    body.innerHTML = `<div class="step"><span class="num">2</span>Swap port 2 to the next antenna (keep everything else still).</div>
      <div class="stepsub">A = <b>${esc(c.aName)}</b>, stored ${hhmm(c.aTime)} on the ${esc(c.profile)} profile.</div>
      <div class="tunebig"><div class="tcell"><div class="tl">Δ vs A in band (average)</div><div class="livebig" id="cmpLive">…</div><div id="cmpWB" class="stepsub"></div></div>
      <div class="tcell"><div class="tl">Name this antenna</div><div class="row" style="margin-top:6px"><input id="cmpName" class="wide" value="${esc(S.cmpName)}" maxlength="40" aria-label="Antenna name">
      <button id="cmpAdd" class="primary bigbtn" type="button">Add to leaderboard</button></div></div></div>
      <canvas id="cmpPlot" class="plot cmpplot"></canvas>
      <table class="board"><thead><tr><th>#</th><th>Antenna</th><th>Δ in band (avg)</th><th>worst</th><th>best</th></tr></thead><tbody>${rows}</tbody></table>
      <div class="row">${undo}<button id="cmpReA" class="ghost small" type="button">Replace A with the current antenna</button>
      <button id="cmpReset" class="ghost small" type="button">Start over</button></div>${note}`;
    $('cmpAdd').onclick = () => run(async () => {
      S.cmpName = $('cmpName').value.trim();
      const res = await api('POST', 'api/compare/add', { name: S.cmpName });
      toast(res.message, { undo: async () => { await api('POST', 'api/compare/undo'); } });
      S.cmpName = nextName(S.cmpName);
    });
    $('cmpReA').onclick = () => run(async () => {
      const n = c.leaderboard.length - 1;
      if (!(await ask('Replace antenna A?', `The current antenna becomes the new A. The ${n} result${n === 1 ? '' : 's'} measured against ${c.aName} will be cleared (Undo restores them).`, 'Replace A'))) return;
      const res = await api('POST', 'api/compare/a', { name: $('cmpName').value.trim() || 'A' });
      toast(res.message, { undo: async () => { await api('POST', 'api/compare/undo'); } });
    });
    $('cmpReset').onclick = () => run(async () => {
      if (!(await ask('Start over?', `Clears A (${c.aName}) and ${c.leaderboard.length - 1} result(s). Undo restores them.`, 'Start over'))) return;
      await api('DELETE', 'api/compare');
      toast('Compare cleared', { undo: async () => { await api('POST', 'api/compare/undo'); } });
    });
  }
  const u = $('cmpUndo'); if (u) u.onclick = () => run(async () => { await api('POST', 'api/compare/undo'); toast('Restored'); });
  updateCompareLive(); drawAll();
}
function nextName(n) { const m = /^(.*?)(\d+)$/.exec(n); if (m) return m[1] + (Number(m[2]) + 1); if (/^[A-Y]$/.test(n)) return String.fromCharCode(n.charCodeAt(0) + 1); return n + ' 2'; }
function updateCompareLive() {
  const el = $('cmpLive'), live = S.res && S.res.compare; if (!el) return;
  if (!live) { el.textContent = '…'; el.className = 'livebig'; return; }
  if (!S.state.compare.hasA) { el.innerHTML = `${live.bandDB.toFixed(1)}<small style="font-size:.35em"> dB</small>`; el.className = 'livebig'; return; }
  if (!live.ready) { el.innerHTML = `<span style="font-size:.35em">${esc(live.problem)}</span>`; el.className = 'livebig'; return; }
  el.innerHTML = `${sgn(live.meanDB)}<small style="font-size:.35em"> dB</small>`;
  el.className = 'livebig ' + (live.meanDB > 0.5 ? 'up' : live.meanDB < -0.5 ? 'down' : '');
  const wb = $('cmpWB'); if (wb) wb.textContent = `worst ${sgn(live.worstDB)} dB @ ${mhz(live.worstFreq)} · best ${sgn(live.bestDB)} dB @ ${mhz(live.bestFreq)} MHz`;
}
function drawCmpPlot() {
  const cv = $('cmpPlot'), r = S.res, live = r && r.compare; if (!cv || !live || !live.delta) return;
  let lo = -6, hi = 6;
  for (const v of live.delta) { if (v < lo) lo = v; if (v > hi) hi = v; }
  lo = Math.max(-60, Math.floor(lo / 3) * 3); hi = Math.min(30, Math.ceil(hi / 3) * 3);
  const rr = Object.assign({}, r, { config: Object.assign({}, r.config, { bands: [live.band].filter((b) => b.name !== 'sweep') }) });
  drawLine(cv, { r: rr, ys: [{ y: live.delta, color: css('--series1'), label: 'Δ vs A' }], ymin: lo, ymax: hi, yticks: niceTicks(lo, hi, 6),
    yfmt: (t) => (t > 0 ? '+' : '') + t + ' dB', hlines: [{ y: 0, label: 'A' }], noCursor: true, legend: true });
}

// ---------- REPORT ----------
async function openReport() {
  $('repView').hidden = false; lockBody();
  const st = S.state;
  await loadTrimList();
  let reports = [];
  try { reports = await api('GET', 'api/reports'); } catch { /* ignore */ }
  const ch = st.reportChoices;
  const name = st.trimActive || (S.trim && S.trim.name) || 'antenna-' + new Date().toISOString().slice(0, 10);
  $('repBody').innerHTML = `<div class="step">Make a printable report</div>
    <div class="stepsub">It opens in a new tab (Print → Save as PDF) and is saved in the data directory.</div>
    <label class="stepsub">Antenna name<br><input id="repName" class="wide" style="font-size:18px;padding:8px 10px;width:100%;max-width:420px" value="${esc(name)}" maxlength="80"></label>
    <div class="stepsub"><b>Include these sweeps</b> (latest of each profile):</div>
    <div class="choices">${ch.length ? ch.map((c) => `<label><input type="checkbox" checked value="${esc(c.profile)}"> <b>${esc(c.label)}</b> <span class="muted">swept ${hhmm(c.time)}</span> ${c.verdict ? badge(c.verdict) : ''}</label>`).join('')
      : '<span class="muted">No profile sweeps yet: tap a profile first.</span>'}</div>
    <label class="stepsub">Trim log <select id="repTrim"><option value="">none</option>${S.trimList.map((t) => `<option ${t.name === st.trimActive ? 'selected' : ''}>${esc(t.name)}</option>`).join('')}</select></label>
    <div class="stepsub">${st.compare.leaderboard.length > 1 ? `S21 compare results (${st.compare.leaderboard.length - 1} vs ${esc(st.compare.aName)}) are included.` : 'No S21 compare results yet.'}</div>
    <label class="stepsub">Notes (optional)<textarea id="repNotes" placeholder="e.g. 3D-printed enclosure, 10 cm RG316 pigtail"></textarea></label>
    <div class="row"><button id="repGo" class="primary bigbtn" type="button">Create report</button></div>
    ${reports.length ? `<div class="stepsub"><b>Earlier reports</b></div><div class="choices">${reports.slice(0, 8).map((r) => `<label><a href="api/reports/${encodeURIComponent(r.file)}" target="_blank" rel="noopener">${esc(r.file)}</a> <span class="muted">${new Date(r.time).toLocaleString()}</span> <a class="btn ghost small" href="api/reports/${encodeURIComponent(r.file)}?download=1">download</a></label>`).join('')}</div>` : ''}`;
  $('repGo').onclick = () => run(async () => {
    const profiles = [...document.querySelectorAll('#repBody .choices input[type=checkbox]:checked')].map((x) => x.value);
    const w = window.open('', '_blank');
    try {
      const r = await api('POST', 'api/report', { name: $('repName').value.trim(), notes: $('repNotes').value, profiles, trim: $('repTrim').value });
      if (w) w.location = r.url; else location.href = r.url;
      toast('Report saved: ' + r.file);
    } catch (e) { if (w) w.close(); throw e; }
  });
}
$('repClose').onclick = () => { $('repView').hidden = true; lockBody(); };

// ---------- start ----------
selectTab('smith');
renderVerdict();
connectEvents();
run(loadSnaps);
