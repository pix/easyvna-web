package rf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// CSV import.
//
// Spreadsheet exports from VNA programs differ in their column names, so
// the reader looks at the header words rather than at a fixed layout:
//
//   - a frequency column ("Frequency (Hz)", "freq_hz", "Freq(MHz)"...); the
//     unit comes from the name, or from the values when the name has none;
//   - for S11 and (optionally) S21, either real + imaginary ("S11 Re",
//     "Re(S11)", "s11_re"), dB + phase ("S11 dB", "S11 Phase (deg)") or
//     linear magnitude + phase ("|S11|", "S11 Mag", "S11 Angle");
//   - or resistance + reactance ("R (Ω)", "X", "Resistance") for S11.
//
// Columns without an S-parameter name are taken as S11. A file without a
// header is read as "frequency, S11 re, S11 im[, S21 re, S21 im]".
// Separators may be commas, semicolons, tabs or spaces; with semicolons or
// tabs a decimal comma ("1,25") is accepted. Lines starting with "!" or
// "//" are comments.

// csvCol describes what one column holds.
type csvCol struct {
	param string // "s11", "s21", "" (frequency) or "-" (ignored)
	qty   string // re, im, db, mag, ph, r, x, freq
	rad   bool   // phase in radians
	mult  float64
}

var camelRe = regexp.MustCompile(`([a-z])([A-Z])`)

func csvTokens(name string) []string {
	name = camelRe.ReplaceAllString(name, "$1 $2")
	return strings.FieldsFunc(strings.ToLower(name), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func has(tok []string, words ...string) bool { return containsAny(tok, words...) }

// classify maps a header cell to a column kind.
func classify(name string) csvCol {
	tok := csvTokens(name)
	c := csvCol{param: "s11"}
	if len(tok) == 0 {
		return csvCol{param: "-"}
	}
	if has(tok, "freq", "frequency", "frequencies", "f", "hz", "khz", "mhz", "ghz") && !has(tok, "re", "im", "db", "real", "imag", "phase") {
		c = csvCol{qty: "freq"}
		switch {
		case has(tok, "ghz"):
			c.mult = 1e9
		case has(tok, "mhz"):
			c.mult = 1e6
		case has(tok, "khz"):
			c.mult = 1e3
		case has(tok, "hz"):
			c.mult = 1
		}
		return c
	}
	switch {
	case has(tok, "s21"):
		c.param = "s21"
	case has(tok, "s12", "s22"):
		return csvCol{param: "-"}
	}
	// Derived columns that cannot give the phase back.
	if has(tok, "swr", "vswr", "return", "loss", "rl", "ml", "q", "l", "c", "group", "delay", "smith") {
		return csvCol{param: "-"}
	}
	isZ := has(tok, "z", "impedance")
	switch {
	case has(tok, "re", "real", "rel"):
		c.qty = "re"
	case has(tok, "im", "imag", "imaginary"):
		c.qty = "im"
	case has(tok, "db", "logmag") || has(tok, "log") && has(tok, "mag"):
		c.qty = "db"
	case has(tok, "phase", "ph", "ang", "angle", "arg", "deg", "degrees", "rad"):
		c.qty, c.rad = "ph", has(tok, "rad", "radians") && !has(tok, "deg", "degrees")
	case has(tok, "mag", "magnitude", "abs", "lin", "linmag", "gamma") || strings.Contains(name, "|"):
		c.qty = "mag"
	case has(tok, "r", "resistance", "rs"):
		c.qty = "r"
	case has(tok, "x", "reactance", "xs"):
		c.qty = "x"
	default:
		return csvCol{param: "-"}
	}
	if isZ && c.qty == "re" {
		c.qty = "r"
	} else if isZ && c.qty == "im" {
		c.qty = "x"
	}
	if c.param == "s21" && (c.qty == "r" || c.qty == "x") {
		return csvCol{param: "-"}
	}
	return c
}

// ReadCSV reads a measurement exported as CSV (see the package notes above).
func ReadCSV(r io.Reader) (*Measurement, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var header []string
	var rows [][]float64
	var cols []csvCol
	sep := ""
	lineNo := 0
	m := &Measurement{Format: "CSV", Z0: Z0}
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "//") {
			if c := strings.TrimSpace(strings.TrimLeft(line, "!/")); c != "" && len(m.Comments) < 8 {
				m.Comments = append(m.Comments, c)
			}
			continue
		}
		hashed := strings.HasPrefix(line, "#")
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		if sep == "" {
			sep = pickSep(line)
		}
		cells := splitCSV(line, sep)
		numCells := cells
		if sep == ";" || sep == "\t" { // decimal commas: "1,25"
			numCells = make([]string, len(cells))
			for i, c := range cells {
				numCells[i] = strings.Replace(c, ",", ".", 1)
			}
		}
		if cols == nil {
			if nums, ok := parseRow(numCells, false); ok && !hashed {
				// No header: frequency, S11 re, im [, S21 re, im].
				if len(nums) < 3 {
					return nil, fmt.Errorf("csv line %d: a file without a header needs at least 3 columns (frequency, S11 real, S11 imaginary)", lineNo)
				}
				cols = []csvCol{{qty: "freq"}, {param: "s11", qty: "re"}, {param: "s11", qty: "im"}}
				note := "no header: columns read as frequency, S11 re, S11 im"
				if len(nums) >= 5 {
					cols = append(cols, csvCol{param: "s21", qty: "re"}, csvCol{param: "s21", qty: "im"})
					note += ", S21 re, S21 im"
				}
				m.Notes = append(m.Notes, note)
			} else {
				if hashed && len(cells) < 2 {
					continue // a "# comment" line
				}
				header = cells
				for _, h := range cells {
					cols = append(cols, classify(h))
				}
				continue
			}
		}
		if hashed {
			continue
		}
		nums, ok := parseRow(numCells, true)
		if !ok {
			return nil, fmt.Errorf("csv line %d: %q is not a row of numbers", lineNo, truncate(line, 60))
		}
		rows = append(rows, nums)
		if len(rows) > MaxImportPoints {
			return nil, fmt.Errorf("csv: more than %d rows", MaxImportPoints)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("csv: %w", err)
	}
	if cols == nil {
		return nil, errors.New("csv: the file is empty")
	}
	return buildFromColumns(m, header, cols, rows)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func pickSep(line string) string {
	best, n := "", 0
	for _, s := range []string{",", ";", "\t"} {
		if c := strings.Count(line, s); c > n {
			best, n = s, c
		}
	}
	if best == "," && strings.Count(line, ";") > 0 {
		return ";" // "1,5;2,5" style with decimal commas
	}
	return best
}

func splitCSV(line, sep string) []string {
	var out []string
	if sep == "" {
		out = strings.Fields(line)
	} else {
		out = strings.Split(line, sep)
	}
	for i := range out {
		out[i] = strings.Trim(strings.TrimSpace(out[i]), `"'`)
	}
	return out
}

func parseRow(cells []string, allowEmpty bool) ([]float64, bool) {
	out := make([]float64, len(cells))
	for i, c := range cells {
		if c == "" && allowEmpty {
			out[i] = math.NaN()
			continue
		}
		v, err := strconv.ParseFloat(c, 64)
		if err != nil || math.IsInf(v, 0) {
			return nil, false
		}
		out[i] = v
	}
	return out, len(cells) > 0
}

// buildFromColumns turns the classified columns into a Measurement.
func buildFromColumns(m *Measurement, header []string, cols []csvCol, rows [][]float64) (*Measurement, error) {
	idx := func(param, qty string) int {
		for i, c := range cols {
			if c.param == param && c.qty == qty {
				return i
			}
		}
		return -1
	}
	fi := -1
	for i, c := range cols {
		if c.qty == "freq" {
			fi = i
			break
		}
	}
	if fi < 0 {
		return nil, fmt.Errorf("csv: no frequency column found in the header %q", strings.Join(header, ", "))
	}
	if len(rows) == 0 {
		return nil, errors.New("csv: no data rows")
	}
	// value builds a complex parameter from the best pair of columns.
	type getter func(row []float64) (complex128, bool)
	pick := func(param string) (getter, string) {
		get := func(row []float64, i int) (float64, bool) {
			if i >= len(row) || math.IsNaN(row[i]) {
				return 0, false
			}
			return row[i], true
		}
		phase := func(row []float64) (float64, bool) {
			i := idx(param, "ph")
			v, ok := get(row, i)
			if ok && !cols[i].rad {
				v *= math.Pi / 180
			}
			return v, ok
		}
		if a, b := idx(param, "re"), idx(param, "im"); a >= 0 && b >= 0 {
			return func(row []float64) (complex128, bool) {
				re, ok1 := get(row, a)
				im, ok2 := get(row, b)
				return complex(re, im), ok1 && ok2
			}, "real/imaginary"
		}
		if a, b := idx(param, "db"), idx(param, "ph"); a >= 0 && b >= 0 {
			return func(row []float64) (complex128, bool) {
				d, ok1 := get(row, a)
				p, ok2 := phase(row)
				return cmplx.Rect(math.Pow(10, d/20), p), ok1 && ok2
			}, "dB/phase"
		}
		if a, b := idx(param, "mag"), idx(param, "ph"); a >= 0 && b >= 0 {
			return func(row []float64) (complex128, bool) {
				mg, ok1 := get(row, a)
				p, ok2 := phase(row)
				return cmplx.Rect(mg, p), ok1 && ok2
			}, "magnitude/phase"
		}
		if a, b := idx(param, "r"), idx(param, "x"); a >= 0 && b >= 0 && param == "s11" {
			return func(row []float64) (complex128, bool) {
				re, ok1 := get(row, a)
				x, ok2 := get(row, b)
				return GammaFromZ(complex(re, x)), ok1 && ok2
			}, "R/X"
		}
		return nil, ""
	}
	g11, how := pick("s11")
	if g11 == nil {
		return nil, fmt.Errorf("csv: no S11 found in the header %q: needs real and imaginary parts, dB or magnitude with phase, or R and X (SWR or return loss alone has no phase)", strings.Join(header, ", "))
	}
	g21, _ := pick("s21")
	if how != "real/imaginary" {
		m.Notes = append(m.Notes, "S11 from "+how+" columns")
	}
	type pt struct {
		f        float64
		s11, s21 complex128
	}
	pts := make([]pt, 0, len(rows))
	mult := cols[fi].mult
	if mult == 0 {
		mx := 0.0
		for _, r := range rows {
			if fi < len(r) && !math.IsNaN(r[fi]) {
				mx = math.Max(mx, r[fi])
			}
		}
		switch {
		case mx < 100:
			mult = 1e9
		case mx < 1e5:
			mult = 1e6
		default:
			mult = 1
		}
		if mult != 1 {
			m.Notes = append(m.Notes, fmt.Sprintf("frequency column has no unit: read as %s", map[float64]string{1e9: "GHz", 1e6: "MHz"}[mult]))
		}
	}
	for k, r := range rows {
		if fi >= len(r) || math.IsNaN(r[fi]) {
			return nil, fmt.Errorf("csv: data row %d has no frequency", k+1)
		}
		p := pt{f: r[fi] * mult}
		var ok bool
		if p.s11, ok = g11(r); !ok {
			return nil, fmt.Errorf("csv: data row %d (%g Hz) has no S11 value", k+1, p.f)
		}
		if g21 != nil {
			if p.s21, ok = g21(r); !ok {
				g21 = nil // incomplete S21: drop it
			}
		}
		if p.f <= 0 {
			return nil, fmt.Errorf("csv: data row %d: frequency must be above 0", k+1)
		}
		pts = append(pts, p)
	}
	sorted := sort.SliceIsSorted(pts, func(i, j int) bool { return pts[i].f < pts[j].f })
	if !sorted {
		sort.SliceStable(pts, func(i, j int) bool { return pts[i].f < pts[j].f })
		m.Notes = append(m.Notes, "rows sorted by frequency")
	}
	for i, p := range pts {
		if i > 0 && p.f == pts[i-1].f {
			return nil, fmt.Errorf("csv: frequency %g Hz appears twice", p.f)
		}
		m.Freq = append(m.Freq, p.f)
		m.S11 = append(m.S11, p.s11)
		if g21 != nil {
			m.S21 = append(m.S21, p.s21)
		}
	}
	if g21 == nil {
		m.S21 = nil
	}
	return m, nil
}
