// Package ports assigns host ports to a game instance.
package ports

import (
	"fmt"
	"sort"

	"steamcmd-serve/internal/catalog"
)

const (
	minPort = 1024
	maxPort = 65535
)

// Binding is one assigned port. Container and host port are equal (ADR 0001 §8).
type Binding struct {
	Name     string `json:"name"`
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

// Key identifies a port on the host.
type Key struct {
	Port     int
	Protocol string
}

func (k Key) String() string { return fmt.Sprintf("%d/%s", k.Port, k.Protocol) }

// ConflictError means a requested port is in use, or no free port was found.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// InvalidError means the request names unknown or derived ports, or ports out of range.
type InvalidError struct{ Msg string }

func (e *InvalidError) Error() string { return e.Msg }

// Assign picks host ports for specs. requested maps non-derived port names to the
// desired port; missing names get the manifest default, or the next free port when
// that is taken. Derived ports follow their base. taken reports ports already in use.
func Assign(specs []catalog.Port, requested map[string]int, taken map[Key]bool) ([]Binding, error) {
	byName := map[string]catalog.Port{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	for name, p := range requested {
		s, ok := byName[name]
		switch {
		case !ok:
			return nil, &InvalidError{fmt.Sprintf("unknown port %q", name)}
		case s.Derive != nil:
			return nil, &InvalidError{fmt.Sprintf("port %q is derived from %q and cannot be set", name, s.Derive.From)}
		case p < minPort || p > maxPort:
			return nil, &InvalidError{fmt.Sprintf("port %q: %d outside %d-%d", name, p, minPort, maxPort)}
		}
	}

	used := map[Key]bool{}
	var out []Binding
	for _, base := range specs {
		if base.Derive != nil {
			continue
		}
		group := withDerived(specs, base)
		free := func(p int) bool {
			for _, s := range group {
				k := Key{p + offset(s), s.Protocol}
				if k.Port < minPort || k.Port > maxPort || taken[k] || used[k] {
					return false
				}
			}
			return true
		}

		port, explicit := requested[base.Name]
		if !explicit {
			port = base.Default
		}
		if explicit {
			for _, s := range group {
				if p := port + offset(s); p < minPort || p > maxPort {
					return nil, &InvalidError{fmt.Sprintf("port %q would be %d, outside %d-%d", s.Name, p, minPort, maxPort)}
				}
			}
		}
		if !free(port) {
			if explicit {
				return nil, &ConflictError{fmt.Sprintf("port %s for %q is in use", describe(group, port), base.Name)}
			}
			found := false
			for p := port + 1; p <= maxPort; p++ {
				if free(p) {
					port, found = p, true
					break
				}
			}
			if !found {
				return nil, &ConflictError{fmt.Sprintf("no free port for %q", base.Name)}
			}
		}
		for _, s := range group {
			b := Binding{Name: s.Name, Port: port + offset(s), Protocol: s.Protocol}
			used[Key{b.Port, b.Protocol}] = true
			out = append(out, b)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// withDerived returns base followed by the ports derived from it.
func withDerived(specs []catalog.Port, base catalog.Port) []catalog.Port {
	group := []catalog.Port{base}
	for _, s := range specs {
		if s.Derive != nil && s.Derive.From == base.Name {
			group = append(group, s)
		}
	}
	return group
}

func offset(s catalog.Port) int {
	if s.Derive == nil {
		return 0
	}
	return s.Derive.Offset
}

func describe(group []catalog.Port, base int) string {
	out := ""
	for i, s := range group {
		if i > 0 {
			out += "+"
		}
		out += Key{base + offset(s), s.Protocol}.String()
	}
	return out
}
