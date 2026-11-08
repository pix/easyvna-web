// Package qr is a small, dependency-free QR code encoder (ISO/IEC 18004,
// model 2, byte mode, versions 1-40, all four error correction levels).
//
// It exists so that printed antenna sheets can carry a QR code of a product
// link (an http/https URL) that can be scanned from paper. The symbol is
// computed locally and rendered as an inline SVG path; no network access or
// third-party library is involved.
//
// Typical use:
//
//	c, err := qr.Encode([]byte(url), qr.M)
//	// <svg viewBox="-4 -4 {c.Size+8} {c.Size+8}"><path d="{c.SVGPath()}"/></svg>
//
// SVGPath does not include the quiet zone; callers should leave a light
// margin of at least 4 modules around the symbol.
package qr

import (
	"errors"
	"strconv"
)

// Level is the error correction level.
type Level int

// Error correction levels, recovering about 7%, 15%, 25% and 30% of the
// codewords respectively.
const (
	L Level = iota
	M
	Q
	H
)

// Code is an encoded QR symbol.
type Code struct {
	Size    int   // modules per side (no quiet zone), 17+4*Version
	Version int   // 1..40
	Mask    int   // 0..7, data mask pattern applied
	Level   Level // error correction level

	mod []bool // Size*Size modules, row-major, true = dark
}

// Dark reports whether the module at column x, row y (0-based) is dark.
// Coordinates outside the symbol are light.
func (c *Code) Dark(x, y int) bool {
	if x < 0 || y < 0 || x >= c.Size || y >= c.Size {
		return false
	}
	return c.mod[y*c.Size+x]
}

// SVGPath returns an SVG path "d" attribute covering all dark modules, one
// unit per module, origin at the top-left module. Horizontal runs of dark
// modules are merged into one rectangle subpath each ("M3 0h4v1h-4z").
func (c *Code) SVGPath() string {
	var b []byte
	for y := 0; y < c.Size; y++ {
		for x := 0; x < c.Size; {
			if !c.mod[y*c.Size+x] {
				x++
				continue
			}
			n := 1
			for x+n < c.Size && c.mod[y*c.Size+x+n] {
				n++
			}
			b = append(b, 'M')
			b = strconv.AppendInt(b, int64(x), 10)
			b = append(b, ' ')
			b = strconv.AppendInt(b, int64(y), 10)
			b = append(b, 'h')
			b = strconv.AppendInt(b, int64(n), 10)
			b = append(b, "v1h-"...)
			b = strconv.AppendInt(b, int64(n), 10)
			b = append(b, 'z')
			x += n
		}
	}
	return string(b)
}

// ErrTooLong is returned when the data does not fit in a version 40 symbol
// at the requested level.
var ErrTooLong = errors.New("qr: data too long")

// Encode encodes data in byte mode at level lvl, using the smallest version
// (1..40) that fits and the data mask with the lowest penalty score.
func Encode(data []byte, lvl Level) (*Code, error) {
	return encode(data, lvl, 0, -1)
}

// Error correction tables, indexed [level][version] (index 0 unused):
// EC codewords per block and number of blocks (ISO/IEC 18004 table 9).
var eccPerBlock = [4][41]int8{
	{-1, 7, 10, 15, 20, 26, 18, 20, 24, 30, 18, 20, 24, 26, 30, 22, 24, 28, 30, 28, 28, 28, 28, 30, 30, 26, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{-1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28},
	{-1, 13, 22, 18, 26, 18, 24, 18, 22, 20, 24, 28, 26, 24, 20, 30, 24, 28, 28, 26, 30, 28, 30, 30, 30, 30, 28, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
	{-1, 17, 28, 22, 16, 22, 28, 26, 26, 24, 28, 24, 28, 22, 24, 24, 30, 28, 28, 26, 28, 30, 24, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30, 30},
}

var numBlocks = [4][41]int8{
	{-1, 1, 1, 1, 1, 1, 2, 2, 2, 2, 4, 4, 4, 4, 4, 6, 6, 6, 6, 7, 8, 8, 9, 9, 10, 12, 12, 12, 13, 14, 15, 16, 17, 18, 19, 19, 20, 21, 22, 24, 25},
	{-1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25, 26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49},
	{-1, 1, 1, 2, 2, 4, 4, 6, 6, 8, 8, 8, 10, 12, 16, 12, 17, 16, 18, 21, 20, 23, 23, 25, 27, 29, 34, 34, 35, 38, 40, 43, 45, 48, 51, 53, 56, 59, 62, 65, 68},
	{-1, 1, 1, 2, 4, 4, 4, 5, 6, 8, 8, 11, 11, 16, 16, 18, 16, 19, 21, 25, 25, 25, 34, 30, 32, 35, 37, 40, 42, 45, 48, 51, 54, 57, 60, 63, 66, 70, 74, 77, 81},
}

// rawModules is the number of modules available for codewords and
// remainder bits in a symbol of version v (everything but function
// patterns, format and version information).
func rawModules(v int) int {
	n := (16*v+128)*v + 64
	if v >= 2 {
		a := v/7 + 2
		n -= (25*a-10)*a - 55
		if v >= 7 {
			n -= 36
		}
	}
	return n
}

// dataCodewords is the number of data (non-EC) codewords for v and lvl.
func dataCodewords(v int, lvl Level) int {
	return rawModules(v)/8 - int(eccPerBlock[lvl][v])*int(numBlocks[lvl][v])
}

// countBits is the width of the byte-mode character count indicator.
func countBits(v int) int {
	if v <= 9 {
		return 8
	}
	return 16
}

// capacity is the maximum number of bytes encodable in byte mode.
func capacity(v int, lvl Level) int {
	return (dataCodewords(v, lvl)*8 - 4 - countBits(v)) / 8
}

// encode builds a symbol. version 0 selects the smallest that fits, mask -1
// selects the lowest-penalty mask; otherwise they are forced (tests).
func encode(data []byte, lvl Level, version, mask int) (*Code, error) {
	if lvl < L || lvl > H {
		return nil, errors.New("qr: invalid level")
	}
	if mask < -1 || mask > 7 || version < 0 || version > 40 {
		return nil, errors.New("qr: invalid version or mask")
	}
	if version == 0 {
		for v := 1; v <= 40; v++ {
			if len(data) <= capacity(v, lvl) {
				version = v
				break
			}
		}
		if version == 0 {
			return nil, ErrTooLong
		}
	} else if len(data) > capacity(version, lvl) {
		return nil, ErrTooLong
	}
	v := version

	// Bit stream: mode 0100, count, bytes, terminator, byte padding.
	nd := dataCodewords(v, lvl)
	bb := bitBuf{buf: make([]byte, 0, nd)}
	bb.put(4, 4)
	bb.put(len(data), countBits(v))
	for _, d := range data {
		bb.put(int(d), 8)
	}
	bb.put(0, min(4, nd*8-bb.n))
	bb.put(0, (8-bb.n%8)%8)
	for p := byte(0xEC); len(bb.buf) < nd; p ^= 0xEC ^ 0x11 {
		bb.buf = append(bb.buf, p)
	}

	cw := interleave(bb.buf, v, lvl)

	c := &Code{Size: 17 + 4*v, Version: v, Level: lvl}
	fn := c.drawFunction()
	c.drawCodewords(cw, fn)

	if mask < 0 {
		best := 0
		for m := 0; m < 8; m++ {
			c.applyMask(m, fn)
			c.drawFormat(m)
			if p := c.penalty(); m == 0 || p < best {
				best, mask = p, m
			}
			c.applyMask(m, fn) // XOR again to undo
		}
	}
	c.applyMask(mask, fn)
	c.drawFormat(mask)
	c.Mask = mask
	return c, nil
}

// bitBuf accumulates bits MSB first.
type bitBuf struct {
	buf []byte
	n   int // bits written
}

func (b *bitBuf) put(v, n int) {
	for i := n - 1; i >= 0; i-- {
		if b.n%8 == 0 {
			b.buf = append(b.buf, 0)
		}
		if v>>i&1 != 0 {
			b.buf[b.n/8] |= 0x80 >> (b.n % 8)
		}
		b.n++
	}
}

// blockLayout returns the block count, EC codewords per block, the number
// of short blocks and the data length of a short block (long blocks carry
// one more data codeword).
func blockLayout(v int, lvl Level) (nb, ecc, nShort, shortData int) {
	nb, ecc = int(numBlocks[lvl][v]), int(eccPerBlock[lvl][v])
	raw := rawModules(v) / 8
	nShort = nb - raw%nb
	shortData = raw/nb - ecc
	return
}

// interleave splits data into blocks, appends Reed-Solomon EC codewords to
// each and interleaves them into the final codeword sequence.
func interleave(data []byte, v int, lvl Level) []byte {
	nb, ecc, nShort, shortData := blockLayout(v, lvl)
	gen := rsGenerator(ecc)
	blocks := make([][]byte, nb)
	eccs := make([][]byte, nb)
	for i, k := 0, 0; i < nb; i++ {
		n := shortData
		if i >= nShort {
			n++
		}
		blocks[i] = data[k : k+n]
		eccs[i] = rsRemainder(blocks[i], gen)
		k += n
	}
	out := make([]byte, 0, rawModules(v)/8)
	for i := 0; i <= shortData; i++ {
		for j := range blocks {
			if i < len(blocks[j]) {
				out = append(out, blocks[j][i])
			}
		}
	}
	for i := 0; i < ecc; i++ {
		for j := range eccs {
			out = append(out, eccs[j][i])
		}
	}
	return out
}

// GF(256) arithmetic with the QR polynomial x^8+x^4+x^3+x^2+1 (0x11d).
var gfExp, gfLog = func() (e [512]byte, l [256]byte) {
	x := 1
	for i := 0; i < 255; i++ {
		e[i], e[i+255] = byte(x), byte(x)
		l[x] = byte(i)
		x <<= 1
		if x&0x100 != 0 {
			x ^= 0x11d
		}
	}
	return
}()

func gfMul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return gfExp[int(gfLog[a])+int(gfLog[b])]
}

// rsGenerator returns the coefficients (highest degree first, leading 1
// omitted) of prod_{i<n} (x - a^i).
func rsGenerator(n int) []byte {
	g := make([]byte, n)
	g[n-1] = 1 // start with the polynomial 1 stored at the low end
	root := byte(1)
	for i := 0; i < n; i++ {
		// multiply by (x - root): shift left, add root*g
		for j := 0; j < n; j++ {
			g[j] = gfMul(g[j], root)
			if j+1 < n {
				g[j] ^= g[j+1]
			}
		}
		root = gfMul(root, 2)
	}
	return g
}

// rsRemainder returns data(x)*x^n mod gen(x), the n EC codewords.
func rsRemainder(data, gen []byte) []byte {
	r := make([]byte, len(gen))
	for _, d := range data {
		f := d ^ r[0]
		copy(r, r[1:])
		r[len(r)-1] = 0
		for i, g := range gen {
			r[i] ^= gfMul(g, f)
		}
	}
	return r
}

// alignPos returns the alignment pattern centre coordinates for version v.
func alignPos(v int) []int {
	if v == 1 {
		return nil
	}
	n := v/7 + 2
	step := (v*8 + n*3 + 5) / (n*4 - 4) * 2
	p := make([]int, n)
	p[0] = 6
	for i, pos := n-1, 17+4*v-7; i >= 1; i, pos = i-1, pos-step {
		p[i] = pos
	}
	return p
}

// drawFunction draws finder, timing, alignment patterns and version
// information, reserves the format areas and returns the function-module
// map (true = not a data module).
func (c *Code) drawFunction() []bool {
	s := c.Size
	c.mod = make([]bool, s*s)
	fn := make([]bool, s*s)
	set := func(x, y int, d bool) { c.mod[y*s+x], fn[y*s+x] = d, true }
	for i := 0; i < s; i++ {
		set(6, i, i%2 == 0)
		set(i, 6, i%2 == 0)
	}
	for _, f := range [3][2]int{{3, 3}, {s - 4, 3}, {3, s - 4}} {
		for dy := -4; dy <= 4; dy++ {
			for dx := -4; dx <= 4; dx++ {
				x, y := f[0]+dx, f[1]+dy
				if x >= 0 && y >= 0 && x < s && y < s {
					d := max(abs(dx), abs(dy))
					set(x, y, d != 2 && d != 4)
				}
			}
		}
	}
	ap := alignPos(c.Version)
	for i, ax := range ap {
		for j, ay := range ap {
			if (i == 0 && j == 0) || (i == 0 && j == len(ap)-1) || (i == len(ap)-1 && j == 0) {
				continue // overlaps a finder
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					set(ax+dx, ay+dy, max(abs(dx), abs(dy)) != 1)
				}
			}
		}
	}
	c.drawFormat(0) // reserve; overwritten after masking
	for i := 0; i < s*s; i++ {
		if c.formatArea(i) {
			fn[i] = true
		}
	}
	if c.Version >= 7 {
		bits := versionBits(c.Version)
		for i := 0; i < 18; i++ {
			a, b := s-11+i%3, i/3
			d := bits>>i&1 != 0
			set(a, b, d)
			set(b, a, d)
		}
	}
	return fn
}

// formatArea reports whether module index i holds format information or
// the always-dark module.
func (c *Code) formatArea(i int) bool {
	s := c.Size
	x, y := i%s, i/s
	return (x == 8 && (y <= 8 || y >= s-8)) || (y == 8 && (x <= 8 || x >= s-8))
}

// formatBits returns the 15-bit masked format information word.
func formatBits(lvl Level, mask int) int {
	d := [4]int{1, 0, 3, 2}[lvl]<<3 | mask
	r := d
	for i := 0; i < 10; i++ {
		r = r<<1 ^ (r>>9)*0x537
	}
	return (d<<10 | r) ^ 0x5412
}

// versionBits returns the 18-bit version information word (v >= 7).
func versionBits(v int) int {
	r := v
	for i := 0; i < 12; i++ {
		r = r<<1 ^ (r>>11)*0x1f25
	}
	return v<<12 | r
}

// drawFormat writes both copies of the format information plus the dark
// module.
func (c *Code) drawFormat(mask int) {
	s := c.Size
	bits := formatBits(c.Level, mask)
	set := func(x, y, i int) { c.mod[y*s+x] = bits>>i&1 != 0 }
	for i := 0; i < 6; i++ {
		set(8, i, i)
	}
	set(8, 7, 6)
	set(8, 8, 7)
	set(7, 8, 8)
	for i := 9; i < 15; i++ {
		set(14-i, 8, i)
	}
	for i := 0; i < 8; i++ {
		set(s-1-i, 8, i)
	}
	for i := 8; i < 15; i++ {
		set(8, s-15+i, i)
	}
	c.mod[(s-8)*s+8] = true
}

// zigzag calls f for every data module in placement order: two-column
// strips from the right, alternating upward and downward, skipping the
// vertical timing column.
func zigzag(s int, fn []bool, f func(i int)) {
	for right := s - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		up := (right+1)&2 == 0
		for vert := 0; vert < s; vert++ {
			y := vert
			if up {
				y = s - 1 - vert
			}
			for j := 0; j < 2; j++ {
				if i := y*s + right - j; !fn[i] {
					f(i)
				}
			}
		}
	}
}

// drawCodewords places the codewords; remainder bits stay light (0).
func (c *Code) drawCodewords(cw []byte, fn []bool) {
	k := 0
	zigzag(c.Size, fn, func(i int) {
		if k < len(cw)*8 {
			c.mod[i] = cw[k/8]>>(7-k%8)&1 != 0
		}
		k++
	})
}

// maskBit reports whether data mask m inverts the module at column x, row y.
func maskBit(m, x, y int) bool {
	switch m {
	case 0:
		return (x+y)%2 == 0
	case 1:
		return y%2 == 0
	case 2:
		return x%3 == 0
	case 3:
		return (x+y)%3 == 0
	case 4:
		return (x/3+y/2)%2 == 0
	case 5:
		return x*y%2+x*y%3 == 0
	case 6:
		return (x*y%2+x*y%3)%2 == 0
	default:
		return ((x+y)%2+x*y%3)%2 == 0
	}
}

// applyMask XORs mask m onto all data modules (applying twice undoes it).
func (c *Code) applyMask(m int, fn []bool) {
	for i := range c.mod {
		if !fn[i] && maskBit(m, i%c.Size, i/c.Size) {
			c.mod[i] = !c.mod[i]
		}
	}
}

// penalty computes the mask evaluation score (rules N1..N4).
func (c *Code) penalty() int {
	s := c.Size
	p := 0
	line := make([]bool, s)
	runs := make([]int, 0, s+2)
	for dir := 0; dir < 2; dir++ {
		for a := 0; a < s; a++ {
			for b := 0; b < s; b++ {
				if dir == 0 {
					line[b] = c.mod[a*s+b] // row a
				} else {
					line[b] = c.mod[b*s+a] // column a
				}
			}
			p += linePenalty(line, runs)
		}
	}
	dark := 0
	for y := 0; y < s; y++ {
		for x := 0; x < s; x++ {
			d := c.mod[y*s+x]
			if d {
				dark++
			}
			// N2: each 2x2 block of one colour scores 3.
			if x+1 < s && y+1 < s && d == c.mod[y*s+x+1] && d == c.mod[(y+1)*s+x] && d == c.mod[(y+1)*s+x+1] {
				p += 3
			}
		}
	}
	// N4: 10 points per full 5% step of deviation from 50% dark.
	return p + abs(20*dark-10*s*s)/(s*s)*10
}

// linePenalty scores one row or column for N1 (runs of 5+ same-colour
// modules: 3 + run-5) and N3 (dark:light:dark:light:dark = n:n:3n:n:n
// with at least 4n light modules on either side, a light run reaching the
// symbol edge counting as long enough because of the quiet zone: 40 each).
// This gives the same scores, and so the same mask choice, as libqrencode.
func linePenalty(line []bool, runs []int) int {
	// runs alternates light, dark, light, ... and starts and ends with a
	// (possibly empty) light run; the first and last ones continue into
	// the light surround for N3.
	runs = append(runs[:0], 0)
	cur := false
	for _, d := range line {
		if d != cur {
			runs = append(runs, 0)
			cur = d
		}
		runs[len(runs)-1]++
	}
	if cur {
		runs = append(runs, 0)
	}
	last := len(runs) - 1
	p := 0
	for i, r := range runs {
		if r >= 5 {
			p += 3 + r - 5
		}
		// Dark runs are at odd indices; test i as the 3n centre.
		if i%2 == 1 && i >= 3 && i+3 <= last && r%3 == 0 {
			n := r / 3
			if runs[i-2] == n && runs[i-1] == n && runs[i+1] == n && runs[i+2] == n &&
				(i-3 == 0 || runs[i-3] >= 4*n || i+3 == last || runs[i+3] >= 4*n) {
				p += 40
			}
		}
	}
	return p
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
