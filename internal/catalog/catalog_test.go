package catalog

import (
	"strings"
	"testing"
	"testing/fstest"

	"steamcmd-serve/games"
)

func TestLoadBuiltinGames(t *testing.T) {
	got, err := Load(games.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"enshrouded", "valheim"} {
		if got[id] == nil {
			t.Errorf("missing built-in game %q (have %v)", id, IDs(got))
		}
	}
	e := got["enshrouded"]
	if e.Manifest.Steam.Platform != "windows" || e.ConfigTemplate == nil {
		t.Errorf("enshrouded: platform=%q template=%v", e.Manifest.Steam.Platform, e.ConfigTemplate)
	}
	if !e.Ready.MatchString("[Session] 'HostOnline' (up)!") {
		t.Error("enshrouded readiness regex does not match documented log line")
	}
}

func TestLoadRejectsBadManifests(t *testing.T) {
	schema, err := games.FS.ReadFile("manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	const settings = `{"type":"object"}`
	base := `
id: demo
name: Demo
steam: {serverAppId: 1, platform: linux}
runtime: {image: steamcmd-base}
launch: {command: ["./srv"]}
ports:
  - {name: game, default: 3000, protocol: udp}
settings: {schema: s.json}
readiness: {logRegex: "ready"}
resources: {memoryMB: 512, diskGB: 1}
mods: {loader: none}
`
	cases := map[string]struct {
		manifest string
		dir      string
		want     string
	}{
		"id mismatch":       {base, "other", "must match directory"},
		"bad image":         {strings.Replace(base, "steamcmd-base", "alpine:latest", 1), "demo", "invalid manifest"},
		"providers on none": {strings.Replace(base, "{loader: none}", "{loader: none, providers: [{type: nexus, gameDomain: x}]}", 1), "demo", "invalid manifest"},
		"derive unknown":    {strings.Replace(base, "protocol: udp}", "protocol: udp}\n  - {name: q, default: 3001, protocol: udp, derive: {from: nope, offset: 1}}", 1), "demo", "non-derived port"},
		"derive mismatch":   {strings.Replace(base, "protocol: udp}", "protocol: udp}\n  - {name: q, default: 3005, protocol: udp, derive: {from: game, offset: 1}}", 1), "demo", "default 3005"},
		"bad template":      {strings.Replace(base, `["./srv"]`, `["{{ .Settings.x"]`, 1), "demo", "launch.command[0]"},
		"bad regex":         {strings.Replace(base, `"ready"`, `"("`, 1), "demo", "readiness.logRegex"},
		"unknown field":     {strings.Replace(base, `["./srv"]`, `["{{ .Paths.data }}"]`, 1), "demo", "dry run"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"manifest.schema.json":    {Data: schema},
				tc.dir + "/manifest.yaml": {Data: []byte(tc.manifest)},
				tc.dir + "/s.json":        {Data: []byte(settings)},
			}
			_, err := Load(fsys)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want error containing %q, got %v", tc.want, err)
			}
		})
	}
}
