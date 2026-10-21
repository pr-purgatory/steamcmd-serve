package instance

import "testing"

func TestInstallProgress(t *testing.T) {
	cases := []struct {
		line string
		want float64
		ok   bool
	}{
		{" Update state (0x61) downloading, progress: 50.00 (5 / 10)", 0.4, true},
		{" Update state (0x81) verifying update, progress: 15.07 (1221173715 / 8102043451)", 0.8 + 0.2*0.1507, true},
		{" Update state (0x3) reconfiguring, progress: 0.00 (0 / 0)", 0, false},
		{" Update state (0x0) unknown, progress: 0.00 (0 / 0)", 0, false},
		{"Success! App '2278520' fully installed.", 0, false},
	}
	for _, c := range cases {
		got, ok := installProgress(c.line)
		if ok != c.ok || (ok && (got-c.want > 1e-9 || c.want-got > 1e-9)) {
			t.Errorf("%q: got %v,%v want %v,%v", c.line, got, ok, c.want, c.ok)
		}
	}
}
