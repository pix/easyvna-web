'use strict';
// EasyVNA Web firmware flasher: back up and update a NanoVNA-H / H4 over
// USB with WebUSB, talking to the STM32 system bootloader (ROM, DfuSe
// protocol, USB 0483:df11). Pure browser code, used by the easyvna-web
// program's page (http://localhost) and by the single-file HTML build
// (https or file://).
//
// The first part (layout, image formats, validation, page planning, the
// DfuSe state machine) has no DOM dependency and is exported for the node
// unit tests (scripts/flasher/). The second part is the FIRMWARE dialog.
//
// Safety rules kept here:
//   * never a mass erase, never read-unprotect (both wipe the calibration);
//     pages are erased one by one, only those the image covers;
//   * a normal image must end before the calibration/config save area;
//     only "Restore a full backup" writes it, after an explicit confirmation;
//   * a full backup is read and downloaded before any write (once per
//     device and page session), and every write is read back and compared.
(function (root) {
  const VID = 0x0483, PID = 0xdf11;
  const FLASH_BASE = 0x08000000;
  const REQ = { DETACH: 0, DNLOAD: 1, UPLOAD: 2, GETSTATUS: 3, CLRSTATUS: 4, GETSTATE: 5, ABORT: 6 };
  const ST = {
    appIDLE: 0, appDETACH: 1, dfuIDLE: 2, dfuDNLOAD_SYNC: 3, dfuDNBUSY: 4, dfuDNLOAD_IDLE: 5,
    dfuMANIFEST_SYNC: 6, dfuMANIFEST: 7, dfuMANIFEST_WAIT_RESET: 8, dfuUPLOAD_IDLE: 9, dfuERROR: 10,
  };
  const STATE_NAMES = Object.keys(ST);
  const STATUS_NAMES = ['OK', 'errTARGET', 'errFILE', 'errWRITE', 'errERASE', 'errCHECK_ERASED', 'errPROG', 'errVERIFY',
    'errADDRESS', 'errNOTDONE', 'errFIRMWARE', 'errVENDOR', 'errUSBR', 'errPOR', 'errUNKNOWN', 'errSTALLEDPKT'];
  const ERR_VENDOR = 11;
  const CMD = { SET_ADDRESS: 0x21, ERASE: 0x41 };
  const DEFAULT_XFER = 2048;

  // The two NanoVNA families with an STM32 DFU bootloader. fwEnd is where
  // the firmware area ends and the save area (calibration slots + config,
  // see SAVE_PROP_CONFIG_ADDR in the firmware's hardware.h) begins.
  const TARGETS = [
    { id: 'f303-256k', name: 'NanoVNA-H4', mcu: 'STM32F303', flashSize: 256 * 1024, ram: 40 * 1024, fwEnd: 0x08023800, slots: 7 },
    { id: 'f072-128k', name: 'NanoVNA-H', mcu: 'STM32F072', flashSize: 128 * 1024, ram: 16 * 1024, fwEnd: 0x08018000, slots: 5 },
  ];

  const hex8 = (v) => '0x' + (v >>> 0).toString(16).toUpperCase().padStart(8, '0');
  const kb = (n) => (n % 1024 ? (n / 1024).toFixed(1) : String(n / 1024)) + ' KB';
  const fmtInt = (n) => String(n).replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const u32 = (b, o) => (b[o] | (b[o + 1] << 8) | (b[o + 2] << 16) | (b[o + 3] << 24)) >>> 0;
  const le32 = (v) => [v & 0xff, (v >>> 8) & 0xff, (v >>> 16) & 0xff, (v >>> 24) & 0xff];

  // ---------- DfuSe memory layout ("@Internal Flash  /0x08000000/128*0002Kg") ----------

  // parseLayout reads the DfuSe memory description an alternate setting
  // carries as its interface name: "@name/addr/N*SIZE[BKM]p,.../addr/...".
  // p is a letter a..g: bit 0 readable, bit 1 erasable, bit 2 writable.
  // The first region is taken as the flash (start, size, pages).
  function parseLayout(name) {
    const s = String(name == null ? '' : name).trim();
    if (!s.startsWith('@')) throw new Error(`not a DfuSe memory layout: "${s}"`);
    const parts = s.slice(1).split('/');
    const layout = { name: parts[0].trim(), raw: s, regions: [] };
    if (parts.length < 3) throw new Error(`DfuSe memory layout without a region: "${s}"`);
    for (let i = 1; i < parts.length; i += 2) {
      const a = parts[i].trim();
      if (a === '' && i === parts.length - 1) break; // trailing "/"
      if (!/^0x[0-9a-f]{1,8}$/i.test(a)) throw new Error(`DfuSe memory layout: bad address "${a}" in "${s}"`);
      if (i + 1 >= parts.length) throw new Error(`DfuSe memory layout: no sectors after ${a} in "${s}"`);
      const start = parseInt(a, 16);
      let at = start;
      const segments = [];
      for (const seg of parts[i + 1].split(',')) {
        const m = /^\s*(\d+)\s*\*\s*(\d+)\s*([BKM]?)\s*([a-g])\s*$/i.exec(seg);
        if (!m) throw new Error(`DfuSe memory layout: bad sector description "${seg.trim()}" in "${s}"`);
        const count = parseInt(m[1], 10);
        const size = parseInt(m[2], 10) * { '': 1, B: 1, K: 1024, M: 1048576 }[m[3].toUpperCase()];
        const p = m[4].toLowerCase().charCodeAt(0) - 96;
        if (!count || !size) throw new Error(`DfuSe memory layout: empty sector description "${seg.trim()}"`);
        segments.push({ start: at, count, size, readable: !!(p & 1), erasable: !!(p & 2), writable: !!(p & 4) });
        at += count * size;
      }
      layout.regions.push({ start, size: at - start, segments });
    }
    const r0 = layout.regions[0];
    layout.start = r0.start;
    layout.size = r0.size;
    layout.segments = r0.segments;
    return layout;
  }

  // pageAt returns the erase page {start, size} holding addr, or null.
  function pageAt(layout, addr) {
    for (const s of layout.segments) {
      const end = s.start + s.count * s.size;
      if (addr >= s.start && addr < end) {
        const start = s.start + Math.floor((addr - s.start) / s.size) * s.size;
        return { start, size: s.size, erasable: s.erasable, writable: s.writable };
      }
    }
    return null;
  }
  // pagesFor lists the pages covering [start, end).
  function pagesFor(layout, start, end) {
    const pages = [];
    for (let a = start; a < end;) {
      const p = pageAt(layout, a);
      if (!p) throw new Error(`address ${hex8(a)} is outside the flash`);
      pages.push(p);
      a = p.start + p.size;
    }
    return pages;
  }
  function targetFor(layout) {
    if (!layout || layout.start !== FLASH_BASE) return null;
    return TARGETS.find((t) => t.flashSize === layout.size) || null;
  }
  function describeLayout(layout) {
    const segs = layout.segments.map((s) => `${s.count} × ${kb(s.size)}`).join(' + ');
    return `${layout.name} at ${hex8(layout.start)}, ${segs} = ${kb(layout.size)}`;
  }

  // ---------- firmware image formats ----------

  // merge turns [{addr, data}] into one image from the lowest address,
  // gaps filled with 0xFF (erased flash).
  function merge(chunks, what) {
    if (!chunks.length) throw new Error(`${what}: no data`);
    let lo = Infinity, hi = 0;
    for (const c of chunks) { lo = Math.min(lo, c.addr); hi = Math.max(hi, c.addr + c.data.length); }
    if (hi - lo > 16 * 1048576) throw new Error(`${what}: data spread over ${kb(hi - lo)} (${hex8(lo)}–${hex8(hi)}), not a firmware image`);
    const out = new Uint8Array(hi - lo).fill(0xff);
    for (const c of chunks) out.set(c.data, c.addr - lo);
    return { base: lo, data: out };
  }

  // parseHex reads an Intel HEX file (records 00..05).
  function parseHex(text) {
    let upper = 0, eof = false, n = 0;
    const chunks = [];
    for (const raw of String(text).split('\n')) {
      n++;
      const line = raw.trim();
      if (!line) continue;
      if (eof) break;
      if (line[0] !== ':' || !/^:([0-9a-f]{2})+$/i.test(line)) throw new Error(`Intel HEX line ${n}: not a record`);
      const b = new Uint8Array((line.length - 1) / 2);
      for (let i = 0; i < b.length; i++) b[i] = parseInt(line.substr(1 + 2 * i, 2), 16);
      if (b.length < 5 || b.length !== b[0] + 5) throw new Error(`Intel HEX line ${n}: bad length`);
      if (b.reduce((s, v) => s + v, 0) & 0xff) throw new Error(`Intel HEX line ${n}: bad checksum`);
      const off = (b[1] << 8) | b[2], type = b[3], d = b.subarray(4, 4 + b[0]);
      switch (type) {
        case 0: chunks.push({ addr: upper + off, data: d.slice() }); break;
        case 1: eof = true; break;
        case 2: upper = ((d[0] << 8) | d[1]) * 16; break;
        case 4: upper = ((d[0] << 8) | d[1]) * 65536; break;
        case 3: case 5: break; // start address: not needed
        default: throw new Error(`Intel HEX line ${n}: unknown record type ${type}`);
      }
    }
    if (!eof) throw new Error('Intel HEX: no end-of-file record (truncated file?)');
    return merge(chunks, 'Intel HEX');
  }

  const CRC_TABLE = (() => {
    const t = new Uint32Array(256);
    for (let i = 0; i < 256; i++) { let c = i; for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1; t[i] = c >>> 0; }
    return t;
  })();
  function crc32(b) { let c = 0xffffffff; for (let i = 0; i < b.length; i++) c = CRC_TABLE[(c ^ b[i]) & 0xff] ^ (c >>> 8); return (c ^ 0xffffffff) >>> 0; }

  // parseDfuFile reads a DfuSe .dfu file (ST's format: prefix "DfuSe",
  // targets "Target" with elements, 16-byte DFU suffix with a CRC). The
  // elements of the target for alternate setting 0 (internal flash) are used.
  function parseDfuFile(b) {
    const ascii = (o, n) => String.fromCharCode(...b.subarray(o, o + n));
    if (b.length < 11 + 16 || ascii(0, 5) !== 'DfuSe') throw new Error('.dfu: not a DfuSe file');
    const sfx = b.length - 16;
    if (ascii(sfx + 8, 3) !== 'UFD' || b[sfx + 11] !== 16) throw new Error('.dfu: no DFU suffix');
    if (((~crc32(b.subarray(0, b.length - 4))) >>> 0) !== u32(b, b.length - 4)) throw new Error('.dfu: bad CRC (damaged file)');
    const vid = b[sfx + 4] | (b[sfx + 5] << 8), pid = b[sfx + 2] | (b[sfx + 3] << 8);
    const targets = b[10];
    let o = 11;
    const chunks = [];
    for (let t = 0; t < targets; t++) {
      if (o + 274 > sfx || ascii(o, 6) !== 'Target') throw new Error('.dfu: bad target header');
      const alt = b[o + 6], nb = u32(b, o + 270);
      o += 274;
      for (let e = 0; e < nb; e++) {
        const addr = u32(b, o), size = u32(b, o + 4);
        if (o + 8 + size > sfx) throw new Error('.dfu: element runs past the end of the file');
        if (alt === 0) chunks.push({ addr, data: b.slice(o + 8, o + 8 + size) });
        o += 8 + size;
      }
    }
    if (!chunks.length) throw new Error('.dfu: no data for the internal flash (alternate setting 0)');
    const img = merge(chunks, '.dfu');
    img.usbId = `${vid.toString(16).padStart(4, '0')}:${pid.toString(16).padStart(4, '0')}`;
    return img;
  }

  // loadImage reads a firmware file: .bin (raw, at 0x08000000), Intel .hex
  // or DfuSe .dfu (by extension, else by content).
  function loadImage(name, bytes) {
    const b = bytes instanceof Uint8Array ? bytes : new Uint8Array(bytes);
    const ext = (/\.([a-z0-9]+)$/i.exec(name || '') || [])[1];
    const e = ext ? ext.toLowerCase() : '';
    const head = String.fromCharCode(...b.subarray(0, 5));
    let img;
    if (e === 'dfu' || head === 'DfuSe') img = { ...parseDfuFile(b), format: 'dfu' };
    else if (e === 'hex' || e === 'ihex' || (b[0] === 0x3a && e !== 'bin' && /^:[0-9a-f]{10}/i.test(String.fromCharCode(...b.subarray(0, 11))))) {
      img = { ...parseHex(new TextDecoder().decode(b)), format: 'hex' };
    } else img = { base: FLASH_BASE, data: b, format: 'bin' };
    img.name = name || 'firmware.bin';
    img.fileSize = b.length;
    return img;
  }

  // validateImage checks img against the device's layout. opts.mode is
  // 'firmware' (normal: must end before the save area) or 'restore' (a full
  // backup: the whole flash); opts.flashSize, when set, is the flash size
  // the image was built for (the bundled EasyVNA image: 256 KB, H4 only).
  function validateImage(layout, img, opts = {}) {
    const mode = opts.mode || 'firmware';
    const checks = [];
    const ok = (text) => checks.push({ ok: true, text });
    const bad = (text) => checks.push({ ok: false, text });
    const t = targetFor(layout);
    const len = img.data.length, end = img.base + len;
    if (!t) {
      bad(`Unknown flash layout (${describeLayout(layout)}): EasyVNA Web only writes the NanoVNA-H (STM32F072, 128 KB) and the NanoVNA-H4 (STM32F303, 256 KB).`);
    } else ok(`Device: ${t.name} (${t.mcu}, ${kb(t.flashSize)} flash).`);
    if (opts.flashSize && opts.flashSize !== layout.size) {
      const want = TARGETS.find((x) => x.flashSize === opts.flashSize);
      bad(`This firmware is for the ${want ? `${want.name} (${want.mcu}, ${kb(want.flashSize)})` : kb(opts.flashSize) + ' flash'}; ` +
        `this device has ${kb(layout.size)}${t ? ` (${t.name}, ${t.mcu})` : ''}. Flashing it would not work.`);
    }
    if (!len) bad('The image is empty.');
    else if (img.base !== layout.start) bad(`The image starts at ${hex8(img.base)}, not at the start of the flash (${hex8(layout.start)}).`);
    else if (end > layout.start + layout.size) bad(`The image is too big: ${fmtInt(len)} bytes, the flash has ${fmtInt(layout.size)}.`);
    else ok(`Size: ${fmtInt(len)} bytes (${kb(len)}), fits in the ${kb(layout.size)} flash.`);
    if (len >= 8 && img.base === layout.start) {
      const sp = u32(img.data, 0), rv = u32(img.data, 4);
      const ramTop = 0x20000000 + (t ? t.ram : 64 * 1024);
      if ((sp & 3) || sp <= 0x20000000 || sp > ramTop) bad(`Vector table: the initial stack pointer ${hex8(sp)} is not in RAM (0x20000000–${hex8(ramTop)}). Not a firmware for this device, or a damaged file.`);
      else ok(`Vector table: stack pointer ${hex8(sp)} in RAM.`);
      const ra = (rv & ~1) >>> 0;
      if (!(rv & 1) || ra < img.base + 8 || ra >= end) bad(`Vector table: the reset vector ${hex8(rv)} does not point into the image (Thumb code at ${hex8(img.base)}–${hex8(end)}).`);
      else ok(`Vector table: reset vector ${hex8(rv)} inside the image.`);
    } else if (len) bad('The image is too short for a vector table.');
    if (t) {
      const saveEnd = layout.start + layout.size;
      const area = `${hex8(t.fwEnd)}–${hex8(saveEnd)}, ${t.slots} calibration slots + configuration`;
      if (mode === 'restore') {
        if (len !== layout.size || img.base !== layout.start) bad(`A full backup is exactly ${fmtInt(layout.size)} bytes (the whole flash); this file has ${fmtInt(len)}.`);
        else ok(`Full backup: restores the whole flash, the calibration and configuration area (${area}) included.`);
      } else if (end > t.fwEnd) {
        bad(`The image ends at ${hex8(end)}, inside the calibration and configuration area (${area}). EasyVNA Web does not overwrite it ` +
          `with a firmware image; to put back a whole backup, choose "Restore a full backup".`);
      } else ok(`Calibration and configuration (${area}) are kept: they are not erased.`);
    }
    const valid = checks.every((c) => c.ok);
    return { ok: valid, checks, target: t, plan: valid ? planWrite(layout, img) : null };
  }

  // planWrite lists the pages to erase and the bytes to write (padded with
  // 0xFF to a multiple of 4: flash is programmed in half-words).
  function planWrite(layout, img) {
    let data = img.data;
    if (data.length % 4) { const p = new Uint8Array(data.length + 4 - (data.length % 4)).fill(0xff); p.set(data); data = p; }
    const pages = pagesFor(layout, img.base, img.base + data.length);
    return { start: img.base, data, length: img.data.length, pages, eraseBytes: pages.reduce((s, p) => s + p.size, 0) };
  }

  // ---------- DfuSe over WebUSB ----------

  class DfuError extends Error {
    constructor(msg, info = {}) { super(msg); this.name = 'DfuError'; Object.assign(this, info); }
  }
  const PROTECTED_MSG = 'The NanoVNA refused to be read: its flash is read-protected (RDP). EasyVNA Web cannot back it up and does not write ' +
    'firmware without a backup. Removing the read protection erases the whole flash, calibration included, so EasyVNA Web does not offer it.';
  const statusName = (s) => STATUS_NAMES[s] || 'status ' + s;
  const stateName = (s) => STATE_NAMES[s] || 'state ' + s;
  function isDisconnect(e) {
    return !!e && (e.name === 'NetworkError' || e.name === 'NotFoundError' || e.name === 'InvalidStateError' || /disconnect|not found|no device/i.test(e.message || ''));
  }

  class DfuSe {
    // opts: sleep(ms), log(text), timeoutMs (per operation)
    constructor(device, opts = {}) {
      this.dev = device;
      this.sleep = opts.sleep || ((ms) => new Promise((r) => setTimeout(r, ms)));
      this.log = opts.log || (() => {});
      this.timeoutMs = opts.timeoutMs || 20000;
      this.xfer = DEFAULT_XFER;
      this.ifn = 0;
      this.layout = null;
      this.target = null;
    }

    async ctrlOut(request, value, data) {
      const setup = { requestType: 'class', recipient: 'interface', request, value, index: this.ifn };
      const r = data && data.length ? await this.dev.controlTransferOut(setup, data) : await this.dev.controlTransferOut(setup);
      if (r.status !== 'ok') throw new DfuError(`USB request ${request} ${r.status === 'stall' ? 'stalled' : 'failed (' + r.status + ')'}`, { code: 'stall', request });
      return r;
    }
    async ctrlIn(request, value, length) {
      const r = await this.dev.controlTransferIn({ requestType: 'class', recipient: 'interface', request, value, index: this.ifn }, length);
      if (r.status !== 'ok') throw new DfuError(`USB request ${request} ${r.status === 'stall' ? 'stalled' : 'failed (' + r.status + ')'}`, { code: 'stall', request });
      const d = r.data;
      return new Uint8Array(d.buffer, d.byteOffset, d.byteLength);
    }
    async descriptor(type, index, langId, length) {
      const r = await this.dev.controlTransferIn({ requestType: 'standard', recipient: 'device', request: 6, value: (type << 8) | index, index: langId }, length);
      if (r.status !== 'ok') throw new DfuError('GET_DESCRIPTOR failed');
      return new Uint8Array(r.data.buffer, r.data.byteOffset, r.data.byteLength);
    }
    async configDescriptor() {
      const h = await this.descriptor(2, 0, 0, 9);
      return this.descriptor(2, 0, 0, h[2] | (h[3] << 8));
    }

    // findFlashAlt looks for the DFU alternate setting of the internal flash.
    findFlashAlt(names) {
      const cfg = this.dev.configuration;
      if (!cfg) return null;
      let first = null;
      for (const itf of cfg.interfaces) {
        for (const alt of itf.alternates) {
          if (alt.interfaceClass !== 0xfe || alt.interfaceSubclass !== 1) continue;
          const name = (names && names.get(`${itf.interfaceNumber}/${alt.alternateSetting}`)) || alt.interfaceName || '';
          const hit = { ifn: itf.interfaceNumber, alt: alt.alternateSetting, name };
          if (/^@\s*Internal Flash/i.test(name)) return hit;
          if (!first && /0x08000000/i.test(name)) first = hit;
        }
      }
      return first;
    }
    // interfaceNames reads the iInterface strings itself (some platforms
    // leave USBAlternateInterface.interfaceName empty).
    async interfaceNames() {
      const names = new Map();
      const c = await this.configDescriptor();
      for (let i = 0; i + 1 < c.length && c[i] > 0; i += c[i]) {
        if (c[i + 1] !== 4 || c[i] < 9 || !c[i + 8]) continue;
        const s = await this.descriptor(3, c[i + 8], 0x0409, 255);
        let str = '';
        for (let k = 2; k + 1 < Math.min(s[0], s.length); k += 2) str += String.fromCharCode(s[k] | (s[k + 1] << 8));
        names.set(`${c[i + 2]}/${c[i + 3]}`, str);
      }
      return names;
    }
    // transferSize reads wTransferSize from the DFU functional descriptor.
    async transferSize() {
      try {
        const c = await this.configDescriptor();
        for (let i = 0; i + 1 < c.length && c[i] > 0; i += c[i]) {
          if (c[i + 1] === 0x21 && c[i] >= 7) { const n = c[i + 5] | (c[i + 6] << 8); if (n >= 64) return n; }
        }
      } catch (e) { this.log('no DFU functional descriptor: ' + e.message); }
      return 0;
    }

    // connect opens the device, selects the internal flash alternate
    // setting, reads the layout and transfer size and returns to dfuIDLE.
    async connect() {
      const d = this.dev;
      if (!d.opened) await d.open();
      if (!d.configuration) await d.selectConfiguration(1);
      let hit = this.findFlashAlt();
      if (!hit || !hit.name) hit = this.findFlashAlt(await this.interfaceNames());
      if (!hit || !hit.name) throw new DfuError('This USB device has no DFU "Internal Flash" interface: is it an STM32 in DFU mode (0483:df11)?');
      await d.claimInterface(hit.ifn);
      await d.selectAlternateInterface(hit.ifn, hit.alt);
      this.ifn = hit.ifn;
      this.layout = parseLayout(hit.name);
      this.target = targetFor(this.layout);
      this.xfer = (await this.transferSize()) || DEFAULT_XFER;
      this.log(`layout "${hit.name}", transfer size ${this.xfer}`);
      await this.toIdle();
      return this.layout;
    }
    async close() { try { await this.dev.close(); } catch { /* gone */ } }

    async getStatus() {
      const b = await this.ctrlIn(REQ.GETSTATUS, 0, 6);
      if (b.length < 6) throw new DfuError('short DFU_GETSTATUS reply');
      return { status: b[0], poll: b[1] | (b[2] << 8) | (b[3] << 16), state: b[4] };
    }
    async getState() { return (await this.ctrlIn(REQ.GETSTATE, 0, 1))[0]; }
    clearStatus() { return this.ctrlOut(REQ.CLRSTATUS, 0); }
    abort() { return this.ctrlOut(REQ.ABORT, 0); }

    // toIdle brings the device back to dfuIDLE (CLRSTATUS from dfuERROR,
    // ABORT from the other states).
    async toIdle() {
      for (let i = 0; i < 5; i++) {
        const s = await this.getStatus();
        if (s.state === ST.dfuIDLE) return s;
        if (s.state === ST.dfuERROR) await this.clearStatus();
        else if (s.state === ST.dfuDNBUSY) await this.sleep(Math.max(s.poll, 5));
        else await this.abort();
      }
      throw new DfuError('The bootloader does not return to dfuIDLE.');
    }
    // finish polls DFU_GETSTATUS after a DNLOAD until the operation is done,
    // waiting bwPollTimeout between polls.
    async finish(what) {
      const t0 = Date.now();
      for (;;) {
        let s;
        try { s = await this.getStatus(); } catch (e) {
          if (e.code === 'stall') { await this.recover(); throw new DfuError(`${what}: the bootloader stalled`, { code: 'stall' }); }
          throw e;
        }
        if (s.state === ST.dfuDNBUSY || s.state === ST.dfuDNLOAD_SYNC) {
          if (Date.now() - t0 > this.timeoutMs) throw new DfuError(`${what}: timed out (device busy)`, { code: 'timeout' });
          await this.sleep(Math.max(s.poll, 1));
          continue;
        }
        if (s.state === ST.dfuERROR || s.status !== 0) {
          await this.recover();
          throw new DfuError(`${what} failed: ${statusName(s.status)} (${stateName(s.state)})`, { code: s.status === ERR_VENDOR ? 'protected' : 'status', status: s.status });
        }
        if (s.state === ST.dfuDNLOAD_IDLE || s.state === ST.dfuIDLE) return s;
        throw new DfuError(`${what}: unexpected state ${stateName(s.state)}`);
      }
    }
    async recover() { try { const s = await this.getStatus(); if (s.state === ST.dfuERROR) await this.clearStatus(); } catch { /* best effort */ } }

    async command(bytes, what) {
      await this.ctrlOut(REQ.DNLOAD, 0, new Uint8Array(bytes));
      return this.finish(what);
    }
    setAddress(a) { return this.command([CMD.SET_ADDRESS, ...le32(a)], `Set address ${hex8(a)}`); }
    erasePage(a) { return this.command([CMD.ERASE, ...le32(a)], `Erase page ${hex8(a)}`); }

    // erase erases the pages one by one (never a mass erase).
    async erase(pages, hooks = {}) {
      await this.toIdle();
      let done = 0;
      const total = pages.reduce((s, p) => s + p.size, 0);
      for (const p of pages) {
        if (hooks.stopped && hooks.stopped()) throw new DfuError('Stopped', { code: 'stopped' });
        await this.erasePage(p.start);
        done += p.size;
        if (hooks.progress) hooks.progress(done, total, p.start);
      }
    }
    // windows splits [addr, addr+len) into runs of at most 0xFFF0 blocks
    // (wBlockNum is 16 bits; data blocks start at 2).
    windows(addr, len) {
      const max = 0xfff0 * this.xfer, out = [];
      for (let o = 0; o < len; o += max) out.push({ addr: addr + o, off: o, len: Math.min(max, len - o) });
      return out;
    }
    // write writes data at addr (already erased): block 2+n goes to
    // address pointer + n * transfer size.
    async write(addr, data, hooks = {}) {
      await this.toIdle();
      for (const w of this.windows(addr, data.length)) {
        await this.setAddress(w.addr);
        for (let n = 0, o = 0; o < w.len; n++, o += this.xfer) {
          if (hooks.stopped && hooks.stopped()) throw new DfuError('Stopped', { code: 'stopped' });
          const chunk = data.subarray(w.off + o, w.off + Math.min(o + this.xfer, w.len));
          await this.ctrlOut(REQ.DNLOAD, 2 + n, chunk);
          await this.finish(`Write at ${hex8(w.addr + o)}`);
          if (hooks.progress) hooks.progress(w.off + o + chunk.length, data.length, w.addr + o);
        }
      }
      await this.toIdle();
    }
    // read reads len bytes at addr: set the address pointer, ABORT back to
    // dfuIDLE, then UPLOAD block 2+n.
    async read(addr, len, hooks = {}) {
      await this.toIdle();
      const out = new Uint8Array(len);
      for (const w of this.windows(addr, len)) {
        try { await this.setAddress(w.addr); } catch (e) { if (e.code === 'protected') throw new DfuError(PROTECTED_MSG, { code: 'protected' }); throw e; }
        await this.abort();
        for (let n = 0, o = 0; o < w.len; n++, o += this.xfer) {
          if (hooks.stopped && hooks.stopped()) { await this.abort().catch(() => {}); throw new DfuError('Stopped', { code: 'stopped' }); }
          const want = Math.min(this.xfer, w.len - o);
          let b;
          try { b = await this.ctrlIn(REQ.UPLOAD, 2 + n, want); } catch (e) {
            if (e.code !== 'stall') throw e;
            let s = null;
            try { s = await this.getStatus(); } catch { /* stalled too */ }
            await this.recover();
            if (o === 0 && w.off === 0 && (!s || s.status === ERR_VENDOR || s.state === ST.dfuERROR)) throw new DfuError(PROTECTED_MSG, { code: 'protected' });
            throw new DfuError(`Read at ${hex8(w.addr + o)} failed${s ? ': ' + statusName(s.status) : ''}`, { code: 'read' });
          }
          if (b.length !== want) throw new DfuError(`Read at ${hex8(w.addr + o)}: got ${b.length} bytes instead of ${want}`, { code: 'read' });
          out.set(b, w.off + o);
          if (hooks.progress) hooks.progress(w.off + o + want, len, w.addr + o);
        }
        await this.abort();
      }
      await this.toIdle();
      return out;
    }
    // leave starts the firmware: address pointer at addr, a zero-length
    // DNLOAD, then GETSTATUS; the device resets, so a lost device is fine.
    async leave(addr = FLASH_BASE) {
      await this.toIdle();
      await this.setAddress(addr);
      try {
        await this.ctrlOut(REQ.DNLOAD, 2);
        await this.getStatus();
      } catch (e) { this.log('leave: ' + (e.message || e) + ' (expected: the device resets)'); }
      await this.close();
    }
  }

  // dumpFlash reads the whole flash.
  async function dumpFlash(dfu, hooks = {}) {
    return dfu.read(dfu.layout.start, dfu.layout.size, hooks);
  }
  // compare returns the differing addresses (at most max) and their count.
  function compare(want, got, base, max = 8) {
    const diffs = [];
    let count = 0;
    for (let i = 0; i < want.length; i++) {
      if (want[i] !== got[i]) { count++; if (diffs.length < max) diffs.push({ addr: base + i, want: want[i], got: got[i] }); }
    }
    return { count, diffs };
  }
  // flashPlan erases, writes and verifies one plan (see planWrite).
  // hooks: phase(name), progress(done, total, addr), stopped().
  async function flashPlan(dfu, plan, hooks = {}) {
    const phase = (p) => hooks.phase && hooks.phase(p);
    phase('erase');
    await dfu.erase(plan.pages, hooks);
    phase('write');
    await dfu.write(plan.start, plan.data, hooks);
    phase('verify');
    const got = await dfu.read(plan.start, plan.data.length, hooks);
    const v = compare(plan.data, got, plan.start);
    if (v.count) {
      const d = v.diffs[0];
      throw new DfuError(`Verify failed: ${v.count} byte${v.count === 1 ? '' : 's'} differ, the first at ${hex8(d.addr)} (written 0x${d.want.toString(16).padStart(2, '0')}, read 0x${d.got.toString(16).padStart(2, '0')}).`,
        { code: 'verify', diffs: v.diffs, count: v.count });
    }
    phase('done');
    return { verified: plan.data.length };
  }

  const api = {
    VID, PID, FLASH_BASE, REQ, ST, CMD, TARGETS, DEFAULT_XFER, PROTECTED_MSG,
    parseLayout, pageAt, pagesFor, targetFor, describeLayout, parseHex, parseDfuFile, crc32, loadImage,
    validateImage, planWrite, compare, DfuSe, DfuError, dumpFlash, flashPlan, isDisconnect, hex8, kb, fmtInt,
  };
  if (typeof module === 'object' && module.exports) module.exports = api;
  root.EasyVNAFlasher = api;
  if (typeof document !== 'undefined' && document.getElementById('fwView')) initUI(api);

  // =====================================================================
  // FIRMWARE dialog
  // =====================================================================
  function initUI(F) {
    const $ = (id) => document.getElementById(id);
    const esc = (s) => String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
    const view = $('fwView'), body = $('fwBody');
    const hasUSB = !!(navigator.usb && navigator.usb.requestDevice);
    const NO_USB = 'Firmware update needs WebUSB: Chrome or Edge on a computer (Windows, macOS, Linux, ChromeOS).';
    const U = {
      dev: null, dfu: null, err: '', busy: '', stop: false,
      choice: 'easyvna', file: null, fileErr: '', restore: null, restoreErr: '',
      fw: null, fwErr: '', fwLoading: null,
      dumped: new Map(), // device key -> backup name (this page session)
      backups: [], phase: '', prog: null, result: null, opened: false,
    };

    // ----- helpers -----
    function saveBlob(blob, name) {
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = name; a.rel = 'noopener'; a.style.display = 'none';
      document.body.appendChild(a); a.click(); a.remove();
      setTimeout(() => URL.revokeObjectURL(a.href), 60000);
    }
    async function sha256(bytes) {
      if (!(root.crypto && root.crypto.subtle)) return '';
      const h = new Uint8Array(await root.crypto.subtle.digest('SHA-256', bytes));
      return Array.from(h, (v) => v.toString(16).padStart(2, '0')).join('');
    }
    const stamp = (d) => {
      const p = (n) => String(n).padStart(2, '0');
      return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}-${p(d.getHours())}${p(d.getMinutes())}${p(d.getSeconds())}`;
    };
    const devKey = () => (U.dev && U.dev.serialNumber ? `${U.dev.serialNumber}|${U.dfu.layout.size}` : '');
    const layoutId = () => (U.dfu.target ? U.dfu.target.id : `${U.dfu.layout.size / 1024}k`);
    const toastMsg = (m, err) => { if (typeof root.toast === 'function') root.toast(m, err ? { err: true } : {}); };
    // appState is the main UI's state (app.js), when it has loaded.
    function appState() {
      try { return typeof S !== 'undefined' && S.state ? S.state : null; } catch { return null; } // eslint-disable-line no-undef
    }
    function appConnected() { const st = appState(); return st && st.connected && st.info && !st.info.mock ? st : null; }

    // ----- backups kept in this browser (IndexedDB) -----
    const IDB = {
      name: 'easyvna-web-flasher', keep: 4,
      open() {
        return new Promise((resolve, reject) => {
          if (!root.indexedDB) { reject(new Error('no IndexedDB')); return; }
          const r = indexedDB.open(this.name, 1);
          r.onupgradeneeded = () => r.result.createObjectStore('backups', { keyPath: 'name' });
          r.onsuccess = () => resolve(r.result);
          r.onerror = () => reject(r.error);
        });
      },
      async tx(mode, fn) {
        const db = await this.open();
        try {
          return await new Promise((resolve, reject) => {
            const t = db.transaction('backups', mode), r = fn(t.objectStore('backups'));
            t.oncomplete = () => resolve(r && r.result);
            t.onerror = t.onabort = () => reject(t.error);
          });
        } finally { db.close(); }
      },
      async list() { return ((await this.tx('readonly', (s) => s.getAll())) || []).sort((a, b) => b.date - a.date); },
      async put(rec) {
        await this.tx('readwrite', (s) => s.put(rec));
        const all = await this.list();
        for (const old of all.slice(this.keep)) await this.tx('readwrite', (s) => s.delete(old.name));
      },
    };
    async function loadBackups() { try { U.backups = await IDB.list(); } catch { U.backups = []; } }

    // ----- the bundled EasyVNA firmware -----
    async function loadBundled() {
      const H = root.EasyVNAHost;
      let manifest, bytes;
      if (H && typeof H.firmware === 'function') {
        const fw = await H.firmware();
        if (!fw) throw new Error('this page has no bundled firmware');
        ({ manifest, bytes } = fw);
      } else {
        const r = await fetch('firmware/manifest.json', { cache: 'no-store' });
        if (!r.ok) throw new Error('no bundled firmware (firmware/manifest.json: ' + r.status + ')');
        manifest = await r.json();
        const b = await fetch('firmware/' + encodeURIComponent(manifest.file), { cache: 'no-store' });
        if (!b.ok) throw new Error(`bundled firmware ${manifest.file}: HTTP ${b.status}`);
        bytes = new Uint8Array(await b.arrayBuffer());
      }
      const sum = await sha256(bytes);
      if (!sum) throw new Error('this browser cannot check the SHA-256 of the bundled firmware (no crypto.subtle), so it is not offered');
      if (sum !== String(manifest.sha256).toLowerCase()) throw new Error(`the bundled firmware is damaged (SHA-256 ${sum.slice(0, 16)}…, expected ${String(manifest.sha256).slice(0, 16)}…)`);
      const img = F.loadImage(manifest.file, bytes);
      img.version = manifest.version; img.sha256 = sum; img.label = `EasyVNA ${manifest.version}`;
      return { manifest, img };
    }
    function ensureBundled() {
      if (!U.fwLoading) {
        U.fwLoading = loadBundled().then((fw) => { U.fw = fw; }, (e) => { U.fwErr = e.message || String(e); }).then(render);
      }
      return U.fwLoading;
    }

    // ----- the chosen image -----
    function chosen() {
      if (U.choice === 'easyvna') return U.fw ? { img: U.fw.img, opts: { mode: 'firmware', flashSize: U.fw.manifest.flashSize } } : null;
      if (U.choice === 'file') return U.file ? { img: U.file, opts: { mode: 'firmware' } } : null;
      if (U.choice === 'restore') return U.restore ? { img: U.restore, opts: { mode: 'restore' } } : null;
      return null;
    }
    function verdict() {
      const c = chosen();
      if (!c || !U.dfu) return null;
      return F.validateImage(U.dfu.layout, c.img, c.opts);
    }

    // ----- USB -----
    async function connect() {
      if (!hasUSB) throw new Error(NO_USB);
      let dev;
      try {
        dev = await navigator.usb.requestDevice({ filters: [{ vendorId: F.VID, productId: F.PID }] });
      } catch (e) {
        if (e && e.name === 'NotFoundError') {
          U.driversOpen = true;
          throw new Error('No device chosen. Put the NanoVNA in DFU mode (see "How to enter DFU mode" below), plug it in and press Connect again: it shows up as "STM32 BOOTLOADER" (USB 0483:df11). ' +
            'Not in the list? Windows needs the WinUSB driver, Linux a udev rule: see "USB drivers and permissions" below.');
        }
        throw e;
      }
      await useDevice(dev);
    }
    async function useDevice(dev) {
      const dfu = new F.DfuSe(dev, { log: (m) => console.info('easyvna-web flasher: ' + m) });
      try { await dfu.connect(); } catch (e) {
        await dfu.close();
        if (e && (e.name === 'SecurityError' || e.name === 'NetworkError' || /access denied|unable to claim/i.test(e.message || ''))) {
          U.driversOpen = true;
          throw new Error(`Could not open the DFU device (${e.message}). On Linux install the udev rule, on Windows the WinUSB driver: see USB drivers and permissions below. Also close other programs using it (STM32CubeProgrammer, dfu-util).`);
        }
        throw e;
      }
      U.dev = dev; U.dfu = dfu; U.err = ''; U.result = null; U.phase = ''; U.helpOpen = null;
    }
    async function disconnect() {
      if (U.dfu) await U.dfu.close();
      U.dev = null; U.dfu = null;
    }
    if (hasUSB) {
      navigator.usb.addEventListener('disconnect', (e) => {
        if (!U.dev || e.device !== U.dev) return;
        U.dev = null; U.dfu = null;
        if (U.busy && U.busy !== 'leave') {
          U.err = `The NanoVNA was unplugged during "${U.busy}". ${INTERRUPTED}`;
          U.stop = true;
        }
        render();
      });
    }

    const INTERRUPTED = 'Nothing is lost for good: the DFU bootloader is in ROM, so the NanoVNA can always be put back in DFU mode and flashed again (EasyVNA, any NanoVNA-D release, or your backup).';

    // run wraps a long USB operation: one at a time, progress, errors.
    async function operation(name, fn) {
      if (U.busy) return;
      U.busy = name; U.stop = false; U.err = ''; U.result = null; U.prog = null;
      U.errTitle = { flash: 'Firmware update failed', backup: 'Backup failed', leave: 'Could not start the firmware' }[name] || 'Problem';
      render();
      try { await fn(); } catch (e) {
        console.warn('easyvna-web flasher:', e);
        if (e && e.code === 'stopped') U.err = `Stopped during ${U.phase || name}. ${U.phase === 'erase' || U.phase === 'write' ? 'The firmware is incomplete now: flash it again before you start it. ' : ''}${INTERRUPTED}`;
        else if (!U.err) {
          const msg = e && e.message ? e.message : String(e);
          const mid = U.phase === 'erase' || U.phase === 'write' || U.phase === 'verify';
          U.err = msg + (mid && !(e && e.code === 'protected') ? ` The NanoVNA stays in DFU mode: flash again. ${INTERRUPTED}` : '');
        }
        U.phase = U.phase === 'done' ? 'done' : 'failed';
      } finally {
        U.busy = ''; U.stop = false;
        render();
      }
    }
    const hooks = () => ({
      stopped: () => U.stop,
      progress: (done, total, addr) => { U.prog = { done, total, addr }; paintProgress(); },
      phase: (p) => { U.phase = p; U.prog = null; render(); },
    });

    // backup reads the whole flash, downloads it and keeps it in IndexedDB.
    async function backup() {
      const h = hooks();
      h.phase('backup');
      const data = await F.dumpFlash(U.dfu, h);
      const name = `nanovna-backup-${layoutId()}-${stamp(new Date())}.bin`;
      saveBlob(new Blob([data], { type: 'application/octet-stream' }), name);
      const key = devKey();
      if (key) U.dumped.set(key, name);
      const rec = { name, date: Date.now(), size: data.length, layout: layoutId(), serial: U.dev.serialNumber || '', sha256: await sha256(data), data };
      try { await IDB.put(rec); } catch (e) { console.warn('easyvna-web flasher: backup not kept in the browser:', e); }
      await loadBackups();
      return name;
    }

    async function doFlash(v, c) {
      await operation('flash', async () => {
        if (!devKey() || !U.dumped.has(devKey())) await backup();
        await F.flashPlan(U.dfu, v.plan, hooks());
        U.result = { img: c.img, plan: v.plan, mode: c.opts.mode };
      });
    }

    // ----- confirmation -----
    function confirmFlash() {
      const v = verdict(), c = chosen();
      if (!v || !v.ok) return;
      const t = v.target, p = v.plan, img = c.img, restore = c.opts.mode === 'restore';
      const last = p.pages[p.pages.length - 1];
      const key = devKey(), already = key && U.dumped.get(key);
      const box = document.createElement('div');
      box.id = 'fwAsk'; box.className = 'overlay ask'; box.setAttribute('role', 'alertdialog'); box.setAttribute('aria-modal', 'true');
      box.innerHTML = `<div class="askbox panel fwask">
        <h3>${restore ? 'Restore this full backup?' : `Flash ${esc(img.label || img.name)}?`}</h3>
        <ul class="fwlist">
          <li><b>Device:</b> ${esc(t.name)} (${t.mcu}, ${F.kb(t.flashSize)})${U.dev.serialNumber ? ' · S/N ' + esc(U.dev.serialNumber) : ''}</li>
          <li><b>Image:</b> ${esc(img.name)}${img.version ? ' · ' + esc(img.version) : ''} · ${F.fmtInt(img.data.length)} bytes${img.sha256 ? ` · SHA-256 ${img.sha256.slice(0, 12)}…` : ''}</li>
          <li><b>Erase:</b> ${p.pages.length} page${p.pages.length === 1 ? '' : 's'} of ${F.kb(p.pages[0].size)}, ${F.hex8(p.start)}–${F.hex8(last.start + last.size)}, one by one (never a full-chip erase)</li>
          <li>${restore ? `<b class="fwbad">The calibration and configuration are replaced</b> by those in the backup (${F.hex8(t.fwEnd)}–${F.hex8(p.start + t.flashSize)}).`
    : `<b>Calibration and configuration are kept</b> (${F.hex8(t.fwEnd)}–${F.hex8(F.FLASH_BASE + t.flashSize)}, ${t.slots} slots + config, not erased).`}</li>
          <li>${already ? `Backed up in this session: <code>${esc(already)}</code>.` : '<b>First, a full backup</b> of the flash is read and downloaded (about 10 seconds); then erase, write and verify.'}</li>
        </ul>
        ${restore ? '<label class="check fwcheck"><input type="checkbox" id="fwAskCheck"> I want to overwrite the calibration and configuration with this backup.</label>' : ''}
        <p class="small muted">Keep the USB cable plugged in. If it is interrupted anyway, the bootloader is in ROM: the NanoVNA can always be flashed again.</p>
        <div class="askbtns"><button id="fwAskNo" class="ghost" type="button">Cancel</button>
          <button id="fwAskYes" class="primary" type="button"${restore ? ' disabled' : ''}>${restore ? 'Restore the whole flash' : `Erase ${p.pages.length} pages and flash`}</button></div></div>`;
      document.body.appendChild(box);
      const close = () => box.remove();
      $('fwAskNo').onclick = close;
      $('fwAskNo').focus();
      const chk = $('fwAskCheck');
      if (chk) chk.onchange = () => { $('fwAskYes').disabled = !chk.checked; };
      $('fwAskYes').onclick = () => { close(); doFlash(v, c); };
    }

    // ----- rendering -----
    const PHASES = [['backup', 'Backup'], ['erase', 'Erase'], ['write', 'Write'], ['verify', 'Verify'], ['done', 'Done']];
    function paintProgress() {
      const bar = $('fwBar'), txt = $('fwProgText');
      if (!bar || !txt) return;
      const p = U.prog;
      const pct = p && p.total ? Math.round((100 * p.done) / p.total) : 0;
      bar.style.width = pct + '%';
      bar.parentElement.setAttribute('aria-valuenow', String(pct));
      const verb = { backup: 'Reading', erase: 'Erasing', write: 'Writing', verify: 'Verifying', read: 'Reading' }[U.phase] || '';
      txt.textContent = p ? `${verb} ${F.hex8(p.addr)} · ${F.kb(p.done)} of ${F.kb(p.total)} (${pct} %)` : `${verb}…`;
    }
    function helpHTML(open) {
      return `<details class="panel fwhelp" id="fwHelp"${open ? ' open' : ''}><summary>How to enter DFU mode</summary>
        <ol class="fwsteps">
          <li><b>NanoVNA-H4 in DFU mode.</b> From stock firmware: power the H4 off, hold the jog wheel pushed in and switch it on.
            The screen stays blank and the H4 shows up as USB <code>0483:df11</code> ("STM32 BOOTLOADER").
            From EasyVNA: type <code>reset dfu</code> in the USB serial shell, or use CONFIG &gt; DFU.
            The NanoVNA-H (F072) enters DFU the same way (jog wheel pushed in at power-on).</li>
          <li><b>EasyVNA Web's own connection.</b> In DFU mode the NanoVNA's serial port disappears, so the connection to it ends
            (and comes back after <b>Start firmware</b>). While it is connected, <b>Reboot into DFU</b> (step 1) sends <code>reset dfu</code> for you.
            Close other programs that use the DFU device (STM32CubeProgrammer, dfu-util).</li>
          <li><b>Connect (DFU) finds nothing or cannot open the device?</b> See <i>USB drivers and permissions</i> below
            (Linux: a udev rule; Windows: the WinUSB driver; macOS: nothing to install).</li>
        </ol></details>`;
    }
    // detectOS picks the drivers tab to show first.
    function detectOS() {
      const nav = root.navigator || {};
      const ua = String(nav.userAgent || '');
      const p = String((nav.userAgentData && nav.userAgentData.platform) || nav.platform || ua).toLowerCase();
      if (/cros/i.test(ua) || p === 'chrome os' || p === 'chromeos') return 'chromeos';
      if (/mac|iphone|ipad/.test(p)) return 'macos';
      if (/win/.test(p)) return 'windows';
      return 'linux';
    }
    const UDEV_RULES = `# /etc/udev/rules.d/70-nanovna.rules
# NanoVNA-H / H4 in normal mode: USB serial (Web Serial, the EasyVNA Web program)
SUBSYSTEM=="tty", ATTRS{idVendor}=="0483", ATTRS{idProduct}=="5740", TAG+="uaccess", ENV{ID_MM_DEVICE_IGNORE}="1"
# NanoVNA V2 / LiteVNA: USB serial
SUBSYSTEM=="tty", ATTRS{idVendor}=="04b4", ATTRS{idProduct}=="0008", TAG+="uaccess", ENV{ID_MM_DEVICE_IGNORE}="1"
# NanoVNA in DFU mode, "STM32 BOOTLOADER" (WebUSB firmware update)
SUBSYSTEM=="usb", ATTRS{idVendor}=="0483", ATTRS{idProduct}=="df11", TAG+="uaccess"`;
    const UDEV_INSTALL = `sudo tee /etc/udev/rules.d/70-nanovna.rules >/dev/null <<'EOF'
${UDEV_RULES.split('\n').slice(1).join('\n')}
EOF
sudo udevadm control --reload-rules && sudo udevadm trigger`;
    const copyBtn = (id) => `<button class="ghost small fwcopy" type="button" data-copy="${id}">Copy</button>`;
    const pre = (id, text) => `<div class="fwprewrap">${copyBtn(id)}<pre class="fwpre" id="${id}">${esc(text)}</pre></div>`;
    function driversHTML(open) {
      const os = U.os || (U.os = detectOS());
      const tabs = [['linux', 'Linux'], ['macos', 'macOS'], ['windows', 'Windows'], ['chromeos', 'ChromeOS']];
      const body = {
        linux: `<p>Chrome reads and writes the NanoVNA through <code>/dev</code>: your user needs permission on the
            serial port (normal mode, <code>0483:5740</code>) and on the USB device (DFU mode, <code>0483:df11</code>).
            Without it, the device is listed but cannot be opened (or is not listed at all).</p>
          <ol class="fwsteps">
            <li><b>Install the udev rule</b> (once): it gives the logged-in user access to the NanoVNA in both modes, and keeps
              ModemManager away from its serial port. Paste in a terminal:
              ${pre('fwUdevInstall', UDEV_INSTALL)}
              The file name must sort before <code>73-seat-late.rules</code> for <code>uaccess</code> to work: keep <code>70-</code>.</li>
            <li><b>Unplug the NanoVNA and plug it in again</b>, then press Connect.</li>
            <li><b>Check:</b> <code>lsusb -d 0483:</code> lists <code>0483:5740</code> (normal mode) or <code>0483:df11</code>
              (DFU mode, "STM Device in DFU Mode"). Nothing listed: try another (data) cable or USB port.</li>
            <li><b>Serial port only, without the rule:</b> add yourself to the serial group, then log out and in again:
              ${pre('fwGroup', 'sudo usermod -aG uucp "$USER"     # Arch, Manjaro\nsudo usermod -aG dialout "$USER"  # Debian, Ubuntu, Fedora, openSUSE')}
              The group does not cover DFU mode: the firmware update needs the rule.</li>
            <li><b>Chromium installed as a Snap</b> (Ubuntu): also allow raw USB, <code>sudo snap connect chromium:raw-usb</code>.</li>
          </ol>
          <details class="fwmore"><summary>The rule file itself</summary>${pre('fwUdevRules', UDEV_RULES)}</details>`,
        macos: `<p><b>Nothing to install.</b> macOS has the drivers for both modes built in, and Chrome or Edge can use them directly.</p>
          <ol class="fwsteps">
            <li><b>Normal mode</b>: the NanoVNA appears as a serial port (<code>/dev/cu.usbmodem…</code>); Connect lists it by name.</li>
            <li><b>DFU mode</b>: it appears as <b>STM32 BOOTLOADER</b> in the Connect (DFU) list.</li>
            <li><b>Not listed?</b> Check that macOS sees it: Apple menu &gt; About This Mac &gt; More Info &gt; System Report &gt; USB,
              or in a terminal:
              ${pre('fwMacCheck', 'system_profiler SPUSBDataType | grep -B2 -A6 -i -E "nanovna|stm32|0x0483"')}
              Not there: use a data cable (some only charge), another port, or no hub. On some Macs, allow the accessory
              to connect when macOS asks (System Settings &gt; Privacy &amp; Security &gt; Allow accessories to connect).</li>
          </ol>`,
        windows: `<ol class="fwsteps">
            <li><b>Normal mode: nothing to install.</b> Windows 10 and 11 use their built-in driver: the NanoVNA is a
              <i>USB Serial Device (COMx)</i> under Device Manager &gt; Ports. Never put WinUSB on it: Web Serial would lose it.</li>
            <li><b>DFU mode: install WinUSB once</b> (a browser can only reach a USB device that uses the WinUSB driver;
              Windows installs none, or ST's own DFU driver if you have STM32CubeProgrammer):
              <ol class="fwsub">
                <li>Put the NanoVNA in DFU mode and plug it in.</li>
                <li>Download and run <a href="https://zadig.akeo.ie/" target="_blank" rel="noopener">Zadig</a> (no install needed; allow it to change the system).</li>
                <li>Menu <b>Options &gt; List All Devices</b>.</li>
                <li>In the list, pick <b>STM32 BOOTLOADER</b>. <b>Check that USB ID reads <code>0483 DF11</code></b>; if it shows anything else
                  (your mouse, keyboard, the NanoVNA's <code>0483 5740</code>…), stop: that is the wrong device.</li>
                <li>On the right of the green arrow choose <b>WinUSB</b>, then press <b>Replace Driver</b> (or <b>Install Driver</b>). It takes up to a minute.</li>
                <li>Back here, press <b>Connect (DFU)</b>. Windows keeps this driver for the NanoVNA's DFU mode on this PC: you do this once.</li>
              </ol></li>
            <li><b>dfu-util</b> works with WinUSB too. <b>STM32CubeProgrammer</b> needs ST's driver: to go back, Device Manager &gt;
              STM32 BOOTLOADER &gt; Uninstall device, tick <i>Attempt to remove the driver</i>, then unplug and plug in again.</li>
            <li><b>WinUSB landed on the wrong device</b> (e.g. the COM port is gone): Device Manager &gt; that device &gt; Uninstall device,
              tick <i>Attempt to remove the driver</i>, unplug and plug in again; Windows puts its own driver back.</li>
          </ol>`,
        chromeos: `<p><b>Nothing to install.</b> Chrome on a Chromebook reaches the NanoVNA in both modes directly: press Connect
            (normal mode) or Connect (DFU) and pick it in the list. If ChromeOS asks where to connect a new USB device, choose Chrome, not Linux.</p>`,
      }[os];
      return `<details class="panel fwhelp fwdrivers" id="fwDrivers"${open ? ' open' : ''}><summary>USB drivers and permissions (Linux, macOS, Windows)</summary>
        <div class="fwtabs" role="tablist">${tabs.map(([k, n]) => `<button type="button" role="tab" class="fwtab${k === os ? ' on' : ''}" aria-selected="${k === os}" data-os="${k}">${n}</button>`).join('')}</div>
        <div class="fwtabbody" id="fwDriversBody" data-os="${os}">${body}</div></details>`;
    }
    function licenceHTML() {
      if (!U.fw) return '';
      const m = U.fw.manifest;
      return `<p class="small muted fwlic">The bundled firmware, ${esc(m.file)} (EasyVNA ${esc(m.version)}, for the ${esc(m.target)}), is free software under the
        ${esc(m.license || 'GPL-3.0')} licence: <a href="${esc(m.source)}" target="_blank" rel="noopener">source code and release</a>. It comes with no warranty.</p>`;
    }
    // serialHTML: the app's own (serial) connection to the NanoVNA, with
    // "Reboot into DFU" when the firmware has a reset command.
    function serialHTML() {
      const conn = appConnected();
      if (!conn) return '';
      return `<div class="small fwserial" id="fwSerial">EasyVNA Web is connected to ${esc(conn.info.model)} over USB serial now.
        ${dfuRebootAvailable() ? '<button id="fwRebootDFU" class="ghost small" type="button">Reboot into DFU</button> sends it <code>reset dfu</code> (firmware with DFU support); the serial connection ends.'
    : 'Put it in DFU mode on the device itself: the serial connection ends by itself.'}</div>`;
    }
    function deviceHTML() {
      if (!hasUSB) {
        return `<div class="wbanner bad" id="fwNoUSB"><span class="wi">!</span><div><b>${NO_USB}</b>This browser has no WebUSB, so it cannot reach the NanoVNA in DFU mode.
          ${root.isSecureContext === false ? ' WebUSB also needs a secure page (https://, http://localhost or a local file).' : ''}
          The rest of EasyVNA Web works here; to update the firmware use Chrome or Edge, or <code>dfu-util</code> (see the firmware's README).</div></div>`;
      }
      if (!U.dfu) {
        return `<div class="panel fwdev"><div class="step"><span class="num">1</span>Connect the NanoVNA in DFU mode</div>
          <div class="stepsub">Put it in DFU mode (see <i>How to enter DFU mode</i> below), plug it in, then press Connect and pick <b>STM32 BOOTLOADER</b>.</div>
          <div class="row"><button id="fwConnect" class="primary bigbtn" type="button"${U.busy ? ' disabled' : ''}>Connect (DFU)</button></div>${serialHTML()}</div>`;
      }
      const L = U.dfu.layout, t = U.dfu.target;
      return `<div class="panel fwdev"><div class="step"><span class="num">1</span>Connected</div>
        <div class="fwchips"><span class="fwchip ${t ? 'good' : 'bad'}" id="fwTarget">${t ? `${esc(t.name)} · ${t.mcu} · ${F.kb(t.flashSize)}` : `unknown device · ${F.kb(L.size)}`}</span>
          <span class="fwchip">${esc(U.dev.productName || 'DFU device')}${U.dev.serialNumber ? ' · S/N ' + esc(U.dev.serialNumber) : ''}</span></div>
        <div class="small muted" id="fwLayout">${esc(F.describeLayout(L))} · transfers of ${U.dfu.xfer} bytes</div>
        ${t ? '' : '<div class="small fwbad">EasyVNA Web does not know this flash layout: it can back it up, but it will not write to it.</div>'}
        <div class="row"><button id="fwStart" class="ghost" type="button"${U.busy ? ' disabled' : ''} title="Leave DFU mode and run the firmware in flash">Start firmware</button>
          <button id="fwDisconnect" class="ghost" type="button"${U.busy ? ' disabled' : ''}>Disconnect</button></div></div>`;
    }
    function backupHTML() {
      const key = U.dfu ? devKey() : '';
      const done = key && U.dumped.get(key);
      const list = U.backups.map((b) => `<tr><td><code>${esc(b.name)}</code></td><td>${new Date(b.date).toLocaleString()}</td><td>${F.kb(b.size)}</td>
        <td><button class="ghost small" type="button" data-dl="${esc(b.name)}">Download</button>
        <button class="ghost small" type="button" data-restore="${esc(b.name)}">Restore…</button></td></tr>`).join('');
      return `<div class="panel"><div class="step"><span class="num">2</span>Back up</div>
        <div class="stepsub">Reads the whole flash (firmware, calibration, configuration) into a <code>.bin</code> file. A backup is made by itself before any write.</div>
        <div class="row"><button id="fwBackup" class="${U.dfu && !done ? 'primary' : 'ghost'}" type="button"${!U.dfu || U.busy ? ' disabled' : ''}>Back up the flash${U.dfu ? ` (${F.kb(U.dfu.layout.size)})` : ''}</button>
          <span class="small muted" id="fwBackupState">${done ? `✓ backed up in this session: <code>${esc(done)}</code>` : U.dfu ? 'not backed up yet in this session' : 'connect first'}</span></div>
        ${list ? `<div class="small muted">Latest backups kept in this browser (also saved as files):</div><div class="histwrap"><table class="hist fwbackups"><tbody>${list}</tbody></table></div>` : ''}</div>`;
    }
    function choiceHTML() {
      const m = U.fw && U.fw.manifest;
      const bundled = m ? `<b>EasyVNA ${esc(m.version)}</b> (bundled) · for the ${esc(m.target)} · ${F.fmtInt(m.size)} bytes`
        : U.fwErr ? `<b>EasyVNA</b> (bundled): <span class="fwbad">${esc(U.fwErr)}</span>` : '<b>EasyVNA</b> (bundled): loading…';
      const f = U.file, r = U.restore;
      const v = verdict(), c = chosen();
      const checks = v ? `<ul class="fwchecks" id="fwChecks">${v.checks.map((x) => `<li class="${x.ok ? 'ok' : 'bad'}"><span>${x.ok ? '✓' : '✗'}</span>${esc(x.text)}</li>`).join('')}</ul>` : '';
      const why = !c ? (U.choice === 'easyvna' ? (U.fwErr ? '' : 'Loading the bundled firmware…') : 'Choose a file.') : !U.dfu ? 'Connect the NanoVNA (step 1) to check this image against it.' : '';
      const plan = v && v.ok ? `<div class="small muted" id="fwPlan">Will erase ${v.plan.pages.length} pages (${F.kb(v.plan.eraseBytes)}), write ${F.fmtInt(v.plan.length)} bytes and read them back.</div>` : '';
      return `<div class="panel"><div class="step"><span class="num">3</span>Flash</div>
        <div class="choices fwchoices" role="radiogroup" aria-label="Firmware to flash">
          <label><input type="radio" name="fwChoice" value="easyvna"${U.choice === 'easyvna' ? ' checked' : ''}><span>${bundled}</span></label>
          <label><input type="radio" name="fwChoice" value="file"${U.choice === 'file' ? ' checked' : ''}><span><b>A firmware file</b> (.bin, .hex or .dfu), e.g. a NanoVNA-D release.
            ${f ? `<code>${esc(f.name)}</code> · ${F.fmtInt(f.data.length)} bytes · ${f.format}` : ''}${U.fileErr ? `<span class="fwbad"> ${esc(U.fileErr)}</span>` : ''}
            <button id="fwPickFile" class="ghost small" type="button">Choose file…</button></span></label>
          <label><input type="radio" name="fwChoice" value="restore"${U.choice === 'restore' ? ' checked' : ''}><span><b>Restore a full backup</b> (writes the calibration and configuration too).
            ${r ? `<code>${esc(r.name)}</code> · ${F.fmtInt(r.data.length)} bytes` : ''}${U.restoreErr ? `<span class="fwbad"> ${esc(U.restoreErr)}</span>` : ''}
            <button id="fwPickBackup" class="ghost small" type="button">Choose backup…</button></span></label>
        </div>
        <input type="file" id="fwFile" accept=".bin,.hex,.ihex,.dfu" hidden>
        <input type="file" id="fwBackupFile" accept=".bin" hidden>
        ${checks}${why ? `<div class="small muted">${why}</div>` : ''}${plan}
        <div class="row"><button id="fwFlash" class="primary bigbtn" type="button"${v && v.ok && !U.busy ? '' : ' disabled'}>${U.choice === 'restore' ? 'Restore…' : 'Flash…'}</button></div></div>`;
    }
    function progressHTML() {
      if (!U.busy && !U.phase) return '';
      const idx = PHASES.findIndex((p) => p[0] === U.phase);
      const list = U.busy === 'backup' || (!U.busy && U.phase === 'backup') ? PHASES.slice(0, 1) : U.busy === 'leave' ? [] : PHASES;
      const steps = list.map(([k, l], i) => `<span class="fwph${k === U.phase ? ' on' : ''}${idx > i || U.phase === 'done' ? ' past' : ''}">${l}</span>`).join('<span class="arrow" aria-hidden="true">›</span>');
      if (U.busy) {
        return `<div class="panel fwprog" id="fwProgress" aria-live="polite"><div class="fwphases">${steps}</div>
          <div class="fwbarwrap" role="progressbar" aria-valuemin="0" aria-valuemax="100" aria-valuenow="0"><div class="fwbar" id="fwBar"></div></div>
          <div class="row"><span id="fwProgText" class="fwprogtext"></span><span class="spacer"></span><button id="fwStop" class="ghost small" type="button"${U.stop ? ' disabled' : ''}>Stop</button></div>
          <p class="note">Keep the USB cable plugged in. If this is interrupted (Stop, unplugged, tab closed), ${INTERRUPTED.charAt(0).toLowerCase() + INTERRUPTED.slice(1)}
            The calibration area is not touched${U.choice === 'restore' && U.busy === 'flash' ? ' except by a restore' : ''}.</p></div>`;
      }
      if (U.result) {
        const r = U.result;
        return `<div class="wbanner good fwdone" id="fwDone"><span class="wi">✓</span><div><b>${r.mode === 'restore' ? 'Backup restored and verified' : `Flashed and verified: ${esc(r.img.label || r.img.name)}`}</b>
          ${F.fmtInt(r.plan.length)} bytes, ${r.plan.pages.length} pages erased and written, read back identical.
          ${r.mode === 'restore' ? 'Calibration and configuration are those of the backup.' : 'Calibration and configuration were kept.'}
          <div class="row"><button id="fwStart2" class="primary bigbtn" type="button"${U.dfu ? '' : ' disabled'}>Start firmware</button>
          <span class="small muted">leaves DFU mode; the NanoVNA restarts and EasyVNA Web can connect to it again.</span></div></div></div>`;
      }
      return '';
    }
    function render() {
      if (view.hidden) return;
      const err = U.err ? `<div class="wbanner bad" id="fwError" role="alert"><span class="wi">!</span><div><b>${esc(U.errTitle || 'Problem')}</b>${esc(U.err)}</div></div>` : '';
      body.innerHTML = `<div class="stepsub">Back up and update the firmware of a NanoVNA-H4 (or NanoVNA-H) over USB, in DFU mode, with the STM32's built-in bootloader.
        The calibration and configuration are kept.</div>
        ${err}${progressHTML()}${deviceHTML()}${hasUSB ? backupHTML() + choiceHTML() : ''}${helpHTML(U.helpOpen != null ? U.helpOpen : !U.dfu)}${driversHTML(!!U.driversOpen)}${licenceHTML()}`;
      paintProgress();
      bind();
      // Bring a new error, progress or result into view.
      const top = $('fwError') || $('fwProgress') || $('fwDone');
      const sig = top ? top.id + (U.err || '') : '';
      if (top && sig !== U.shown && top.scrollIntoView) top.scrollIntoView({ block: 'nearest' });
      U.shown = sig;
    }
    function bind() {
      const on = (id, fn) => { const el = $(id); if (el) el.onclick = fn; };
      const act = (fn) => async () => { try { U.errTitle = 'Problem'; await fn(); } catch (e) { U.err = e.message || String(e); } render(); };
      on('fwConnect', act(connect));
      on('fwDisconnect', act(disconnect));
      const start = () => operation('leave', async () => {
        U.phase = '';
        await U.dfu.leave(F.FLASH_BASE);
        U.dev = null; U.dfu = null; U.result = null;
        toastMsg('Firmware started: the NanoVNA restarts');
        const st = appState();
        if (st && !st.connected) setTimeout(() => root.fetch('api/connect', { method: 'POST' }).catch(() => {}), 2500);
      });
      on('fwStart', start); on('fwStart2', start);
      on('fwBackup', () => operation('backup', async () => { await backup(); U.phase = ''; toastMsg('Backup saved'); }));
      on('fwStop', () => { U.stop = true; render(); });
      on('fwFlash', confirmFlash);
      on('fwPickFile', (e) => { e.preventDefault(); $('fwFile').click(); });
      on('fwPickBackup', (e) => { e.preventDefault(); $('fwBackupFile').click(); });
      on('fwRebootDFU', act(rebootDFU));
      const help = $('fwHelp');
      if (help) help.ontoggle = () => { U.helpOpen = help.open; };
      const drv = $('fwDrivers');
      if (drv) drv.ontoggle = () => { U.driversOpen = drv.open; };
      for (const b of body.querySelectorAll('.fwtab')) b.onclick = () => { U.os = b.dataset.os; U.driversOpen = true; render(); };
      for (const b of body.querySelectorAll('[data-copy]')) {
        b.onclick = async () => {
          const el = $(b.dataset.copy);
          try { await root.navigator.clipboard.writeText(el.textContent); toastMsg('Copied'); } catch (e) {
            const r = document.createRange(); r.selectNodeContents(el); const sel = root.getSelection(); sel.removeAllRanges(); sel.addRange(r);
            toastMsg('Selected: press Ctrl+C to copy');
          }
        };
      }
      for (const r of body.querySelectorAll('input[name=fwChoice]')) r.onchange = () => { U.choice = r.value; render(); };
      const readFile = async (input, setter) => {
        const f = input.files[0];
        input.value = '';
        if (!f) return;
        setter(f.name, new Uint8Array(await f.arrayBuffer()));
        render();
      };
      $('fwFile') && ($('fwFile').onchange = (e) => readFile(e.target, (name, b) => {
        U.choice = 'file'; U.file = null; U.fileErr = '';
        try { U.file = F.loadImage(name, b); } catch (err) { U.fileErr = err.message; }
      }));
      $('fwBackupFile') && ($('fwBackupFile').onchange = (e) => readFile(e.target, (name, b) => {
        U.choice = 'restore'; U.restore = null; U.restoreErr = '';
        try { U.restore = F.loadImage(/\.bin$/i.test(name) ? name : name + '.bin', b); U.restore.name = name; } catch (err) { U.restoreErr = err.message; }
      }));
      for (const b of body.querySelectorAll('[data-dl]')) {
        b.onclick = () => { const r = U.backups.find((x) => x.name === b.dataset.dl); if (r) saveBlob(new Blob([r.data], { type: 'application/octet-stream' }), r.name); };
      }
      for (const b of body.querySelectorAll('[data-restore]')) {
        b.onclick = () => {
          const r = U.backups.find((x) => x.name === b.dataset.restore);
          if (!r) return;
          U.choice = 'restore'; U.restoreErr = '';
          U.restore = { ...F.loadImage(r.name, r.data), name: r.name };
          render();
          const el = $('fwFlash'); if (el) el.scrollIntoView({ block: 'center' });
        };
      }
    }

    // ----- Reboot into DFU over the existing serial connection -----
    function dfuRebootAvailable() { const st = appConnected(); return !!(st && st.info && st.info.dfuReboot); }
    async function rebootDFU() {
      const ok = typeof root.ask === 'function'
        ? await root.ask('Reboot the NanoVNA into DFU mode?', 'EasyVNA Web sends "reset dfu" to the NanoVNA. It restarts in DFU mode (blank screen) and the serial connection ends. Then press Connect (DFU).', 'Reboot into DFU')
        : true;
      if (!ok) return;
      const r = await fetch('api/dfu', { method: 'POST' });
      if (!r.ok) { let m = r.statusText; try { m = (await r.json()).error || m; } catch { /* not JSON */ } throw new Error(m); }
      toastMsg('Rebooting into DFU mode…');
    }

    // ----- open / close -----
    async function open() {
      view.hidden = false;
      document.body.classList.add('locked');
      if (!U.opened) { U.opened = true; await loadBackups(); }
      ensureBundled();
      render();
      const f = $('fwClose'); if (f) f.focus();
    }
    async function close() {
      if (U.busy && U.busy !== 'leave') {
        const ok = typeof root.ask === 'function' ? await root.ask('Stop the firmware update?', 'An operation is running. Closing stops it; the NanoVNA stays in DFU mode and can be flashed again.', 'Stop and close') : true;
        if (!ok) return;
        U.stop = true;
      }
      view.hidden = true;
      if (typeof root.lockBody === 'function') root.lockBody(); else document.body.classList.remove('locked');
    }
    // Follow the app's serial connection (step 1 mentions it) while open.
    let connSig = '';
    setInterval(() => {
      if (view.hidden || U.busy) return;
      const c = appConnected(), sig = c ? `${c.info.model}|${!!c.info.dfuReboot}` : '';
      if (sig !== connSig) { connSig = sig; render(); }
    }, 1000);
    $('btnFirmware').onclick = open;
    $('fwClose').onclick = close;
    document.addEventListener('keydown', (e) => {
      if (e.key !== 'Escape' || view.hidden) return;
      const a = $('fwAsk');
      if (a) { a.remove(); e.stopImmediatePropagation(); return; }
      const ask = $('askView');
      if (ask && !ask.hidden) return;
      e.stopImmediatePropagation();
      close();
    }, true);
    root.addEventListener('beforeunload', (e) => { if (U.busy && U.busy !== 'leave') { e.preventDefault(); e.returnValue = ''; } });
    root.EasyVNAFlasherUI = { open, state: U };
  }
})(typeof window !== 'undefined' ? window : globalThis);
