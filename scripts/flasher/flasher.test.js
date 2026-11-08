'use strict';
// Unit tests of the firmware flasher's logic (web/static/flasher.js)
// against the simulated DfuSe bootloader (dfuse-sim.js). No hardware.
//   node --test scripts/flasher/      (or scripts/test-js.sh)
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const F = require('../../web/static/flasher.js');
const { SimDfuSe, fakeFirmware, VARIANTS, ST } = require('./dfuse-sim.js');

const FW_DIR = path.join(__dirname, '..', '..', 'web', 'static', 'firmware');
const MANIFEST = JSON.parse(fs.readFileSync(path.join(FW_DIR, 'manifest.json'), 'utf8'));
const EASYVNA = new Uint8Array(fs.readFileSync(path.join(FW_DIR, MANIFEST.file)));
const EASYVNA_PAGES = Math.ceil(EASYVNA.length / 2048);
const H4 = F.parseLayout(VARIANTS.f303.layout);
const H = F.parseLayout(VARIANTS.f072.layout);

// toHex writes an Intel HEX file (16-byte records, extended linear address).
function toHex(data, base) {
  const lines = [];
  const rec = (type, addr, bytes) => {
    const b = [bytes.length, (addr >> 8) & 0xff, addr & 0xff, type, ...bytes];
    const sum = (0x100 - (b.reduce((s, v) => s + v, 0) & 0xff)) & 0xff;
    lines.push(':' + [...b, sum].map((v) => v.toString(16).toUpperCase().padStart(2, '0')).join(''));
  };
  let upper = -1;
  for (let o = 0; o < data.length; o += 16) {
    const a = base + o;
    if (a >>> 16 !== upper) { upper = a >>> 16; rec(4, 0, [(upper >> 8) & 0xff, upper & 0xff]); }
    rec(0, a & 0xffff, Array.from(data.subarray(o, o + 16)));
  }
  rec(5, 0, [0x08, 0x00, 0x01, 0xa1]);
  rec(1, 0, []);
  return lines.join('\r\n') + '\r\n';
}
// toDfu writes a DfuSe file with one target (alt 0) and the given elements.
function toDfu(elements, alt = 0) {
  const u32 = (v) => [v & 0xff, (v >>> 8) & 0xff, (v >>> 16) & 0xff, (v >>> 24) & 0xff];
  const els = [];
  for (const e of elements) els.push(...u32(e.addr), ...u32(e.data.length), ...e.data);
  const tname = new Array(255).fill(0); Array.from('ST...').forEach((c, i) => { tname[i] = c.charCodeAt(0); });
  const target = [...Array.from('Target', (c) => c.charCodeAt(0)), alt, ...u32(1), ...tname, ...u32(els.length), ...u32(elements.length), ...els];
  const body = [...Array.from('DfuSe', (c) => c.charCodeAt(0)), 1, ...u32(11 + target.length), 1, ...target];
  const sfx = [0xff, 0xff, 0x11, 0xdf, 0x83, 0x04, 0x1a, 0x01, 0x55, 0x46, 0x44, 16];
  const all = new Uint8Array([...body, ...sfx]);
  const crc = (~F.crc32(all)) >>> 0;
  return new Uint8Array([...all, ...u32(crc)]);
}
async function connected(opts) {
  const sim = new SimDfuSe(opts);
  const dfu = new F.DfuSe(sim);
  await dfu.connect();
  return { sim, dfu };
}
const sha = (b) => crypto.createHash('sha256').update(b).digest('hex');

// ---------- layout ----------
test('parseLayout: NanoVNA-H4 (F303, 256 KB) and NanoVNA-H (F072, 128 KB)', () => {
  assert.equal(H4.name, 'Internal Flash');
  assert.equal(H4.start, 0x08000000);
  assert.equal(H4.size, 256 * 1024);
  assert.deepEqual(H4.segments, [{ start: 0x08000000, count: 128, size: 2048, readable: true, erasable: true, writable: true }]);
  assert.equal(H.size, 128 * 1024);
  assert.equal(F.targetFor(H4).id, 'f303-256k');
  assert.equal(F.targetFor(H).id, 'f072-128k');
  assert.equal(F.targetFor(F.parseLayout('@Internal Flash /0x08000000/32*0001Kg')), null);
});
test('parseLayout: several sectors, regions, multipliers and properties', () => {
  const l = F.parseLayout('@Internal Flash  /0x08000000/04*016Kg,01*064Kg,07*128Kg/0x1FFF0000/1*1Ma');
  assert.equal(l.size, 4 * 16384 + 65536 + 7 * 131072);
  assert.equal(l.segments[1].start, 0x08010000);
  assert.equal(l.regions[1].start, 0x1fff0000);
  assert.equal(l.regions[1].size, 1048576);
  assert.deepEqual([l.regions[1].segments[0].readable, l.regions[1].segments[0].erasable, l.regions[1].segments[0].writable], [true, false, false]);
  const ob = F.parseLayout('@Option Bytes  /0x1FFFF800/01*016 e');
  assert.equal(ob.size, 16);
  assert.deepEqual([ob.segments[0].readable, ob.segments[0].erasable, ob.segments[0].writable], [true, false, true]);
  assert.equal(F.parseLayout('@Flash/0x08000000/2*512Bd/').size, 1024);
  for (const bad of ['', 'Internal Flash', '@Internal Flash', '@X/0x0800/12*x', '@X/08000000/1*1Kg', '@X/0x08000000/1*1Kz']) {
    assert.throws(() => F.parseLayout(bad), /DfuSe/, bad);
  }
});
test('pageAt / pagesFor', () => {
  assert.deepEqual(F.pageAt(H4, 0x08000801), { start: 0x08000800, size: 2048, erasable: true, writable: true });
  assert.equal(F.pageAt(H4, 0x08040000), null);
  assert.equal(F.pagesFor(H4, 0x08000000, 0x08000000 + 122660).length, 60);
  assert.throws(() => F.pagesFor(H, 0x08000000, 0x08020001), /outside the flash/);
});

// ---------- image formats ----------
test('Intel HEX: round trip of the bundled firmware, gaps, records', () => {
  const img = F.loadImage('EasyVNA.hex', new TextEncoder().encode(toHex(EASYVNA, 0x08000000)));
  assert.equal(img.format, 'hex');
  assert.equal(img.base, 0x08000000);
  assert.equal(sha(img.data), MANIFEST.sha256);
  // extended segment address (type 02) and a gap filled with 0xFF
  const h = ':020000021000EC\n:0400000001020304F2\n:04001000AABBCCDDDE\n:00000001FF\n';
  const g = F.parseHex(h);
  assert.equal(g.base, 0x10000);
  assert.deepEqual(Array.from(g.data), [1, 2, 3, 4, ...new Array(12).fill(0xff), 0xaa, 0xbb, 0xcc, 0xdd]);
  assert.throws(() => F.parseHex(':0400000001020304F3\n:00000001FF\n'), /checksum/);
  assert.throws(() => F.parseHex(':0400000001020304F2\n'), /end-of-file/);
  assert.throws(() => F.parseHex('hello\n'), /not a record/);
  assert.throws(() => F.parseHex(':0300000001020304F2\n'), /length/);
});
test('DfuSe .dfu file', () => {
  const d = toDfu([{ addr: 0x08000000, data: EASYVNA.subarray(0, 4096) }, { addr: 0x08001000, data: EASYVNA.subarray(4096) }]);
  const img = F.loadImage('fw.dfu', d);
  assert.equal(img.format, 'dfu');
  assert.equal(img.usbId, '0483:df11');
  assert.equal(sha(img.data), MANIFEST.sha256);
  const bad = d.slice(); bad[300] ^= 1;
  assert.throws(() => F.loadImage('fw.dfu', bad), /CRC/);
  assert.throws(() => F.loadImage('x.dfu', toDfu([{ addr: 0x08000000, data: [1, 2] }], 1)), /alternate setting 0/);
});
test('loadImage: .bin is raw at 0x08000000', () => {
  const img = F.loadImage('EasyVNA-H4-v0.1.0.bin', EASYVNA);
  assert.equal(img.format, 'bin');
  assert.equal(img.base, 0x08000000);
  assert.equal(img.data.length, MANIFEST.size);
});

// ---------- validation and planning ----------
const okText = (v) => v.checks.filter((c) => !c.ok).map((c) => c.text).join(' | ');
test('validate: the bundled EasyVNA image on an H4 keeps the calibration', () => {
  const v = F.validateImage(H4, F.loadImage(MANIFEST.file, EASYVNA), { mode: 'firmware', flashSize: MANIFEST.flashSize });
  assert.ok(v.ok, okText(v));
  assert.equal(v.plan.pages.length, EASYVNA_PAGES);
  assert.equal(v.plan.pages[59].start, 0x0801d800);
  assert.equal(v.plan.data.length % 4, 0);
  assert.ok(v.checks.some((c) => /kept/.test(c.text)));
});
test('validate: the H4 image is refused on a 128 KB NanoVNA-H', () => {
  const v = F.validateImage(H, F.loadImage(MANIFEST.file, EASYVNA), { mode: 'firmware', flashSize: MANIFEST.flashSize });
  assert.ok(!v.ok);
  assert.match(okText(v), /for the NanoVNA-H4 .*this device has 128 KB/);
  assert.equal(v.plan, null);
});
test('validate: an image overlapping the save area is refused', () => {
  const big = fakeFirmware(0x23800 + 2);
  const v = F.validateImage(H4, F.loadImage('big.bin', big));
  assert.ok(!v.ok);
  assert.match(okText(v), /inside the calibration and configuration area \(0x08023800/);
  assert.ok(F.validateImage(H4, F.loadImage('fits.bin', fakeFirmware(0x23800))).ok);
  const h = F.validateImage(H, F.loadImage('h.bin', fakeFirmware(0x18000 + 4)));
  assert.match(okText(h), /0x08018000/);
});
test('validate: vector table, base address, size, unknown layout', () => {
  const sp = fakeFirmware(4096); sp[3] = 0x30;
  assert.match(okText(F.validateImage(H4, F.loadImage('a.bin', sp))), /stack pointer/);
  const rv = fakeFirmware(4096); rv[4] = 0xa0; // even reset vector
  assert.match(okText(F.validateImage(H4, F.loadImage('a.bin', rv))), /reset vector/);
  const far = fakeFirmware(4096); far[6] = 0x10; // reset vector beyond the image
  assert.match(okText(F.validateImage(H4, F.loadImage('a.bin', far))), /reset vector/);
  const off = { ...F.loadImage('a.bin', fakeFirmware(4096)), base: 0x08004000 };
  assert.match(okText(F.validateImage(H4, off)), /not at the start/);
  assert.match(okText(F.validateImage(H4, F.loadImage('e.bin', new Uint8Array(0)))), /empty/);
  assert.match(okText(F.validateImage(H, F.loadImage('a.bin', fakeFirmware(200 * 1024)))), /too big/);
  assert.match(okText(F.validateImage(F.parseLayout('@Internal Flash /0x08000000/32*1Kg'), F.loadImage('a.bin', fakeFirmware(4096)))), /Unknown flash layout/);
  // F072 RAM is 16 KB
  const ram = fakeFirmware(4096); ram[0] = 0x00; ram[1] = 0x50; // SP 0x20005000
  assert.ok(F.validateImage(H4, F.loadImage('a.bin', ram)).ok);
  assert.match(okText(F.validateImage(H, F.loadImage('a.bin', ram))), /stack pointer/);
});
test('validate: restore needs the whole flash', () => {
  const sim = new SimDfuSe();
  assert.ok(F.validateImage(H4, F.loadImage('b.bin', sim.flash), { mode: 'restore' }).ok);
  assert.equal(F.validateImage(H4, F.loadImage('b.bin', sim.flash), { mode: 'restore' }).plan.pages.length, 128);
  assert.match(okText(F.validateImage(H4, F.loadImage('b.bin', sim.flash.subarray(0, 4096)), { mode: 'restore' })), /exactly 262,144 bytes/);
  assert.match(okText(F.validateImage(H4, F.loadImage('b.bin', sim.flash))), /calibration and configuration area/); // not as a normal image
});
test('planWrite pads to half-words with 0xFF', () => {
  const img = F.loadImage('odd.bin', fakeFirmware(4097));
  const p = F.planWrite(H4, img);
  assert.equal(p.data.length, 4100);
  assert.equal(p.length, 4097);
  assert.deepEqual(Array.from(p.data.subarray(4097)), [0xff, 0xff, 0xff]);
  assert.equal(p.pages.length, 3);
});

// ---------- DfuSe protocol against the simulator ----------
test('connect: layout, transfer size from the functional descriptor, dfuIDLE', async () => {
  const { sim, dfu } = await connected({ xfer: 1024 });
  assert.equal(dfu.layout.size, 256 * 1024);
  assert.equal(dfu.target.name, 'NanoVNA-H4');
  assert.equal(dfu.xfer, 1024);
  assert.equal(sim.state, ST.dfuIDLE);
  assert.deepEqual(sim.log.slice(0, 4), ['open', 'config 1', 'claim 0', 'alt 0']);
  const b = await connected({ noFunctional: true, noInterfaceName: true, variant: 'f072' });
  assert.equal(b.dfu.xfer, 2048);
  assert.equal(b.dfu.layout.size, 128 * 1024);
});
test('dump reads the whole flash', async () => {
  const { sim, dfu } = await connected();
  let last = 0;
  const data = await F.dumpFlash(dfu, { progress: (d, t) => { assert.ok(d > last && t === 262144); last = d; } });
  assert.equal(last, 262144);
  assert.equal(sha(data), sha(sim.flash));
  assert.equal(sim.state, ST.dfuIDLE);
  assert.equal(sim.pollViolations, 0);
});
test('flash the bundled firmware: only its pages erased, calibration untouched, verified', async () => {
  const { sim, dfu } = await connected();
  const before = sim.flash.slice();
  const v = F.validateImage(dfu.layout, F.loadImage(MANIFEST.file, EASYVNA), { flashSize: MANIFEST.flashSize });
  const phases = [];
  await F.flashPlan(dfu, v.plan, { phase: (p) => phases.push(p) });
  assert.deepEqual(phases, ['erase', 'write', 'verify', 'done']);
  assert.equal(sha(sim.flash.subarray(0, EASYVNA.length)), MANIFEST.sha256);
  assert.ok(sim.flash.subarray(EASYVNA.length, EASYVNA_PAGES * 2048).every((x) => x === 0xff));
  assert.deepEqual(sim.flash.subarray(EASYVNA_PAGES * 2048), before.subarray(EASYVNA_PAGES * 2048)); // old bytes up to 0x23800 and the save area
  assert.deepEqual(sim.erased, Array.from({ length: EASYVNA_PAGES }, (_, i) => 0x08000000 + i * 2048));
  assert.equal(sim.massErase, 0);
  assert.equal(sim.unprotect, 0);
  assert.equal(sim.pollViolations, 0);
  assert.equal(sim.state, ST.dfuIDLE);
});
test('restore a full backup writes everything back', async () => {
  const { sim, dfu } = await connected();
  const backup = await F.dumpFlash(dfu);
  await F.flashPlan(dfu, F.validateImage(dfu.layout, F.loadImage('e.bin', EASYVNA)).plan);
  sim.flash.fill(0x00, 0x23800, 0x24000); // a changed calibration
  const v = F.validateImage(dfu.layout, F.loadImage('backup.bin', backup), { mode: 'restore' });
  assert.ok(v.ok, okText(v));
  await F.flashPlan(dfu, v.plan);
  assert.equal(sha(sim.flash), sha(backup));
});
test('verify failure names the address', async () => {
  const { dfu } = await connected({ corruptAt: 0x08004321 });
  const v = F.validateImage(dfu.layout, F.loadImage('e.bin', EASYVNA));
  await assert.rejects(F.flashPlan(dfu, v.plan), (e) => e.code === 'verify' && e.count === 1 && /0x08004321/.test(e.message));
});
test('writing over non-erased flash fails like real flash (errPROG), then recovers', async () => {
  const { sim, dfu } = await connected();
  await assert.rejects(dfu.write(0x08000000, new Uint8Array([1, 2, 3, 4])), (e) => e.code === 'status' && /errPROG/.test(e.message));
  assert.equal(sim.state, ST.dfuIDLE); // CLRSTATUS done
  await dfu.toIdle();
  assert.equal(sim.state, ST.dfuIDLE);
});
test('read-protected device: clear message, nothing written', async () => {
  const { sim, dfu } = await connected({ protected: true });
  await assert.rejects(F.dumpFlash(dfu), (e) => e.code === 'protected' && /read-protected/.test(e.message) && /does not offer/.test(e.message));
  assert.equal(sim.unprotect, 0);
  assert.equal(sim.state, ST.dfuIDLE);
});
test('state machine: UPLOAD right after a set-address stalls (ABORT is needed)', async () => {
  const { sim, dfu } = await connected();
  await dfu.setAddress(0x08000000);
  assert.equal(sim.state, ST.dfuDNLOAD_IDLE);
  await assert.rejects(dfu.ctrlIn(F.REQ.UPLOAD, 2, 16), /stalled/);
  assert.equal(sim.state, ST.dfuERROR);
  await dfu.toIdle();
  assert.equal(sim.state, ST.dfuIDLE);
});
test('stop between blocks', async () => {
  const { sim, dfu } = await connected();
  let n = 0;
  await assert.rejects(dfu.erase(F.pagesFor(dfu.layout, 0x08000000, 0x08008000), { stopped: () => ++n > 3 }), (e) => e.code === 'stopped');
  assert.equal(sim.erased.length, 3);
});
test('leave: set address, zero-length DNLOAD, device resets', async () => {
  const { sim, dfu } = await connected();
  await dfu.leave();
  assert.ok(sim.left);
  assert.ok(sim.log.includes('setaddr 0x8000000'));
  assert.ok(sim.log.some((l) => /^leave /.test(l)));
  await new Promise((r) => setTimeout(r, 20));
  assert.ok(sim.gone);
});
test('poll timeout honoured even when slow', async () => {
  const { sim, dfu } = await connected({ pollErase: 25 });
  const t0 = Date.now();
  await dfu.erase(F.pagesFor(dfu.layout, 0x08000000, 0x08001000));
  assert.ok(Date.now() - t0 >= 50);
  assert.equal(sim.pollViolations, 0);
});
test('the bundled firmware matches its manifest', () => {
  assert.equal(sha(EASYVNA), MANIFEST.sha256);
  assert.equal(EASYVNA.length, MANIFEST.size);
  assert.equal(MANIFEST.flashSize, 262144);
  assert.match(MANIFEST.license, /GPL-3\.0/);
  assert.match(MANIFEST.source, /^https:\/\/github\.com\/pix\/easyvna\/releases\/tag\//);
});
