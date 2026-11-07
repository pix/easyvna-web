'use strict';
// EasyVNA Web, single-file browser build.
//
// This script boots the Go app (compiled to WebAssembly, embedded in this
// page as gzip + base64) and gives it what the easyvna-web program gets
// from the operating system:
//   * HTTP: the UI's fetch('api/...') and EventSource('api/events') are
//     routed into the Go http.Handler running in this page (no network);
//   * storage: the data directory lives in IndexedDB;
//   * USB: the NanoVNA is opened with Web Serial (Chrome / Edge);
//   * ?demo runs the built-in simulator instead (works in every browser).
// It then inserts the UI (index.html body) and runs app.js unchanged.
(() => {
  const VID = 0x0483, PID = 0x5740; // NanoVNA-H / H4 (STM32 USB CDC)
  const NO_SERIAL = 'USB needs Chrome or Edge on a computer; the simulator works everywhere.';
  const NO_PORT = 'No NanoVNA chosen yet: plug it in, switch it on, then press Connect and pick it in the list (USB 0483:5740).';
  const LOST = 'NanoVNA disconnected (USB unplugged). Plug it back in: EasyVNA Web reconnects by itself, or press Connect.';
  const q = new URLSearchParams(location.search);
  const demo = q.has('demo');
  const hasSerial = !!navigator.serial;
  const fromFile = location.protocol === 'file:';
  const $ = (id) => document.getElementById(id);
  const text = (id) => { const el = $(id); if (!el) throw new Error('page is incomplete: #' + id + ' missing'); return el.textContent; };

  // ---------- storage: the data directory in IndexedDB ----------
  const DB = demo ? 'easyvna-web-demo' : 'easyvna-web';
  const storage = {
    db: null, chain: Promise.resolve(), error: null,
    open() {
      return new Promise((resolve, reject) => {
        const r = indexedDB.open(DB, 1);
        r.onupgradeneeded = () => r.result.createObjectStore('files');
        r.onsuccess = () => resolve(r.result);
        r.onerror = () => reject(r.error);
        r.onblocked = () => reject(new Error('IndexedDB is blocked by another tab'));
      });
    },
    req(mode, fn) {
      return new Promise((resolve, reject) => {
        const tx = this.db.transaction('files', mode);
        const r = fn(tx.objectStore('files'));
        tx.oncomplete = () => resolve(r && r.result);
        tx.onerror = tx.onabort = () => reject(tx.error || new Error('IndexedDB transaction failed'));
      });
    },
    async load() {
      try { this.db = await this.open(); } catch (e) { this.error = e; return []; }
      const st = await new Promise((resolve, reject) => {
        const tx = this.db.transaction('files', 'readonly'), os = tx.objectStore('files');
        const keys = os.getAllKeys(), vals = os.getAll();
        tx.oncomplete = () => resolve([keys.result, vals.result]);
        tx.onerror = () => reject(tx.error);
      });
      return st[0].map((name, i) => ({ name: String(name), data: st[1][i].data, mtime: st[1][i].mtime }));
    },
    queue(fn) {
      if (!this.db) return;
      this.chain = this.chain.then(fn).catch((e) => { this.error = e; console.warn('easyvna-web: storage:', e); });
    },
    put(name, data, mtime) { this.queue(() => this.req('readwrite', (os) => os.put({ data, mtime }, name))); },
    remove(name) { this.queue(() => this.req('readwrite', (os) => os.delete(name))); },
    flush() { return this.chain; },
  };

  // ---------- Web Serial adapter (used by internal/webserial) ----------
  const withTimeout = (p, ms) => Promise.race([p, new Promise((resolve) => setTimeout(resolve, ms))]);
  const serial = {
    chosen: null, // picked with requestPort() on a click
    cur: null, // open handle
    lost: false, // the last port was unplugged
    match(p) { const i = p.getInfo ? p.getInfo() : {}; return i.usbVendorId === VID && i.usbProductId === PID; },
    async granted() { return hasSerial ? (await navigator.serial.getPorts()).filter((p) => this.match(p)) : []; },
    // choose shows the browser's port chooser. Call it straight from a click.
    async choose() {
      if (!hasSerial) throw new Error(NO_SERIAL);
      try {
        this.chosen = await navigator.serial.requestPort({ filters: [{ usbVendorId: VID, usbProductId: PID }] });
      } catch (e) {
        if (e && e.name === 'NotFoundError') { // chooser closed without a pick
          if (!(await this.granted()).length) throw new Error(NO_PORT);
          return;
        }
        throw e;
      }
    },
    // open(onData, onEnd) is called by the Go side to connect.
    async open(onData, onEnd) {
      if (!hasSerial) throw new Error(NO_SERIAL);
      if (this.cur) await this.cur.close();
      let port = this.chosen;
      if (!port) port = (await this.granted())[0];
      if (!port) throw new Error(this.lost ? LOST : NO_PORT);
      try {
        await port.open({ baudRate: 115200, bufferSize: 65536 });
      } catch (e) {
        this.chosen = null;
        throw new Error('Could not open the NanoVNA (' + (e && e.message || e) + '). Another program or browser tab may be using it ' +
          '(NanoVNA-Saver, a serial terminal, another EasyVNA Web tab): close it, then press Connect.');
      }
      const reader = port.readable.getReader(), writer = port.writable.getWriter();
      const info = port.getInfo ? port.getInfo() : {};
      const hex = (v) => (v || 0).toString(16).padStart(4, '0');
      let closing = false, ended = false;
      const end = (msg) => { if (!ended) { ended = true; onEnd(msg || ''); } };
      const h = {
        port,
        name: `USB ${hex(info.usbVendorId)}:${hex(info.usbProductId)} (Web Serial)`,
        write: (u8) => writer.write(u8),
        close: async () => {
          if (closing) return;
          closing = true;
          if (this.cur === h) this.cur = null;
          try { await withTimeout(reader.cancel(), 1000); } catch { /* gone */ }
          try { reader.releaseLock(); } catch { /* pending read */ }
          try { await withTimeout(writer.close(), 1000); } catch { /* gone */ }
          try { writer.releaseLock(); } catch { /* ignore */ }
          try { await withTimeout(port.close(), 1500); } catch { /* already closed */ }
          end('');
        },
        lost: (msg) => { end(msg); h.close(); },
      };
      (async () => {
        try {
          for (;;) {
            const { value, done } = await reader.read();
            if (done) break;
            if (value && value.length) onData(value);
          }
          end(closing ? '' : 'NanoVNA disconnected: the serial stream ended.');
        } catch (e) {
          end(closing ? '' : 'NanoVNA disconnected (' + (e && e.message || e) + ').');
        }
      })();
      this.cur = h;
      this.lost = false;
      return h;
    },
  };
  if (hasSerial) {
    navigator.serial.addEventListener('disconnect', (e) => {
      const cur = serial.cur;
      if (e.target === serial.chosen) serial.chosen = null;
      if (cur && e.target === cur.port) {
        serial.lost = true;
        cur.lost(LOST);
        // Let the server notice now rather than at the next sweep.
        setTimeout(() => window.fetch('api/connect', { method: 'POST' }).catch(() => {}), 50);
      }
    });
    navigator.serial.addEventListener('connect', (e) => {
      if (!demo && serial.match(e.target) && !serial.cur) {
        setTimeout(() => window.fetch('api/connect', { method: 'POST' }).catch(() => {}), 1200); // let it boot
      }
    });
  }

  // ---------- API shim: fetch / EventSource -> Go handler ----------
  const H = {}; // filled by the Go side: goServe, goStream, goMockShell
  // apiPath returns "/api/..." for URLs that point at the app's API
  // (relative to this page), else null.
  function apiPath(url) {
    let u;
    try { u = new URL(url, document.baseURI); } catch { return null; }
    const base = new URL('.', document.baseURI).href;
    if (!u.href.startsWith(base + 'api/')) return null;
    return '/' + u.href.slice(base.length).replace(/#.*$/, '');
  }
  const origFetch = window.fetch.bind(window);
  async function apiFetch(input, init) {
    init = init || {};
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
    const p = apiPath(url);
    if (p === null) return origFetch(input, init);
    const method = String(init.method || (input instanceof Request ? input.method : 'GET')).toUpperCase();
    const headers = [...new Headers(init.headers || (input instanceof Request ? input.headers : undefined)).entries()];
    const body = init.body == null ? new Uint8Array(0) : new Uint8Array(await new Response(init.body).arrayBuffer());
    const r = await H.goServe(method, p, headers, body);
    const nullBody = r.status === 204 || r.status === 205 || r.status === 304;
    return new Response(nullBody ? null : r.body, { status: r.status, headers: r.headers });
  }
  const OrigES = window.EventSource;
  class GoEventSource extends EventTarget {
    constructor(url, opts) {
      super();
      const p = apiPath(String(url));
      if (p === null) return new OrigES(url, opts);
      this.url = new URL(String(url), document.baseURI).href;
      this.withCredentials = false;
      this.readyState = 0;
      this.onopen = this.onmessage = this.onerror = null;
      setTimeout(() => {
        if (this.readyState === 2) return;
        this.readyState = 1;
        this._emit(new Event('open'));
        this._cancel = H.goStream(p, (name, data) => {
          queueMicrotask(() => { if (this.readyState === 1) this._emit(new MessageEvent(name, { data })); });
        }, (err) => {
          queueMicrotask(() => {
            if (this.readyState === 2) return;
            if (err) console.warn('easyvna-web: event stream:', err);
            this.readyState = 2;
            this._emit(new Event('error'));
          });
        });
      }, 0);
    }
    _emit(ev) {
      this.dispatchEvent(ev);
      const h = this['on' + ev.type];
      if (typeof h === 'function') h.call(this, ev);
    }
    close() {
      this.readyState = 2;
      if (this._cancel) { this._cancel(); this._cancel = null; }
    }
  }
  GoEventSource.CONNECTING = 0; GoEventSource.OPEN = 1; GoEventSource.CLOSED = 2;

  // ---------- downloads and report pages ----------
  function saveBlob(blob, name) {
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = name;
    a.rel = 'noopener';
    a.style.display = 'none';
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(a.href), 60000);
  }
  function fileName(resp, fallback) {
    const m = /filename="([^"]+)"/.exec(resp.headers.get('Content-Disposition') || '');
    return m ? m[1] : fallback;
  }
  async function okFetch(url, init) {
    const r = await window.fetch(url, init);
    if (!r.ok) {
      let msg = r.statusText || 'HTTP ' + r.status;
      try { msg = (await r.json()).error || msg; } catch { /* not JSON */ }
      throw new Error(msg);
    }
    return r;
  }
  // Reports are complete HTML pages: show them from a blob: URL. Their
  // "Download HTML" link (?download=1 on the server) asks this page to save
  // the report (a blob: page opened from file:// may not load blob: URLs
  // itself); without an opener it links to its own blob.
  const reportWins = new Map(); // report window -> { html, file }
  window.addEventListener('message', (e) => {
    const r = e.data && e.data.easyvnaSaveReport ? reportWins.get(e.source) : null;
    if (r) saveBlob(new Blob([r.html], { type: 'text/html' }), r.file);
  });
  function showReport(w, html, file, hash) {
    const blob0 = new Blob([html], { type: 'text/html' });
    if (!w) { saveBlob(blob0, file); return; } // pop-up blocked: download it instead
    const fix = '<script>(function(){var a=document.querySelector(\'a[href="?download=1"]\');if(!a)return;' +
      'a.href=location.href;a.download=' + JSON.stringify(file) + ';' +
      'if(window.opener)a.addEventListener("click",function(e){e.preventDefault();window.opener.postMessage({easyvnaSaveReport:true},"*");});' +
      '})();<\/script>';
    const page = /<\/body>/i.test(html) ? html.replace(/<\/body>/i, fix + '</body>') : html + fix;
    reportWins.set(w, { html, file });
    for (const k of reportWins.keys()) if (k.closed) reportWins.delete(k);
    w.location.href = URL.createObjectURL(new Blob([page], { type: 'text/html' })) + (hash || ''); // kept: the page links to itself
  }
  async function openReport(w, url, file, hash) {
    try {
      const r = await okFetch(url);
      showReport(w, await r.text(), file || decodeURIComponent(url.split('/').pop().split('?')[0]), hash);
    } catch (e) { if (w) w.close(); throw e; }
  }
  function fail(e) {
    if (typeof window.toast === 'function') window.toast(e.message || String(e), { err: true });
    else console.error(e);
  }
  // Links into the API (exports, earlier reports) cannot be followed:
  // there is no server. Fetch them through the shim instead.
  document.addEventListener('click', (e) => {
    const a = e.target && e.target.closest ? e.target.closest('a[href]') : null;
    if (!a || e.defaultPrevented) return;
    const p = apiPath(a.getAttribute('href'));
    if (p === null) return;
    e.preventDefault();
    if (a.hasAttribute('download') || /[?&]download=1\b/.test(p)) {
      okFetch(a.href).then(async (r) => saveBlob(await r.blob(), fileName(r, a.getAttribute('download') || 'easyvna-export'))).catch(fail);
    } else {
      const w = window.open('', '_blank');
      openReport(w, a.href).catch(fail);
    }
  }, true);

  // ---------- this page as a file (offline copy) ----------
  // The page is built from a fixed skeleton plus a few <script>/<style>
  // blocks whose text the HTML parser keeps byte for byte, so the exact
  // file can be rebuilt from the DOM without fetching it again.
  function selfHTML() {
    const sk = JSON.parse(text('ev-skel'));
    let out = sk.parts[0];
    sk.ids.forEach((id, i) => { out += text(id) + sk.parts[i + 1]; });
    return out;
  }

  // ---------- host bar: mode, browser data, offline copy ----------
  function hostUI(version) {
    const btn = $('btnConnect');
    if (!demo) { btn.textContent = 'Connect'; btn.title = 'Choose the NanoVNA (USB) and connect'; }
    const foot = document.querySelector('.foot');
    if (foot) foot.append(` · ${version} (in your browser)`);
    const bar = document.createElement('div');
    bar.className = 'hostbar'; bar.id = 'hostbar';
    const here = location.pathname;
    const mode = demo
      ? `<span class="hmode"><b>Simulator</b> · a simulated NanoVNA-H4 and test antennas, nothing to plug in</span>
         <a class="btn ghost small" id="hostUSB" href="${here}">Use my NanoVNA</a>`
      : `<span class="hmode"><b>USB</b> · NanoVNA-H / H4 through Web Serial${hasSerial ? '' : ' (not available in this browser)'}</span>
         <a class="btn ghost small" id="hostDemo" href="${here}?demo">Try the simulator</a>`;
    bar.innerHTML = `${mode}<span class="hsp"></span>
      <span class="hdata" title="Settings, REF traces, trim logs, reports and snapshots are kept in this browser only">Data stays in this browser:</span>
      <button class="ghost small" type="button" id="hostExport">Export data</button>
      <button class="ghost small" type="button" id="hostImport">Import data</button>
      <input type="file" id="hostImportFile" accept=".json,application/json" hidden>
      ${fromFile ? '' : '<button class="ghost small" type="button" id="hostOffline" title="Save this whole app as one HTML file that runs without internet">Download for offline use</button>'}`;
    document.querySelector('header.top').after(bar);
    if (!demo && !hasSerial) {
      const n = document.createElement('div');
      n.className = 'hostnotice'; n.id = 'hostNotice'; n.setAttribute('role', 'note');
      n.innerHTML = `<b>${NO_SERIAL}</b> <span>This browser has no Web Serial, so it cannot talk to a NanoVNA over USB.</span>
        <a class="btn primary small" href="${here}?demo">Try the simulator</a>`;
      bar.after(n);
    }
    if (storage.error) {
      const n = document.createElement('div');
      n.className = 'hostnotice'; n.setAttribute('role', 'note');
      n.textContent = 'Browser storage is not available (' + (storage.error.message || storage.error) + '): settings and measurements are lost when you close this page. Use Export data to keep them.';
      bar.after(n);
    }
    $('hostExport').onclick = () => okFetch('api/backup').then(async (r) => saveBlob(await r.blob(), fileName(r, 'easyvna-web-backup.json'))).catch(fail);
    $('hostImport').onclick = () => $('hostImportFile').click();
    $('hostImportFile').onchange = async (e) => {
      const f = e.target.files[0];
      e.target.value = '';
      if (!f) return;
      try {
        const body = await f.text();
        let n = 0;
        try { n = (JSON.parse(body).files || []).length; } catch { throw new Error(f.name + ' is not an EasyVNA Web backup (Export data makes one).'); }
        const ok = typeof window.ask === 'function'
          ? await window.ask(`Import ${n} files from ${f.name}?`, 'Files with the same name (settings, REF traces, trim logs, reports, snapshots) are replaced; the others are kept. The page reloads afterwards.', 'Import')
          : true;
        if (!ok) return;
        const r = await okFetch('api/backup', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body });
        const res = await r.json();
        await storage.flush();
        if (typeof window.toast === 'function') window.toast(res.message + ', reloading…');
        setTimeout(() => location.reload(), 600);
      } catch (err) { fail(err); }
    };
    const off = $('hostOffline');
    if (off) off.onclick = () => saveBlob(new Blob([selfHTML()], { type: 'text/html' }), 'easyvna-web.html');
  }

  // ---------- boot ----------
  const host = window.EasyVNAHost = {
    config: { demo, readOnly: q.has('readonly'), antenna: q.get('antenna') || '', firmware: q.get('firmware') || '',
      storeLabel: (demo ? 'this browser (simulator data, IndexedDB "' : 'this browser (IndexedDB "') + DB + '")', files: [] },
    storage, serial,
    demo, fromFile, hasSerial,
    beforeConnect: async () => { if (!demo) await serial.choose(); },
    openReport,
    selfHTML,
    goReady: null,
  };
  function loading(msg, err) {
    const t = $('ev-loading-text');
    if (t) t.textContent = msg;
    const l = $('ev-loading');
    if (l && err) l.classList.add('err');
  }
  function b64bytes(s) {
    if (Uint8Array.fromBase64) return Uint8Array.fromBase64(s.replace(/\s+/g, ''));
    const bin = atob(s.replace(/\s+/g, ''));
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  }
  async function boot() {
    if (typeof WebAssembly !== 'object' || typeof DecompressionStream !== 'function') {
      throw new Error('This browser is too old for EasyVNA Web (it needs WebAssembly and DecompressionStream). Use a current Chrome, Edge, Firefox or Safari.');
    }
    loading('Loading EasyVNA Web…');
    const [files, wasm] = await Promise.all([
      storage.load(),
      new Response(new Blob([b64bytes(text('ev-wasm'))]).stream().pipeThrough(new DecompressionStream('gzip'))).arrayBuffer(),
    ]);
    host.config.files = files;
    const ready = new Promise((resolve) => { host.goReady = resolve; });
    const go = new window.Go();
    const { instance } = await WebAssembly.instantiate(wasm, go.importObject);
    go.run(instance).then(() => loading('EasyVNA Web stopped. Reload the page.', true));
    const version = await ready;
    H.goServe = host.goServe; H.goStream = host.goStream; H.goMockShell = host.goMockShell;
    window.fetch = apiFetch;
    window.EventSource = GoEventSource;
    // The UI: index.html's body, then app.js.
    document.body.insertAdjacentHTML('afterbegin', text('ev-body'));
    hostUI(version);
    const s = document.createElement('script');
    s.textContent = text('ev-app');
    document.body.appendChild(s);
    const l = $('ev-loading');
    if (l) l.remove();
  }
  boot().catch((e) => { console.error(e); loading('EasyVNA Web could not start: ' + (e.message || e), true); });
})();
