package handler

import "testing"

func TestFormatters(t *testing.T) {
	for in, want := range map[float64]string{0: "0", 999: "999", 1000: "1,000", -1234567: "-1,234,567", 12.5: "12.50"} {
		if got := count(in); got != want {
			t.Errorf("count(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{1234.567: "1,234.57", 2.999: "3.00", 0.05: "0.05", -12.3: "-12.30"} {
		if got := money(in); got != want {
			t.Errorf("money(%v) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[float64]string{5: "0:05", 65: "1:05", 3723: "1:02:03"} {
		if got := clock(in); got != want {
			t.Errorf("clock(%v) = %q, want %q", in, got, want)
		}
	}
	if got := table([]string{"a", "b"}, [][]string{{"x|y", "1"}}); got != "| a | b |\n|---|---|\n| x\\|y | 1 |\n" {
		t.Errorf("table = %q", got)
	}
}
