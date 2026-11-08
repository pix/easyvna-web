'use strict';
// A simulated STM32 system bootloader in DFU mode (USB 0483:df11, DfuSe)
// that looks like a WebUSB USBDevice, for the flasher's tests: node unit
// tests (require) and the Playwright e2e (injected into the page, where
// installFakeUSB replaces navigator.usb). Never talks to real hardware.
//
// It follows the DFU 1.1 state machine with ST's DfuSe extensions:
//   DNLOAD -> dfuDNLOAD-SYNC -> (GETSTATUS: start, bwPollTimeout) dfuDNBUSY
//   -> (GETSTATUS after the poll time) dfuDNLOAD-IDLE; UPLOAD only from
//   dfuIDLE / dfuUPLOAD-IDLE (so an ABORT is needed after a set-address);
//   errors go to dfuERROR until CLRSTATUS. Block 0 carries the commands
//   (0x21 set address, 0x41 page erase, 0x41 alone = mass erase, 0x92 read
//   unprotect); blocks >= 2 write/read at pointer + (n-2) * transfer size.
// Flash behaves like the real thing: a half-word can only be programmed
// when it is erased (0xFFFF), else errPROG.
(function (root) {
  const ST = { dfuIDLE: 2, dfuDNLOAD_SYNC: 3, dfuDNBUSY: 4, dfuDNLOAD_IDLE: 5, dfuMANIFEST_SYNC: 6, dfuMANIFEST: 7, dfuUPLOAD_IDLE: 9, dfuERROR: 10 };
  const ERR = { OK: 0, errTARGET: 1, errWRITE: 3, errERASE: 4, errPROG: 6, errADDRESS: 8, errVENDOR: 11, errUNKNOWN: 14, errSTALLEDPKT: 15 };
  const BASE = 0x08000000;

  // Deterministic filler bytes.
  function prng(seed) { let x = seed >>> 0 || 1; return () => { x ^= x << 13; x >>>= 0; x ^= x >>> 17; x ^= x << 5; x >>>= 0; return x & 0xff; }; }

  // fakeFirmware returns a plausible image: vector table (SP in RAM, odd
  // reset vector inside it), then code-like bytes.
  function fakeFirmware(size, seed = 7) {
    const b = new Uint8Array(size);
    const r = prng(seed);
    for (let i = 0; i < size; i++) b[i] = r();
    const w = (o, v) => { b[o] = v & 0xff; b[o + 1] = (v >>> 8) & 0xff; b[o + 2] = (v >>> 16) & 0xff; b[o + 3] = (v >>> 24) & 0xff; };
    w(0, 0x20000100); w(4, BASE + 0x1a1);
    for (let i = 8; i < 0x180; i += 4) w(i, BASE + 0x9b1);
    return b;
  }

  // Variants: H4 (STM32F303, 256 KB), H (STM32F072, 128 KB).
  const VARIANTS = {
    f303: { flashSize: 256 * 1024, fwEnd: 0x23800, slots: 7, slotSize: 0x4000, layout: '@Internal Flash  /0x08000000/128*0002Kg', serial: '2061366E5041', fw: 140 * 1024 },
    f072: { flashSize: 128 * 1024, fwEnd: 0x18000, slots: 5, slotSize: 0x1800, layout: '@Internal Flash  /0x08000000/064*0002Kg', serial: '3276397A3233', fw: 90 * 1024 },
  };

  class SimDfuSe {
    // opts: variant ('f303' | 'f072'), protected (RDP level 1), xfer,
    // pollErase / pollWrite (ms, bwPollTimeout), corruptAt (absolute
    // address whose byte is stored flipped when written), serial,
    // noInterfaceName (the alternate's interfaceName is empty, as on some
    // platforms: the name must be read from the string descriptors),
    // noFunctional (no DFU functional descriptor: default transfer size).
    constructor(opts = {}) {
      const v = VARIANTS[opts.variant || 'f303'];
      this.v = v;
      this.opts = opts;
      this.vendorId = 0x0483; this.productId = 0xdf11;
      this.productName = 'STM32  BOOTLOADER'; this.manufacturerName = 'STMicroelectronics';
      this.serialNumber = opts.serial !== undefined ? opts.serial : v.serial;
      this.xfer = opts.xfer || 2048;
      this.flash = new Uint8Array(v.flashSize).fill(0xff);
      this.flash.set(fakeFirmware(v.fw, 7), 0);
      this.calArea = v.fwEnd;
      // Save area: calibration slots and config, recognisable content.
      const enc = (s) => Array.from(s, (c) => c.charCodeAt(0));
      for (let i = 0; i < v.slots; i++) {
        const o = v.fwEnd + i * v.slotSize, r = prng(1000 + i);
        this.flash.set(enc(`CALIBRATION SLOT ${i}`), o);
        for (let k = 64; k < v.slotSize - 512; k++) this.flash[o + k] = r();
      }
      this.flash.set(enc('CONFIG NanoVNA'), v.flashSize - 0x800);
      this.state = ST.dfuIDLE; this.status = ERR.OK;
      this.ptr = BASE; this.pending = null; this.busyUntil = 0; this.poll = 0;
      this.opened = false; this.claimed = false; this.alt = 0; this.gone = false;
      this.log = []; this.erased = []; this.writes = 0; this.reads = 0; this.pollViolations = 0; this.massErase = 0; this.unprotect = 0;
      this.left = false;
      const names = [v.layout, '@Option Bytes  /0x1FFFF800/01*016 e', '@Device Feature/0xFFFF0000/01*004 e'];
      this.altNames = names;
      this.configuration = null;
      this.configurations = [this.cfg()];
    }
    cfg() {
      return {
        configurationValue: 1, configurationName: null,
        interfaces: [{
          interfaceNumber: 0, get claimed() { return false; },
          alternate: null,
          alternates: this.altNames.map((n, i) => ({ alternateSetting: i, interfaceClass: 0xfe, interfaceSubclass: 1, interfaceProtocol: 2,
            interfaceName: this.opts.noInterfaceName ? null : n, endpoints: [] })),
        }],
      };
    }
    get flashSize() { return this.v.flashSize; }
    gone_() { const e = new Error('The device was disconnected.'); e.name = 'NetworkError'; return e; }
    check(needClaim) {
      if (this.gone) throw this.gone_();
      if (!this.opened) { const e = new Error('The device must be opened first.'); e.name = 'InvalidStateError'; throw e; }
      if (needClaim && !this.claimed) { const e = new Error('The interface is not claimed.'); e.name = 'InvalidStateError'; throw e; }
    }
    async open() { if (this.gone) throw this.gone_(); this.opened = true; this.log.push('open'); }
    async close() { this.opened = false; this.claimed = false; this.log.push('close'); }
    async selectConfiguration(n) { this.check(); if (n !== 1) throw new Error('no such configuration'); this.configuration = this.configurations[0]; this.log.push('config 1'); }
    async claimInterface(n) { this.check(); if (!this.configuration || n !== 0) throw new Error('no such interface'); this.claimed = true; this.log.push('claim 0'); }
    async releaseInterface() { this.claimed = false; }
    async selectAlternateInterface(n, alt) { this.check(true); if (alt >= this.altNames.length) throw new Error('no such alternate'); this.alt = alt; this.log.push('alt ' + alt); }
    async reset() { this.check(); }

    // ----- descriptors -----
    configDescriptor() {
      const d = [9, 2, 0, 0, 1, 1, 0, 0xc0, 50];
      this.altNames.forEach((n, i) => d.push(9, 4, 0, i, 0, 0xfe, 1, 2, 4 + i));
      if (!this.opts.noFunctional) d.push(9, 0x21, 0x0b, 0xff, 0x00, this.xfer & 0xff, this.xfer >> 8, 0x1a, 0x01);
      d[2] = d.length & 0xff; d[3] = d.length >> 8;
      return new Uint8Array(d);
    }
    stringDescriptor(i) {
      const s = i === 0 ? null : i >= 4 && i - 4 < this.altNames.length ? this.altNames[i - 4] : 'STM32  BOOTLOADER';
      if (s === null) return new Uint8Array([4, 3, 0x09, 0x04]);
      const d = [2 + 2 * s.length, 3];
      for (const c of s) d.push(c.charCodeAt(0), 0);
      return new Uint8Array(d);
    }

    // ----- control transfers -----
    stall(status = ERR.errSTALLEDPKT) { this.state = ST.dfuERROR; this.status = status; return { status: 'stall' }; }
    async controlTransferIn(setup, length) {
      this.check();
      if (setup.requestType === 'standard' && setup.recipient === 'device' && setup.request === 6) {
        const type = setup.value >> 8, idx = setup.value & 0xff;
        const d = type === 2 ? this.configDescriptor() : type === 3 ? this.stringDescriptor(idx) : null;
        if (!d) return { status: 'stall' };
        return this.reply(d.subarray(0, Math.min(length, d.length)));
      }
      this.check(true);
      if (setup.requestType !== 'class' || setup.recipient !== 'interface' || setup.index !== 0) return { status: 'stall' };
      switch (setup.request) {
        case 3: return this.reply(this.getStatus());
        case 5: return this.reply(new Uint8Array([this.state]));
        case 2: return this.upload(setup.value, length);
        default: return this.stall();
      }
    }
    async controlTransferOut(setup, data) {
      this.check(true);
      if (setup.requestType !== 'class' || setup.recipient !== 'interface' || setup.index !== 0) return { status: 'stall' };
      const b = data ? new Uint8Array(data.buffer ? data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) : data) : new Uint8Array(0);
      switch (setup.request) {
        case 1: return this.dnload(setup.value, b);
        case 4: // CLRSTATUS
          if (this.state !== ST.dfuERROR) return this.stall();
          this.state = ST.dfuIDLE; this.status = ERR.OK; this.log.push('clrstatus');
          return { status: 'ok', bytesWritten: 0 };
        case 6: // ABORT
          if (this.state === ST.dfuERROR || this.state === ST.dfuDNBUSY) return this.stall();
          this.state = ST.dfuIDLE; this.pending = null; this.log.push('abort');
          return { status: 'ok', bytesWritten: 0 };
        default: return this.stall();
      }
    }
    reply(u8) { const c = new Uint8Array(u8); return { status: 'ok', data: new DataView(c.buffer) }; }

    dnload(block, b) {
      if (this.state !== ST.dfuIDLE && this.state !== ST.dfuDNLOAD_IDLE) return this.stall();
      if (b.length > this.xfer) return this.stall();
      if (b.length === 0) { // leave DFU (manifestation)
        this.state = ST.dfuMANIFEST_SYNC; this.log.push('leave ' + block);
        return { status: 'ok', bytesWritten: 0 };
      }
      if (block === 1) return this.stall();
      this.pending = { block, data: b };
      this.state = ST.dfuDNLOAD_SYNC;
      return { status: 'ok', bytesWritten: b.length };
    }
    getStatus() {
      // A host that polls before bwPollTimeout is over is counted (1 ms of
      // slack for timer granularity).
      const now = (root.performance || Date).now();
      if (this.state === ST.dfuDNLOAD_SYNC) {
        const op = this.pending; this.pending = null;
        const res = this.execute(op);
        this.poll = res.poll; this.busyUntil = now + res.poll; this.result = res.err;
        this.state = ST.dfuDNBUSY;
      } else if (this.state === ST.dfuDNBUSY) {
        if (now < this.busyUntil - 1) { this.pollViolations++; } else if (this.result) {
          this.state = ST.dfuERROR; this.status = this.result; this.result = 0;
        } else this.state = ST.dfuDNLOAD_IDLE;
      } else if (this.state === ST.dfuMANIFEST_SYNC) {
        this.state = ST.dfuMANIFEST;
        this.left = true; this.log.push('manifest: jump to 0x' + this.ptr.toString(16));
        setTimeout(() => this.detach(), 5);
      }
      const p = this.state === ST.dfuDNBUSY ? this.poll : 0;
      return new Uint8Array([this.status, p & 0xff, (p >> 8) & 0xff, (p >> 16) & 0xff, this.state, 0]);
    }
    detach() {
      this.gone = true; this.opened = false;
      if (this.onGone) this.onGone(this);
    }
    inFlash(a, n) { return a >= BASE && a + n <= BASE + this.v.flashSize; }
    execute(op) {
      const b = op.data;
      if (op.block === 0) {
        const addr = b.length >= 5 ? (b[1] | (b[2] << 8) | (b[3] << 16) | (b[4] << 24)) >>> 0 : 0;
        switch (b[0]) {
          case 0x21:
            if (b.length !== 5) return { poll: 1, err: ERR.errTARGET };
            this.ptr = addr; this.log.push('setaddr 0x' + addr.toString(16));
            return { poll: 1, err: 0 };
          case 0x41:
            if (b.length === 1) { this.massErase++; this.log.push('MASS ERASE'); this.flash.fill(0xff); return { poll: 100, err: 0 }; }
            if (this.opts.protected) return { poll: 1, err: ERR.errVENDOR };
            if (b.length !== 5 || !this.inFlash(addr, 1) || (addr - BASE) % 2048) return { poll: 1, err: ERR.errTARGET };
            this.flash.fill(0xff, addr - BASE, addr - BASE + 2048);
            this.erased.push(addr); this.log.push('erase 0x' + addr.toString(16));
            return { poll: this.opts.pollErase != null ? this.opts.pollErase : 3, err: 0 };
          case 0x92:
            this.unprotect++; this.log.push('READ UNPROTECT'); return { poll: 1, err: 0 };
          default:
            return { poll: 1, err: ERR.errSTALLEDPKT };
        }
      }
      const addr = this.ptr + (op.block - 2) * this.xfer;
      if (this.opts.protected) return { poll: 1, err: ERR.errVENDOR };
      if (!this.inFlash(addr, b.length)) return { poll: 1, err: ERR.errADDRESS };
      const o = addr - BASE;
      for (let i = 0; i < b.length; i += 2) {
        const cur = this.flash[o + i] | ((i + 1 < b.length ? this.flash[o + i + 1] : 0xff) << 8);
        const nw = b[i] | ((i + 1 < b.length ? b[i + 1] : 0xff) << 8);
        if (cur !== 0xffff && nw !== 0xffff) { this.log.push('PROG ERROR at 0x' + (addr + i).toString(16)); return { poll: 1, err: ERR.errPROG }; }
      }
      this.flash.set(b, o);
      const c = this.opts.corruptAt;
      if (c != null && c >= addr && c < addr + b.length) this.flash[c - BASE] ^= 0x01;
      this.writes++; this.log.push(`write 0x${addr.toString(16)} +${b.length}`);
      return { poll: this.opts.pollWrite != null ? this.opts.pollWrite : 1, err: 0 };
    }
    upload(block, length) {
      if (this.state !== ST.dfuIDLE && this.state !== ST.dfuUPLOAD_IDLE) return this.stall();
      if (block === 0) { this.state = ST.dfuUPLOAD_IDLE; return this.reply(new Uint8Array([0x00, 0x21, 0x41, 0x92]).subarray(0, length)); }
      if (block === 1 || length > this.xfer) return this.stall();
      if (this.opts.protected) return this.stall(ERR.errVENDOR);
      const addr = this.ptr + (block - 2) * this.xfer;
      if (!this.inFlash(addr, length)) return this.stall(ERR.errADDRESS);
      this.state = ST.dfuUPLOAD_IDLE; this.reads++;
      return this.reply(this.flash.slice(addr - BASE, addr - BASE + length));
    }
  }

  // installFakeUSB replaces navigator.usb in a page with a fake that offers
  // the plugged simulated device in requestDevice(). Page-side API:
  //   __usbSim.plug(opts) plugs a new SimDfuSe(opts); __usbSim.unplug();
  //   __usbSim.dev is the current device; __usbSim.log the calls.
  function installFakeUSB(win) {
    const usb = new win.EventTarget();
    const sim = { dev: null, granted: false, log: [] };
    const fire = (type, dev) => { const e = new win.Event(type); Object.defineProperty(e, 'device', { value: dev }); usb.dispatchEvent(e); };
    sim.plug = (opts) => {
      const d = new SimDfuSe(opts || {});
      d.onGone = (x) => { if (sim.dev === x) sim.dev = null; fire('disconnect', x); };
      sim.dev = d; sim.granted = false;
      fire('connect', d);
      return d;
    };
    sim.unplug = () => { const d = sim.dev; if (!d) return; sim.dev = null; d.gone = true; d.opened = false; fire('disconnect', d); };
    usb.requestDevice = async (opts) => {
      sim.log.push('requestDevice active=' + (win.navigator.userActivation ? win.navigator.userActivation.isActive : '?') + ' filters=' + JSON.stringify(opts && opts.filters));
      const ok = sim.dev && (opts.filters || []).some((f) => f.vendorId === sim.dev.vendorId && f.productId === sim.dev.productId);
      if (!ok) throw new win.DOMException('No device selected.', 'NotFoundError');
      sim.granted = true;
      return sim.dev;
    };
    usb.getDevices = async () => (sim.dev && sim.granted ? [sim.dev] : []);
    Object.defineProperty(win.Navigator.prototype, 'usb', { get() { return usb; }, configurable: true });
    win.__usbSim = sim;
    return sim;
  }

  const api = { SimDfuSe, fakeFirmware, installFakeUSB, VARIANTS, ST, ERR };
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.DfuSeSim = api;
})(typeof window !== 'undefined' ? window : globalThis);
