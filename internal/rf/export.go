package rf

import (
	"bufio"
	"errors"
	"fmt"
	"io"
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
