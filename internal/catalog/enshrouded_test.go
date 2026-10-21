package catalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"steamcmd-serve/games"
)

// officialDefault is the default enshrouded_server.json for server v0.9.0.0
// (Server Gameplay Settings doc) with the example passwords blanked.
const officialDefault = `{
"name": "Enshrouded Server",
"saveDirectory": "./savegame",
"logDirectory": "./logs",
"ip": "0.0.0.0",
"queryPort": 15637,
"slotCount": 16,
"tags": [],
"voiceChatMode": "Proximity",
"enableVoiceChat": false,
"enableTextChat": false,
"gameSettingsPreset": "Default",
"gameSettings": {
"playerHealthFactor": 1,
"playerManaFactor": 1,
"playerStaminaFactor": 1,
"playerBodyHeatFactor": 1,
"playerDivingTimeFactor": 1,
"enableDurability": true,
"enableStarvingDebuff": false,
"foodBuffDurationFactor": 1,
"fromHungerToStarving": 600000000000,
"shroudTimeFactor": 1,
"tombstoneMode": "AddBackpackMaterials",
"enableGliderTurbulences": true,
"weatherFrequency": "Normal",
"fishingDifficulty": "Normal",
"miningDamageFactor": 1,
"plantGrowthSpeedFactor": 1,
"resourceDropStackAmountFactor": 1,
"factoryProductionSpeedFactor": 1,
"perkUpgradeRecyclingFactor": 0.500000,
"perkCostFactor": 1,
"experienceCombatFactor": 1,
"experienceMiningFactor": 1,
"experienceExplorationQuestsFactor": 1,
"randomSpawnerAmount": "Normal",
"aggroPoolAmount": "Normal",
"enemyDamageFactor": 1,
"enemyHealthFactor": 1,
"enemyStaminaFactor": 1,
"enemyPerceptionRangeFactor": 1,
"bossDamageFactor": 1,
"bossHealthFactor": 1,
"threatBonus": 1,
"pacifyAllEnemies": false,
"tamingStartleRepercussion": "LoseSomeProgress",
"dayTimeDuration": 1800000000000,
"nightTimeDuration": 720000000000,
"curseModifier": "Normal"
},
"userGroups": [
{"name": "Admin", "password": "", "canKickBan": true, "canAccessInventories": true, "canEditWorld": true, "canEditBase": true, "canExtendBase": true, "reservedSlots": 0},
{"name": "Friend", "password": "", "canKickBan": false, "canAccessInventories": true, "canEditWorld": true, "canEditBase": true, "canExtendBase": false, "reservedSlots": 0},
{"name": "Guest", "password": "", "canKickBan": false, "canAccessInventories": false, "canEditWorld": true, "canEditBase": false, "canExtendBase": false, "reservedSlots": 0},
{"name": "Visitor", "password": "", "canKickBan": false, "canAccessInventories": false, "canEditWorld": false, "canEditBase": false, "canExtendBase": false, "reservedSlots": 0}
],
"bans": []
}`

func decodeNumbers(t *testing.T, src []byte) map[string]any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(src))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// defaultsOf builds a value from the "default" keywords of a schema node,
// recursing into object properties that have no explicit default.
func defaultsOf(node map[string]any) any {
	if d, ok := node["default"]; ok {
		return d
	}
	props, _ := node["properties"].(map[string]any)
	out := map[string]any{}
	for k, v := range props {
		out[k] = defaultsOf(v.(map[string]any))
	}
	return out
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sameKeys(t *testing.T, what string, got, want map[string]any) {
	t.Helper()
	if g, w := strings.Join(keys(got), ","), strings.Join(keys(want), ","); g != w {
		t.Errorf("%s keys differ:\n got  %s\n want %s", what, g, w)
	}
}

func enshrouded(t *testing.T) (*Game, map[string]any) {
	t.Helper()
	all, err := Load(games.FS)
	if err != nil {
		t.Fatal(err)
	}
	g := all["enshrouded"]
	if g == nil {
		t.Fatal("enshrouded not loaded")
	}
	schema := decodeNumbers(t, g.SettingsRaw)
	return g, defaultsOf(schema).(map[string]any)
}

func TestEnshroudedSchemaDefaultsAndTemplate(t *testing.T) {
	g, settings := enshrouded(t)

	// (a) defaults validate.
	if err := g.SettingsSchema.Validate(settings); err != nil {
		t.Fatalf("defaults do not validate: %v", err)
	}

	// (b) template renders valid JSON.
	var buf bytes.Buffer
	ctx := struct {
		Settings map[string]any
		Ports    map[string]int
		Paths    map[string]string
	}{settings, map[string]int{"query": 15637}, map[string]string{}}
	if err := g.ConfigTemplate.Execute(&buf, ctx); err != nil {
		t.Fatal(err)
	}
	rendered := decodeNumbers(t, buf.Bytes())
	official := decodeNumbers(t, []byte(officialDefault))

	// (c) key sets match the official default file exactly.
	sameKeys(t, "top-level", rendered, official)
	sameKeys(t, "gameSettings", rendered["gameSettings"].(map[string]any), official["gameSettings"].(map[string]any))
	rg, og := rendered["userGroups"].([]any), official["userGroups"].([]any)
	if len(rg) != len(og) {
		t.Fatalf("userGroups: got %d groups, want %d", len(rg), len(og))
	}
	for i := range og {
		sameKeys(t, fmt.Sprintf("userGroups[%d]", i), rg[i].(map[string]any), og[i].(map[string]any))
	}
	if _, ok := rendered["password"]; ok {
		t.Error("deprecated top-level password must not be rendered")
	}

	// Rendered values equal the official defaults (except blank passwords, which match too).
	var rv, ov any
	if err := json.Unmarshal(buf.Bytes(), &rv); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(officialDefault), &ov); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rv, ov) {
		t.Errorf("rendered default differs from official default:\n got  %s\n want %s", buf.String(), officialDefault)
	}

	// (d) large integers stay exact.
	if !strings.Contains(buf.String(), `"fromHungerToStarving": 600000000000,`) {
		t.Errorf("fromHungerToStarving not rendered as 600000000000:\n%s", buf.String())
	}
}

func TestEnshroudedSchemaRejectsInvalid(t *testing.T) {
	g, _ := enshrouded(t)
	cases := map[string]func(m map[string]any){
		"bad enum":      func(m map[string]any) { m["gameSettings"].(map[string]any)["tombstoneMode"] = "xAddBackpackMaterials" },
		"slotCount 17":  func(m map[string]any) { m["slotCount"] = json.Number("17") },
		"unknown key":   func(m map[string]any) { m["queryPort"] = json.Number("1234") },
		"factor range":  func(m map[string]any) { m["gameSettings"].(map[string]any)["playerHealthFactor"] = json.Number("4.5") },
		"no user group": func(m map[string]any) { m["userGroups"] = []any{} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			_, settings := enshrouded(t) // fresh copy
			mutate(settings)
			if err := g.SettingsSchema.Validate(settings); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
