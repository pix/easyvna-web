package rf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"math/cmplx"
	"strconv"
	"strings"
)

// WriteTouchstone writes a Touchstone v1 file in Hz / RI / 50 Ω format.
// With s21 == nil a one-port (.s1p) file is written. With s21 present a
// two-port (.s2p) file is written; since a NanoVNA only measures S11 and S21,
// S12 and S22 are written as 0 (stated in the header comment).
func WriteTouchstone(w io.Writer, freq []float64, s11, s21 []complex128, comments []string) error {
	if len(freq) != len(s11) {
		return errors.New("touchstone: freq/s11 length mismatch")
	}
	twoPort := s21 != nil
	if twoPort && len(s21) != len(freq) {
		return errors.New("touchstone: freq/s21 length mismatch")
	}
	bw := bufio.NewWriter(w)
	for _, c := range comments {
		for _, l := range strings.Split(c, "\n") {
			fmt.Fprintf(bw, "! %s\n", l)
		}
	}
	if twoPort {
		fmt.Fprintln(bw, "! S12 and S22 were not measured and are written as 0")
	}
	fmt.Fprintf(bw, "# HZ S RI R %g\n", Z0)
	for i, f := range freq {
		fmt.Fprintf(bw, "%s %s %s", fmtFreq(f), fmtF(real(s11[i])), fmtF(imag(s11[i])))
		if twoPort {
			fmt.Fprintf(bw, " %s %s 0 0 0 0", fmtF(real(s21[i])), fmtF(imag(s21[i])))
		}
		fmt.Fprintln(bw)
	}
	return bw.Flush()
}

// WriteCSV writes a CSV with raw and derived columns.
func WriteCSV(w io.Writer, freq []float64, s11, s21 []complex128) error {
	if len(freq) != len(s11) {
		return errors.New("csv: freq/s11 length mismatch")
	}
	twoPort := s21 != nil && len(s21) == len(freq)
	d := Derive(s11, s21)
	bw := bufio.NewWriter(w)
	hdr := "freq_hz,s11_re,s11_im,gamma_mag,swr,return_loss_db,s11_phase_deg,r_ohm,x_ohm"
	if twoPort {
		hdr += ",s21_re,s21_im,s21_db,s21_phase_deg"
	}
	fmt.Fprintln(bw, hdr)
	for i, f := range freq {
		cols := []string{fmtFreq(f), fmtF(real(s11[i])), fmtF(imag(s11[i])), fmtF(d.GammaMag[i]),
			fmtF(d.SWR[i]), fmtF(d.RL[i]), fmtF(d.S11Phase[i]), fmtF(d.R[i]), fmtF(d.X[i])}
		if twoPort {
			cols = append(cols, fmtF(real(s21[i])), fmtF(imag(s21[i])), fmtF(d.S21dB[i]), fmtF(d.S21Phase[i]))
		}
		fmt.Fprintln(bw, strings.Join(cols, ","))
	}
	return bw.Flush()
}

func fmtFreq(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) }
func fmtF(v float64) string    { return strconv.FormatFloat(v, 'g', 9, 64) }

// ReadTouchstone parses a Touchstone v1 file in any of the RI/MA/DB formats
// and Hz/kHz/MHz/GHz units, returning frequencies (Hz), S11 and (for 2-port
// files) S21. It is used by the tests and for importing reference traces.
func ReadTouchstone(r io.Reader) (freq []float64, s11, s21 []complex128, err error) {
	sc := bufio.NewScanner(r)
	mult, format, z0 := 1e9, "MA", 50.0
	var nums []float64
	for sc.Scan() {
		line := sc.Text()
		if i := strings.IndexByte(line, '!'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			f := strings.Fields(strings.ToUpper(line[1:]))
			for k := 0; k < len(f); k++ {
				switch f[k] {
				case "HZ":
					mult = 1
				case "KHZ":
					mult = 1e3
				case "MHZ":
					mult = 1e6
				case "GHZ":
					mult = 1e9
				case "RI", "MA", "DB":
					format = f[k]
				case "R":
					if k+1 < len(f) {
						z0, _ = strconv.ParseFloat(f[k+1], 64)
						k++
					}
				}
			}
			continue
		}
		for _, fld := range strings.Fields(line) {
			v, perr := strconv.ParseFloat(fld, 64)
			if perr != nil {
				return nil, nil, nil, fmt.Errorf("touchstone: %w", perr)
			}
			nums = append(nums, v)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, nil, err
	}
	if z0 != Z0 {
		return nil, nil, nil, fmt.Errorf("touchstone: reference impedance %g not supported", z0)
	}
	pair := func(a, b float64) complex128 {
		switch format {
		case "RI":
			return complex(a, b)
		case "DB":
			a = pow10(a / 20)
		}
		return complexPolar(a, b)
	}
	// Try 2-port (9 numbers per point) when divisible by 9 and not by 3
	// alone ambiguously; callers should know their file type, so decide by
	// the number of columns of the first data row instead.
	stride := 3
	if len(nums)%9 == 0 && len(nums) > 0 && !looksOnePort(nums) {
		stride = 9
	}
	if len(nums)%stride != 0 {
		return nil, nil, nil, errors.New("touchstone: ragged data")
	}
	for i := 0; i < len(nums); i += stride {
		freq = append(freq, nums[i]*mult)
		s11 = append(s11, pair(nums[i+1], nums[i+2]))
		if stride == 9 {
			s21 = append(s21, pair(nums[i+3], nums[i+4]))
		}
	}
	return freq, s11, s21, nil
}

// looksOnePort checks whether interpreting the numbers with stride 3 gives
// monotonically increasing frequencies (which a 9-stride file would not).
func looksOnePort(nums []float64) bool {
	for i := 3; i < len(nums); i += 3 {
		if nums[i] <= nums[i-3] {
			return false
		}
	}
	return true
}

func pow10(x float64) float64 { return math.Pow(10, x) }

func complexPolar(mag, deg float64) complex128 { return cmplx.Rect(mag, deg*math.Pi/180) }
