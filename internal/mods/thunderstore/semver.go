package thunderstore

import (
	"fmt"
	"strconv"
	"strings"
)

type semver [3]int

func parseSemver(s string) (semver, error) {
	parts := strings.Split(s, ".")
	var v semver
	if len(parts) != 3 {
		return v, fmt.Errorf("invalid version %q", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || p == "" || p[0] == '+' {
			return v, fmt.Errorf("invalid version %q", s)
		}
		v[i] = n
	}
	return v, nil
}

func (a semver) cmp(b semver) int {
	for i := range a {
		switch {
		case a[i] < b[i]:
			return -1
		case a[i] > b[i]:
			return 1
		}
	}
	return 0
}

// splitDep parses "Namespace-Name-1.2.3".
func splitDep(s string) (id, ver string, err error) {
	parts := strings.Split(s, "-")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid dependency string %q", s)
	}
	if _, err := parseSemver(parts[2]); err != nil {
		return "", "", fmt.Errorf("invalid dependency string %q: %w", s, err)
	}
	return parts[0] + "-" + parts[1], parts[2], nil
}
