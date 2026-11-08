// Package nanovnav2 is an EXPERIMENTAL driver for the NanoVNA V2 family
// (NanoVNA V2 / V2 Plus / V2 Plus4 and compatible boards such as the
// LiteVNA) that speak the binary register protocol over USB serial
// (USB 04b4:0008). It has been written from the published protocol
// description and tested against the simulator in internal/vna/mock only:
// no real hardware has been tested.
//
// Protocol summary (all multi-byte values little-endian):
//
//	0x00              NOP
//	0x0d              INDICATE: the device answers one byte, '2'
//	0x10 a            READ  1 byte at register a
//	0x11 a            READ2 2 bytes
//	0x12 a            READ4 4 bytes
//	0x18 a n          READFIFO: n (1..255) records of 32 bytes from FIFO a
//	0x20 a v          WRITE  1 byte
//	0x21 a v v        WRITE2 2 bytes
//	0x22 a v×4        WRITE4 4 bytes
//	0x23 a v×8        WRITE8 8 bytes
//	0x28 a n v×n      WRITEFIFO
//
// Eight zero bytes (NOPs) bring the command parser back to a known state.
//
// Registers: 0x00 sweep start (Hz, u64), 0x10 sweep step (Hz, u64), 0x20
// sweep points (u16), 0x22 values per frequency (u16), 0x30 values FIFO
// (any write clears it), 0xf0 device variant (2 = V2), 0xf1 protocol
// version, 0xf2 hardware revision, 0xf3 / 0xf4 firmware major / minor
// (major 0xff = bootloader / DFU mode).
//
// A FIFO record is fwd0 re, fwd0 im, rev0 re, rev0 im, rev1 re, rev1 im
// (six int32), the frequency index (uint16) and 6 reserved bytes. The data
// are raw detector values: S11 = rev0 / fwd0, S21 = rev1 / fwd0, with no
// calibration applied, so EasyVNA Web calibrates on the host.
package nanovnav2

import (
	"encoding/binary"
	"math"
)

// Opcodes.
const (
	OpNop       = 0x00
	OpIndicate  = 0x0d
	OpRead      = 0x10
	OpRead2     = 0x11
	OpRead4     = 0x12
	OpReadFIFO  = 0x18
	OpWrite     = 0x20
	OpWrite2    = 0x21
	OpWrite4    = 0x22
	OpWrite8    = 0x23
	OpWriteFIFO = 0x28
)

// Registers.
const (
	RegSweepStart     = 0x00
	RegSweepStep      = 0x10
	RegSweepPoints    = 0x20
	RegValuesPerFreq  = 0x22
	RegValuesFIFO     = 0x30
	RegDeviceVariant  = 0xf0
	RegProtocolVer    = 0xf1
	RegHardwareRev    = 0xf2
	RegFirmwareMajor  = 0xf3
	RegFirmwareMinor  = 0xf4
	IndicateReply     = '2'
	RecordSize        = 32
	MaxFIFORead       = 255
	VariantV2         = 2
	firmwareMajorDFU  = 0xff
	resetSequenceSize = 8
)

// Record is one FIFO entry.
type Record struct {
	Fwd0, Rev0, Rev1 complex128
	FreqIndex        int
}

// S11 is the raw reflection rev0 / fwd0.
func (r Record) S11() complex128 { return ratio(r.Rev0, r.Fwd0) }

// S21 is the raw transmission rev1 / fwd0.
func (r Record) S21() complex128 { return ratio(r.Rev1, r.Fwd0) }

func ratio(a, b complex128) complex128 {
	if b == 0 {
		return complex(math.NaN(), math.NaN())
	}
	return a / b
}

// ParseRecord decodes a 32-byte FIFO record.
func ParseRecord(b []byte) Record {
	i32 := func(k int) float64 { return float64(int32(binary.LittleEndian.Uint32(b[4*k:]))) }
	return Record{
		Fwd0:      complex(i32(0), i32(1)),
		Rev0:      complex(i32(2), i32(3)),
		Rev1:      complex(i32(4), i32(5)),
		FreqIndex: int(binary.LittleEndian.Uint16(b[24:])),
	}
}

// EncodeRecord is the inverse of ParseRecord (values are rounded to int32).
func EncodeRecord(r Record) []byte {
	b := make([]byte, RecordSize)
	put := func(k int, v float64) {
		v = math.Max(math.MinInt32, math.Min(math.MaxInt32, math.Round(v)))
		binary.LittleEndian.PutUint32(b[4*k:], uint32(int32(v)))
	}
	put(0, real(r.Fwd0))
	put(1, imag(r.Fwd0))
	put(2, real(r.Rev0))
	put(3, imag(r.Rev0))
	put(4, real(r.Rev1))
	put(5, imag(r.Rev1))
	binary.LittleEndian.PutUint16(b[24:], uint16(r.FreqIndex))
	return b
}

// Command encoders.

func cmdRead(reg byte) []byte     { return []byte{OpRead, reg} }
func cmdWrite(reg, v byte) []byte { return []byte{OpWrite, reg, v} }
func cmdWrite2(reg byte, v uint16) []byte {
	return binary.LittleEndian.AppendUint16([]byte{OpWrite2, reg}, v)
}
func cmdWrite8(reg byte, v uint64) []byte {
	return binary.LittleEndian.AppendUint64([]byte{OpWrite8, reg}, v)
}
func cmdReadFIFO(reg byte, n int) []byte { return []byte{OpReadFIFO, reg, byte(n)} }
