package rf

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"path"
	"strconv"
	"strings"
)

// Measurement is a sweep read from a file (Touchstone or CSV).
type Measurement struct {
	Freq []float64    // Hz, strictly increasing
	S11  []complex128 // referred to 50 Ω
	S21  []complex128 // nil when the file has no transmission data
	// Format is a short description, e.g. "Touchstone 1-port (.s1p)".
	Format string
	// Z0 is the reference impedance the file was written for (Ω).
	Z0 float64
	// Notes say, in plain words, what was done to the data while reading it
	// (renormalised to 50 Ω, S12/S22 ignored, rows reordered...).
	Notes []string
	// Comments are the file's first comment lines (at most 8).
	Comments []string
}

// Import limits.
const (
	MaxImportPoints = 100001
	minImportPoints = 3
)

// ImportFile reads a measurement file. The format comes from the file name
// (.s1p, .s2p, .csv, .txt...) and, when that is not conclusive, from the
// content.
func ImportFile(name string, data []byte) (*Measurement, error) {
	m, err := importFile(name, data)
	if err == nil && len(m.Freq) < minImportPoints {
		err = fmt.Errorf("only %d frequency point(s): need at least %d", len(m.Freq), minImportPoints)
	}
	return m, err
}

func importFile(name string, data []byte) (*Measurement, error) {
	ext := strings.ToLower(path.Ext(strings.ReplaceAll(name, "\\", "/")))
	switch ext {
	case ".s1p":
		return ReadTouchstoneFile(bytes.NewReader(data), 1)
	case ".s2p":
		return ReadTouchstoneFile(bytes.NewReader(data), 2)
	case ".csv", ".tsv":
		return ReadCSV(bytes.NewReader(data))
	}
	if m := regexpSnp(ext); m > 2 {
		return nil, fmt.Errorf("%d-port Touchstone files are not supported (use .s1p or .s2p)", m)
	}
	if looksTouchstone(data) {
		return ReadTouchstoneFile(bytes.NewReader(data), 0)
	}
	return ReadCSV(bytes.NewReader(data))
}

// regexpSnp returns n for ".sNp" extensions, else 0.
func regexpSnp(ext string) int {
	if len(ext) < 4 || ext[0:2] != ".s" || ext[len(ext)-1] != 'p' {
		return 0
	}
	n, err := strconv.Atoi(ext[2 : len(ext)-1])
	if err != nil {
		return 0
	}
	return n
}

// looksTouchstone reports whether data has a Touchstone option line or
// keyword before its first data line.
func looksTouchstone(data []byte) bool {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		switch {
		case l == "" || strings.HasPrefix(l, "!"):
			continue
		case strings.HasPrefix(l, "#") || strings.HasPrefix(l, "["):
			f := strings.Fields(strings.ToUpper(strings.TrimPrefix(l, "#")))
			return strings.HasPrefix(l, "[") || len(f) == 0 || containsAny(f, "HZ", "KHZ", "MHZ", "GHZ", "S", "RI", "MA", "DB", "R")
		default:
			return false
		}
	}
	return false
}

func containsAny(list []string, want ...string) bool {
	for _, a := range list {
		for _, b := range want {
			if a == b {
				return true
			}
		}
	}
	return false
}

// tsOptions is a parsed Touchstone option line.
type tsOptions struct {
	mult   float64
	param  string
	format string
	z0     float64
}

func parseOptionLine(line string, o *tsOptions) error {
	f := strings.Fields(strings.ToUpper(strings.TrimPrefix(strings.TrimSpace(line), "#")))
	for k := 0; k < len(f); k++ {
		switch f[k] {
		case "HZ":
			o.mult = 1
		case "KHZ":
			o.mult = 1e3
		case "MHZ":
			o.mult = 1e6
		case "GHZ":
			o.mult = 1e9
		case "S", "Y", "Z", "H", "G":
			o.param = f[k]
		case "RI", "MA", "DB":
			o.format = f[k]
		case "R":
			if k+1 >= len(f) {
				return errors.New("the option line has R without a value")
			}
			v, err := strconv.ParseFloat(f[k+1], 64)
			if err != nil || v <= 0 || math.IsInf(v, 0) {
				return fmt.Errorf("the option line has a bad reference impedance %q", f[k+1])
			}
			o.z0 = v
			k++
		default:
			return fmt.Errorf("unknown word %q in the option line %q", f[k], strings.TrimSpace(line))
		}
	}
	return nil
}

// ReadTouchstoneFile parses a Touchstone file with S-parameters in RI, MA or
// DB format, any frequency unit and any (real) reference impedance. ports
// is 1 (.s1p), 2 (.s2p) or 0 to decide from the first data line (3 numbers:
// one port, 9: two ports). Touchstone 2.0 files with one or two ports are
// read too. Data is renormalised to 50 Ω when the file uses another
// reference impedance; for two-port files S11 and S21 are kept (S12 and
// S22 only take part in the renormalisation). Noise data at the end of a
// two-port file is ignored.
func ReadTouchstoneFile(r io.Reader, ports int) (*Measurement, error) {
	opt := tsOptions{mult: 1e9, param: "S", format: "MA", z0: 50}
	sawOption := false
	order12 := false // [Two-Port Data Order] 12_21
	v2 := false
	m := &Measurement{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var rec []float64
	var s12, s22 []complex128
	stride := 0
	if ports == 1 {
		stride = 3
	} else if ports == 2 {
		stride = 9
	}
	noise := false
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := sc.Text()
		if i := strings.IndexByte(line, '!'); i >= 0 {
			if c := strings.TrimSpace(line[i+1:]); c != "" && len(m.Comments) < 8 {
				m.Comments = append(m.Comments, c)
			}
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" || noise {
			continue
		}
		if strings.HasPrefix(line, "[") {
			kw, val, _ := strings.Cut(line[1:], "]")
			kw, val = strings.ToLower(strings.TrimSpace(kw)), strings.TrimSpace(val)
			switch kw {
			case "version":
				v2 = true
			case "number of ports":
				n, err := strconv.Atoi(val)
				if err != nil || n < 1 {
					return nil, fmt.Errorf("touchstone line %d: bad [Number of Ports] %q", lineNo, val)
				}
				if n > 2 {
					return nil, fmt.Errorf("touchstone: %d-port files are not supported (use 1 or 2 ports)", n)
				}
				if ports != 0 && n != ports {
					return nil, fmt.Errorf("touchstone: the file says %d port(s) but its name says %d", n, ports)
				}
				ports, stride = n, 1+2*n*n
			case "two-port data order":
				order12 = strings.HasPrefix(val, "12")
			case "reference":
				f := strings.Fields(val)
				if len(f) == 0 { // values on the next line are not supported
					return nil, errors.New("touchstone: [Reference] without values on the same line is not supported")
				}
				z, err := strconv.ParseFloat(f[0], 64)
				for _, x := range f[1:] {
					if x != f[0] {
						err = errors.New("different per port")
					}
				}
				if err != nil || z <= 0 {
					return nil, fmt.Errorf("touchstone: [Reference] %q is not supported (needs one real impedance for all ports)", val)
				}
				opt.z0 = z
			case "number of frequencies", "network data", "end", "number of noise frequencies":
			case "matrix format":
				if !strings.EqualFold(val, "full") {
					return nil, fmt.Errorf("touchstone: [Matrix Format] %s is not supported (only Full)", val)
				}
			case "noise data": // noise parameters follow; they are not used
				noise = true
			default:
				return nil, fmt.Errorf("touchstone: keyword [%s] is not supported", kw)
			}
			continue
		}
		if strings.HasPrefix(line, "#") {
			if sawOption {
				continue // only the first option line counts
			}
			sawOption = true
			if err := parseOptionLine(line, &opt); err != nil {
				return nil, fmt.Errorf("touchstone line %d: %w", lineNo, err)
			}
			if opt.param != "S" {
				return nil, fmt.Errorf("touchstone: this file holds %s-parameters; only S-parameter files can be opened", opt.param)
			}
			continue
		}
		fields := strings.Fields(line)
		if stride == 0 {
			switch len(fields) {
			case 3:
				ports, stride = 1, 3
			case 9:
				ports, stride = 2, 9
			default:
				return nil, fmt.Errorf("touchstone line %d: %d numbers per line: expected 3 (one port) or 9 (two ports)", lineNo, len(fields))
			}
		}
		for _, fld := range fields {
			v, err := strconv.ParseFloat(fld, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("touchstone line %d: %q is not a number", lineNo, fld)
			}
			rec = append(rec, v)
		}
		for len(rec) >= stride {
			f := rec[0] * opt.mult
			if n := len(m.Freq); n > 0 && f <= m.Freq[n-1] {
				if ports == 2 && !v2 {
					// Touchstone 1: noise parameters follow the network data,
					// starting again at a lower frequency.
					noise = true
					rec = nil
					break
				}
				return nil, fmt.Errorf("touchstone line %d: frequencies must increase (%g after %g Hz)", lineNo, f, m.Freq[n-1])
			}
			if f <= 0 {
				return nil, fmt.Errorf("touchstone line %d: frequency must be above 0", lineNo)
			}
			pair := func(k int) complex128 { return tsPair(opt.format, rec[k], rec[k+1]) }
			m.Freq = append(m.Freq, f)
			m.S11 = append(m.S11, pair(1))
			if stride == 9 {
				a, b := pair(3), pair(5)
				if order12 {
					a, b = b, a
				}
				m.S21 = append(m.S21, a)
				s12 = append(s12, b)
				s22 = append(s22, pair(7))
			}
			rec = rec[stride:]
			if len(m.Freq) > MaxImportPoints {
				return nil, fmt.Errorf("touchstone: more than %d points", MaxImportPoints)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("touchstone: %w", err)
	}
	if len(rec) != 0 {
		return nil, errors.New("touchstone: the last data line is incomplete")
	}
	if len(m.Freq) == 0 {
		return nil, errors.New("touchstone: no data points")
	}
	m.Z0 = opt.z0
	if ports == 2 {
		m.Format = "Touchstone 2-port (.s2p)"
		m.Notes = append(m.Notes, "S11 and S21 used; S12 and S22 are not shown")
	} else {
		m.Format = "Touchstone 1-port (.s1p)"
	}
	if v2 {
		m.Format = strings.Replace(m.Format, "Touchstone", "Touchstone 2.0", 1)
	}
	if opt.z0 != Z0 {
		if ports == 2 {
			if err := renormalize2(opt.z0, m.S11, m.S21, s12, s22); err != nil {
				return nil, err
			}
		} else {
			for i, g := range m.S11 {
				m.S11[i] = Renormalize(g, opt.z0)
			}
		}
		m.Notes = append(m.Notes, fmt.Sprintf("renormalised from %g Ω to 50 Ω", opt.z0))
	}
	return m, nil
}

func tsPair(format string, a, b float64) complex128 {
	switch format {
	case "RI":
		return complex(a, b)
	case "DB":
		a = math.Pow(10, a/20)
	}
	return cmplx.Rect(a, b*math.Pi/180)
}

// Renormalize converts a reflection coefficient referred to z0 into one
// referred to Z0 (50 Ω). It stays finite for Γ = 1 (an open is an open).
func Renormalize(g complex128, z0 float64) complex128 {
	if z0 == Z0 {
		return g
	}
	r := complex((Z0-z0)/(Z0+z0), 0)
	return (g - r) / (1 - r*g)
}

// renormalize2 renormalises a two-port S matrix from z0 (both ports) to
// 50 Ω in place: S' = (S − rI)(I − rS)⁻¹ with r = (50 − z0)/(50 + z0).
func renormalize2(z0 float64, s11, s21, s12, s22 []complex128) error {
	r := complex((Z0-z0)/(Z0+z0), 0)
	for i := range s11 {
		// A = S − rI, B = I − rS.
		a11, a12, a21, a22 := s11[i]-r, s12[i], s21[i], s22[i]-r
		b11, b12, b21, b22 := 1-r*s11[i], -r*s12[i], -r*s21[i], 1-r*s22[i]
		det := b11*b22 - b12*b21
		if cmplx.Abs(det) < 1e-12 {
			return fmt.Errorf("touchstone: cannot renormalise point %d to 50 Ω", i+1)
		}
		i11, i12, i21, i22 := b22/det, -b12/det, -b21/det, b11/det
		s11[i] = a11*i11 + a12*i21
		s21[i] = a21*i11 + a22*i21
		s12[i] = a11*i12 + a12*i22
		s22[i] = a21*i12 + a22*i22
	}
	return nil
}

// ReadTouchstone parses a Touchstone file (one or two ports, decided from
// the data) and returns frequencies (Hz), S11 and, for two-port files, S21,
// referred to 50 Ω.
func ReadTouchstone(r io.Reader) (freq []float64, s11, s21 []complex128, err error) {
	m, err := ReadTouchstoneFile(r, 0)
	if err != nil {
		return nil, nil, nil, err
	}
	return m.Freq, m.S11, m.S21, nil
}
