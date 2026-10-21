package catalog

import (
	"testing"

	"steamcmd-serve/games"
)

func TestSecrets(t *testing.T) {
	gs, err := Load(games.FS)
	if err != nil {
		t.Fatal(err)
	}
	g := gs["enshrouded"]
	stored, err := g.CompleteSettings(Settings{})
	if err != nil {
		t.Fatal(err)
	}
	groups := stored["userGroups"].([]any)
	pw := func(s Settings, i int) any { return s["userGroups"].([]any)[i].(map[string]any)["password"] }
	adminPW, guestPW := pw(stored, 0), pw(stored, 2)

	masked := g.MaskSecrets(stored)
	if m, ok := pw(masked, 0).(map[string]any); !ok || m["hasValue"] != true {
		t.Fatalf("masked password = %v", pw(masked, 0))
	}
	if pw(stored, 0) != adminPW {
		t.Fatal("MaskSecrets modified its input")
	}

	// Client removes "Friend" (index 1) and sends the rest back masked: passwords must
	// follow group names, not indexes.
	in := g.MaskSecrets(stored)
	ig := in["userGroups"].([]any)
	in["userGroups"] = append([]any{ig[0]}, ig[2:]...)
	merged := g.MergeSecrets(in, stored)
	if pw(merged, 0) != adminPW || pw(merged, 1) != guestPW {
		t.Fatalf("merge by x-key failed: got %v / %v", pw(merged, 0), pw(merged, 1))
	}

	// A typed value replaces; a new group with a placeholder gets "" (then generated).
	in = g.MaskSecrets(stored)
	in["userGroups"].([]any)[0].(map[string]any)["password"] = "typed-new-password"
	newGroup := deepCopy(groups[3]).(map[string]any)
	newGroup["name"] = "Mods"
	newGroup["password"] = map[string]any{"hasValue": true}
	in["userGroups"] = append(in["userGroups"].([]any), newGroup)
	merged = g.MergeSecrets(in, stored)
	if pw(merged, 0) != "typed-new-password" || pw(merged, 4) != "" {
		t.Fatalf("typed/new: %v / %v", pw(merged, 0), pw(merged, 4))
	}
	completed, err := g.CompleteSettings(merged)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := pw(completed, 4).(string); len(p) < 16 {
		t.Fatal("new group's password was not generated")
	}

	enc := g.MapSecrets(stored, func(v any) any { return "enc:" + v.(string) })
	if pw(enc, 0) != "enc:"+adminPW.(string) || enc["name"] != stored["name"] {
		t.Fatalf("MapSecrets: %v", pw(enc, 0))
	}
}
