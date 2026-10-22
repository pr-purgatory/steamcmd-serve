package instance

import "testing"

func TestSamePackage(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"A-Mod", "A-Mod", true},
		{"A-Mod", "B-Mod", false},
		{"42:777", "42:888", true},
		{"42:777", "42", true},
		{"42:777", "421:777", false},
	} {
		if got := samePackage(c.a, c.b); got != c.want {
			t.Errorf("samePackage(%q, %q) = %v", c.a, c.b, got)
		}
	}
}
