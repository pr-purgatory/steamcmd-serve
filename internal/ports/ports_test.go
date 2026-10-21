package ports

import (
	"errors"
	"testing"

	"steamcmd-serve/games"
	"steamcmd-serve/internal/catalog"
)

func specs(t *testing.T, game string) []catalog.Port {
	t.Helper()
	g, err := catalog.Load(games.FS)
	if err != nil {
		t.Fatal(err)
	}
	return g[game].Manifest.Ports
}

func portsOf(bs []Binding) map[string]int {
	m := map[string]int{}
	for _, b := range bs {
		m[b.Name] = b.Port
	}
	return m
}

func TestAssign(t *testing.T) {
	valheim := specs(t, "valheim")
	enshrouded := specs(t, "enshrouded")
	udp := func(ps ...int) map[Key]bool {
		m := map[Key]bool{}
		for _, p := range ps {
			m[Key{p, "udp"}] = true
		}
		return m
	}

	cases := []struct {
		name      string
		specs     []catalog.Port
		requested map[string]int
		taken     map[Key]bool
		want      map[string]int
		wantErr   string // "", "conflict", "invalid"
	}{
		{"defaults", valheim, nil, nil, map[string]int{"game": 2456, "query": 2457}, ""},
		{"default taken moves whole group", valheim, nil, udp(2457), map[string]int{"game": 2458, "query": 2459}, ""},
		{"second enshrouded instance", enshrouded, nil, udp(15637), map[string]int{"query": 15638}, ""},
		{"requested", valheim, map[string]int{"game": 3000}, nil, map[string]int{"game": 3000, "query": 3001}, ""},
		{"requested derived collides", valheim, map[string]int{"game": 3000}, udp(3001), nil, "conflict"},
		{"requested taken", enshrouded, map[string]int{"query": 15637}, udp(15637), nil, "conflict"},
		{"tcp does not clash with udp", enshrouded, nil, map[Key]bool{{15637, "tcp"}: true}, map[string]int{"query": 15637}, ""},
		{"derived not settable", valheim, map[string]int{"query": 3001}, nil, nil, "invalid"},
		{"unknown name", valheim, map[string]int{"rcon": 3001}, nil, nil, "invalid"},
		{"out of range", enshrouded, map[string]int{"query": 80}, nil, nil, "invalid"},
		{"derived beyond max", valheim, map[string]int{"game": 65535}, nil, nil, "invalid"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Assign(tc.specs, tc.requested, tc.taken)
			var ce *ConflictError
			var ie *InvalidError
			switch tc.wantErr {
			case "conflict":
				if !errors.As(err, &ce) {
					t.Fatalf("want ConflictError, got %v (%v)", err, got)
				}
				return
			case "invalid":
				if !errors.As(err, &ie) {
					t.Fatalf("want InvalidError, got %v (%v)", err, got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if g := portsOf(got); len(g) != len(tc.want) || func() bool {
				for k, v := range tc.want {
					if g[k] != v {
						return true
					}
				}
				return false
			}() {
				t.Fatalf("got %v want %v", g, tc.want)
			}
		})
	}
}
