package catalog

import (
	"encoding/json"
	"errors"
	"testing"

	"steamcmd-serve/games"
)

func TestCompleteSettings(t *testing.T) {
	gs, err := Load(games.FS)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("enshrouded defaults and generated group passwords", func(t *testing.T) {
		in := Settings{"name": "My World"}
		s, err := gs["enshrouded"].CompleteSettings(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(in) != 1 {
			t.Error("input was modified")
		}
		if s["name"] != "My World" || s["slotCount"] != json.Number("16") {
			t.Errorf("name/slotCount = %v/%v", s["name"], s["slotCount"])
		}
		gameSettings := s["gameSettings"].(map[string]any)
		if gameSettings["dayTimeDuration"] != json.Number("1800000000000") {
			t.Errorf("dayTimeDuration = %v", gameSettings["dayTimeDuration"])
		}
		seen := map[string]bool{}
		for _, g := range s["userGroups"].([]any) {
			pw := g.(map[string]any)["password"].(string)
			if len(pw) < 16 || seen[pw] {
				t.Errorf("weak or repeated generated password (len %d)", len(pw))
			}
			seen[pw] = true
		}
		if len(seen) != 4 {
			t.Errorf("want 4 groups, got %d", len(seen))
		}
	})

	t.Run("explicit password kept", func(t *testing.T) {
		s, err := gs["valheim"].CompleteSettings(Settings{"password": "hunter22"})
		if err != nil {
			t.Fatal(err)
		}
		if s["password"] != "hunter22" || s["world"] != "Dedicated" {
			t.Errorf("got %v", s)
		}
	})

	t.Run("launch command renders", func(t *testing.T) {
		g := gs["valheim"]
		s, err := g.CompleteSettings(Settings{"password": "hunter22", "public": true})
		if err != nil {
			t.Fatal(err)
		}
		cmd, err := g.LaunchCommand(TemplateContext{Settings: s, Ports: map[string]int{"game": 2456, "query": 2457}, Paths: Paths{Server: "/server", Data: "/data"}})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"./valheim_server.x86_64", "-nographics", "-batchmode", "-name", "Valheim Server", "-port", "2456", "-world", "Dedicated", "-password", "hunter22", "-public", "1", "-savedir", "/data/saves"}
		if len(cmd) != len(want) {
			t.Fatalf("got %q", cmd)
		}
		for i := range want {
			if cmd[i] != want[i] {
				t.Fatalf("arg %d: got %q want %q", i, cmd[i], want[i])
			}
		}
	})

	t.Run("invalid rejected", func(t *testing.T) {
		_, err := gs["enshrouded"].CompleteSettings(Settings{"slotCount": 17})
		var se *SettingsError
		if !errors.As(err, &se) {
			t.Fatalf("want SettingsError, got %v", err)
		}
	})
}
