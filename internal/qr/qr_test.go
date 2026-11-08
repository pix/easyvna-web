package qr

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The golden files in testdata were produced by libqrencode 4.1.1
// (testdata/gen.py); the tests do not need qrencode at run time.
//
// Every golden symbol is compared twice: once with the version and mask
// qrencode chose (read back from its format information) forced, which
// must give an identical matrix, and once with automatic selection, which
// must pick the same version and mask. Our N1..N4 scoring matches
// libqrencode's: N3 counts n:n:3n:n:n once per occurrence, a light run
// touching the symbol edge counts as >= 4n (quiet zone), and N4 is
// 10*floor(|dark% - 50| / 5). The only difference is N4 rounding
// (libqrencode rounds the dark percentage first); it never changed the
// choice in the 167 symbols tested. Should a regenerated golden file
// disagree only on the mask, relax the auto-mask check for that symbol
// with a comment rather than the forced-mask comparison.

var levelNames = map[string]Level{"L": L, "M": M, "Q": Q, "H": H}

// rows renders a code as '#'/'.' lines, the golden file format.
func rows(c *Code) []string {
	out := make([]string, c.Size)
	for y := range out {
		b := make([]byte, c.Size)
		for x := range b {
			b[x] = '.'
			if c.Dark(x, y) {
				b[x] = '#'
			}
		}
		out[y] = string(b)
	}
	return out
}

func rowsHash(r []string) string {
	h := sha256.Sum256([]byte(strings.Join(r, "\n") + "\n"))
	return hex.EncodeToString(h[:])
}

// readFormat decodes both format information copies of a symbol given as a
// dark(x, y) function and returns level and mask; ok is false if a copy is
// not a valid BCH word or the copies disagree.
func readFormat(size int, dark func(x, y int) bool) (lvl Level, mask int, ok bool) {
	var a, b int
	bit := func(v *int, i, x, y int) {
		if dark(x, y) {
			*v |= 1 << i
		}
	}
	for i := 0; i < 6; i++ {
		bit(&a, i, 8, i)
	}
	bit(&a, 6, 8, 7)
	bit(&a, 7, 8, 8)
	bit(&a, 8, 7, 8)
	for i := 9; i < 15; i++ {
		bit(&a, i, 14-i, 8)
	}
	for i := 0; i < 8; i++ {
		bit(&b, i, size-1-i, 8)
	}
	for i := 8; i < 15; i++ {
		bit(&b, i, 8, size-15+i)
	}
	if a != b || !dark(8, size-8) {
		return 0, 0, false
	}
	d := (a ^ 0x5412) >> 10
	lvl = [4]Level{M, L, H, Q}[d>>3]
	mask = d & 7
	return lvl, mask, formatBits(lvl, mask) == a
}

// readVersion returns both 18-bit version information copies.
func readVersion(c *Code) (a, b int) {
	for i := 0; i < 18; i++ {
		x, y := c.Size-11+i%3, i/3
		if c.Dark(x, y) {
			a |= 1 << i
		}
		if c.Dark(y, x) {
			b |= 1 << i
		}
	}
	return
}

type golden struct {
	name   string
	lvl    Level
	input  []byte
	minver int
	rows   []string
}

func loadGoldens(t *testing.T) []golden {
	files, err := filepath.Glob("testdata/*.txt")
	if err != nil {
		t.Fatal(err)
	}
	var gs []golden
	for _, f := range files {
		if filepath.Base(f) == "sweep.txt" {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		g := golden{name: filepath.Base(f)}
		for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			k, v, _ := strings.Cut(l, " ")
			switch k {
			case "level":
				g.lvl = levelNames[v]
			case "input":
				if g.input, err = hex.DecodeString(v); err != nil {
					t.Fatal(f, err)
				}
			case "minver":
				g.minver, _ = strconv.Atoi(v)
			default:
				g.rows = append(g.rows, l)
			}
		}
		gs = append(gs, g)
	}
	if len(gs) < 5 {
		t.Fatalf("only %d golden files", len(gs))
	}
	return gs
}

func TestGolden(t *testing.T) {
	for _, g := range loadGoldens(t) {
		t.Run(g.name, func(t *testing.T) {
			size := len(g.rows)
			v := (size - 17) / 4
			lvl, mask, ok := readFormat(size, func(x, y int) bool { return g.rows[y][x] == '#' })
			if !ok || lvl != g.lvl {
				t.Fatalf("golden format info: ok=%v level=%v want %v", ok, lvl, g.lvl)
			}
			c, err := encode(g.input, g.lvl, v, mask)
			if err != nil {
				t.Fatal(err)
			}
			got := rows(c)
			for y := range got {
				if got[y] != g.rows[y] {
					t.Fatalf("v%d mask %d: row %d differs\n got %s\nwant %s", v, mask, y, got[y], g.rows[y])
				}
			}
			// Automatic choices.
			var auto *Code
			if g.minver == 0 {
				auto, err = Encode(g.input, g.lvl)
			} else {
				auto, err = encode(g.input, g.lvl, v, -1)
			}
			if err != nil {
				t.Fatal(err)
			}
			if auto.Version != v {
				t.Errorf("Encode picked version %d, qrencode %d", auto.Version, v)
			}
			if auto.Mask != mask {
				t.Errorf("auto mask %d, qrencode chose %d", auto.Mask, mask)
			}
			selfCheck(t, auto, g.input)
		})
	}
}

// pattern matches gen.py's sweep input.
func pattern(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(1 + (i*37+11)%255)
	}
	return b
}

// TestSweep covers every version and level: capacity (smallest version
// selection), block structure, alignment and version information all show
// up in the full-symbol hash.
func TestSweep(t *testing.T) {
	f, err := os.Open("testdata/sweep.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "#") {
			continue
		}
		var ls, hash string
		var v, size, mask int
		if _, err := parseSweepLine(sc.Text(), &ls, &v, &size, &mask, &hash); err != nil {
			t.Fatal(err)
		}
		lvl := levelNames[ls]
		n++
		if got := capacity(v, lvl); got != size {
			t.Errorf("%s v%d: capacity %d, want %d", ls, v, got, size)
		}
		in := pattern(size)
		c, err := encode(in, lvl, v, mask)
		if err != nil {
			t.Fatal(err)
		}
		if h := rowsHash(rows(c)); h != hash {
			t.Errorf("%s v%d mask %d: matrix differs from qrencode", ls, v, mask)
		}
		auto, err := Encode(in, lvl)
		if err != nil {
			t.Fatal(err)
		}
		if auto.Version != v {
			t.Errorf("%s %d bytes: version %d, want %d", ls, size, auto.Version, v)
		}
		if auto.Mask != mask {
			t.Errorf("%s v%d: auto mask %d, qrencode %d", ls, v, auto.Mask, mask)
		}
		if v <= 12 || v%7 == 0 || v == 40 {
			selfCheck(t, auto, in)
		}
		next, err := Encode(pattern(size+1), lvl)
		switch {
		case v == 40 && !errors.Is(err, ErrTooLong):
			t.Errorf("%s: %d bytes should not fit", ls, size+1)
		case v < 40 && (err != nil || next.Version != v+1):
			t.Errorf("%s: %d bytes should need v%d", ls, size+1, v+1)
		}
	}
	if n != 160 {
		t.Fatalf("sweep has %d entries, want 160", n)
	}
}

func parseSweepLine(s string, ls *string, v, size, mask *int, hash *string) (int, error) {
	f := strings.Fields(s)
	if len(f) != 5 {
		return 0, errors.New("bad sweep line: " + s)
	}
	*ls, *hash = f[0], f[4]
	var err error
	for i, p := range []*int{v, size, mask} {
		if *p, err = strconv.Atoi(f[i+1]); err != nil {
			return 0, err
		}
	}
	return 5, nil
}

// selfCheck validates a symbol independently of the encoder's placement
// path: format and version information, then it un-masks, reads and
// de-interleaves the codewords, checks every block's Reed-Solomon
// syndromes are zero and parses the byte-mode segment back.
func selfCheck(t *testing.T, c *Code, want []byte) {
	t.Helper()
	if c.Size != 17+4*c.Version || len(c.mod) != c.Size*c.Size {
		t.Fatalf("bad size %d for v%d", c.Size, c.Version)
	}
	lvl, mask, ok := readFormat(c.Size, c.Dark)
	if !ok || lvl != c.Level || mask != c.Mask {
		t.Fatalf("format info: ok=%v level %v mask %d, want %v %d", ok, lvl, mask, c.Level, c.Mask)
	}
	if c.Version >= 7 {
		a, b := readVersion(c)
		if a != b || a>>12 != c.Version || a != versionBits(c.Version) {
			t.Fatalf("version info %#x/%#x for v%d", a, b, c.Version)
		}
	}

	ref := &Code{Size: c.Size, Version: c.Version, Level: c.Level}
	fn := ref.drawFunction()
	raw := make([]byte, rawModules(c.Version)/8)
	k := 0
	zigzag(c.Size, fn, func(i int) {
		x, y := i%c.Size, i/c.Size
		if k < len(raw)*8 && c.mod[i] != maskBit(c.Mask, x, y) {
			raw[k/8] |= 0x80 >> (k % 8)
		}
		k++
	})
	if k != rawModules(c.Version) {
		t.Fatalf("v%d: %d data modules, want %d", c.Version, k, rawModules(c.Version))
	}

	nb, ecc, nShort, shortData := blockLayout(c.Version, c.Level)
	blocks := make([][]byte, nb)
	p := 0
	for i := 0; i <= shortData; i++ {
		for j := range blocks {
			if i < shortData || j >= nShort {
				blocks[j] = append(blocks[j], raw[p])
				p++
			}
		}
	}
	var data []byte
	for j := range blocks {
		data = append(data, blocks[j]...)
	}
	for i := 0; i < ecc; i++ {
		for j := range blocks {
			blocks[j] = append(blocks[j], raw[p])
			p++
		}
	}
	if p != len(raw) {
		t.Fatalf("de-interleave consumed %d of %d codewords", p, len(raw))
	}
	for j, b := range blocks {
		for i, x := 0, byte(1); i < ecc; i, x = i+1, gfMul(x, 2) {
			var s byte
			for _, co := range b {
				s = gfMul(s, x) ^ co
			}
			if s != 0 {
				t.Fatalf("block %d syndrome %d = %#x", j, i, s)
			}
		}
	}

	pos := 0
	get := func(n int) int {
		v := 0
		for ; n > 0; n-- {
			v = v<<1 | int(data[pos/8]>>(7-pos%8)&1)
			pos++
		}
		return v
	}
	if m := get(4); m != 4 {
		t.Fatalf("mode %d, want byte mode", m)
	}
	n := get(countBits(c.Version))
	got := make([]byte, n)
	for i := range got {
		got[i] = byte(get(8))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded %q, want %q", got, want)
	}
}

func TestSelfCheckLevelsAndVersions(t *testing.T) {
	in := []byte("https://www.amazon.fr/dp/B0C1234567?tag=abcd-21")
	for lvl := L; lvl <= H; lvl++ {
		for _, v := range []int{4, 5, 7, 10, 13, 20, 27, 32, 40} {
			if capacity(v, lvl) < len(in) {
				continue
			}
			for _, m := range []int{-1, 0, 3, 7} {
				c, err := encode(in, lvl, v, m)
				if err != nil {
					t.Fatal(err)
				}
				selfCheck(t, c, in)
			}
		}
	}
	c, err := Encode(nil, H)
	if err != nil || c.Version != 1 {
		t.Fatalf("empty input: %v %v", c, err)
	}
	selfCheck(t, c, []byte{})
}

func TestKnownConstants(t *testing.T) {
	// ISO/IEC 18004 Annex C/D examples.
	for v, want := range map[int]int{7: 0x07C94, 8: 0x085BC, 21: 0x15683, 40: 0x28C69} {
		if got := versionBits(v); got != want {
			t.Errorf("version bits v%d = %#x, want %#x", v, got, want)
		}
	}
	if got := formatBits(M, 5); got != 0x40CE { // 00 101 -> 100000011001110
		t.Errorf("format bits M/5 = %#x, want 0x40ce", got)
	}
	// RS example from ISO/IEC 18004 Annex I (1-M "01234567").
	data := []byte{0x10, 0x20, 0x0C, 0x56, 0x61, 0x80, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11, 0xEC, 0x11}
	want := []byte{0xA5, 0x24, 0xD4, 0xC1, 0xED, 0x36, 0xC7, 0x87, 0x2C, 0x55}
	if got := rsRemainder(data, rsGenerator(10)); !bytes.Equal(got, want) {
		t.Errorf("RS ecc % x, want % x", got, want)
	}
	for v, want := range map[int][]int{1: nil, 2: {6, 18}, 7: {6, 22, 38}, 32: {6, 34, 60, 86, 112, 138}, 40: {6, 30, 58, 86, 114, 142, 170}} {
		got := alignPos(v)
		if len(got) != len(want) {
			t.Errorf("align v%d = %v, want %v", v, got, want)
			continue
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("align v%d = %v, want %v", v, got, want)
				break
			}
		}
	}
}

func TestLinePenalty(t *testing.T) {
	line := func(s string) []bool {
		b := make([]bool, len(s))
		for i := range s {
			b[i] = s[i] == '#'
		}
		return b
	}
	for s, want := range map[string]int{
		".....................": 19,      // all light: N1 only, no N3
		"#.###.#..............": 40 + 12, // finder-like at the edge + 14-run
		"....#.###.#....":       40,      // light on both sides
		"..#.###.#..":           40,      // short light runs touching the edges
		"#.#.###.#.#":           0,       // no light margin
		"##..######..##.":       40 + 4,  // 2:2:6:2:2 scaled
	} {
		if got := linePenalty(line(s), nil); got != want {
			t.Errorf("linePenalty(%s) = %d, want %d", s, got, want)
		}
	}
}

func TestCapacity(t *testing.T) {
	check := func(n int, lvl Level, wantV int) {
		t.Helper()
		c, err := Encode(bytes.Repeat([]byte{'a'}, n), lvl)
		if err != nil || c.Version != wantV {
			t.Errorf("%d bytes level %d: %v, want v%d", n, lvl, err, wantV)
		}
	}
	check(213, M, 10)
	check(214, M, 11)
	check(2331, M, 40)
	check(2953, L, 40)
	check(1273, H, 40)
	if _, err := Encode(make([]byte, 2332), M); !errors.Is(err, ErrTooLong) {
		t.Errorf("2332 bytes at M: err %v, want ErrTooLong", err)
	}
	if _, err := encode(make([]byte, 214), M, 10, -1); !errors.Is(err, ErrTooLong) {
		t.Errorf("214 bytes forced v10-M: err %v, want ErrTooLong", err)
	}
	if _, err := Encode(nil, Level(4)); err == nil {
		t.Error("invalid level accepted")
	}
}

func TestSVGPath(t *testing.T) {
	c, err := Encode([]byte("hello"), L)
	if err != nil {
		t.Fatal(err)
	}
	p := c.SVGPath()
	if !strings.HasPrefix(p, "M0 0h7v1h-7z") {
		t.Errorf("path starts %q", p[:min(len(p), 40)])
	}
	// Re-rasterise the path and compare with Dark.
	got := make([]bool, c.Size*c.Size)
	for _, sub := range strings.Split(strings.TrimSuffix(p, "z"), "z") {
		var x, y, w, w2 int
		if _, err := parseSubpath(sub, &x, &y, &w, &w2); err != nil || w != w2 {
			t.Fatalf("bad subpath %q: %v", sub, err)
		}
		for i := 0; i < w; i++ {
			if got[y*c.Size+x+i] {
				t.Fatalf("overlapping runs at %d,%d", x+i, y)
			}
			got[y*c.Size+x+i] = true
		}
	}
	for i, d := range got {
		if d != c.Dark(i%c.Size, i/c.Size) {
			t.Fatalf("path differs from Dark at %d,%d", i%c.Size, i/c.Size)
		}
	}
	if c.Dark(-1, 0) || c.Dark(0, c.Size) {
		t.Error("outside modules must be light")
	}
}

// parseSubpath parses "M<x> <y>h<w>v1h-<w>".
func parseSubpath(s string, x, y, w, w2 *int) (int, error) {
	r := strings.NewReplacer("M", "", "h-", " ", "h", " ", "v1", " ")
	f := strings.Fields(r.Replace(s))
	if len(f) != 4 || !strings.HasPrefix(s, "M") {
		return 0, errors.New("bad subpath")
	}
	var err error
	for i, p := range []*int{x, y, w, w2} {
		if *p, err = strconv.Atoi(f[i]); err != nil {
			return 0, err
		}
	}
	return 4, nil
}
