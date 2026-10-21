package catalog

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"
)

// Settings is an instance's settings document. Numbers are json.Number so that large
// integers (e.g. Enshrouded's nanosecond durations) survive round trips exactly.
type Settings = map[string]any

// TemplateContext is the data available to launch args and config file templates.
type TemplateContext struct {
	Settings Settings
	Ports    map[string]int
	Paths    Paths
}

// Paths are container-side paths.
type Paths struct {
	Server string
	Data   string
}

// DecodeSettings parses a settings document, keeping numbers exact.
func DecodeSettings(raw []byte) (Settings, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var s Settings
	if err := d.Decode(&s); err != nil {
		return nil, err
	}
	if s == nil {
		s = Settings{}
	}
	return s, nil
}

// CompleteSettings fills missing values from schema defaults, generates values for empty
// `x-generate` fields, and validates the result. in is not modified.
func (g *Game) CompleteSettings(in Settings) (Settings, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	out, err := DecodeSettings(raw)
	if err != nil {
		return nil, err
	}
	schema, err := DecodeSettings(g.SettingsRaw)
	if err != nil {
		return nil, err
	}
	completed, err := complete(schema, out)
	if err != nil {
		return nil, err
	}
	s, _ := completed.(Settings)
	if err := g.SettingsSchema.Validate(s); err != nil {
		return nil, &SettingsError{Err: err}
	}
	return s, nil
}

// SettingsError reports settings that do not match the game's schema.
type SettingsError struct{ Err error }

func (e *SettingsError) Error() string { return "invalid settings: " + e.Err.Error() }
func (e *SettingsError) Unwrap() error { return e.Err }

func complete(schema map[string]any, v any) (any, error) {
	if v == nil {
		if d, ok := schema["default"]; ok {
			v = deepCopy(d)
		}
	}
	if gen, _ := schema["x-generate"].(string); gen != "" {
		if s, isStr := v.(string); v == nil || (isStr && s == "") {
			val, err := generate(gen)
			if err != nil {
				return nil, err
			}
			v = val
		}
	}
	switch t := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for name, p := range props {
			ps, _ := p.(map[string]any)
			c, err := complete(ps, t[name])
			if err != nil {
				return nil, err
			}
			if c != nil {
				t[name] = c
			}
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		for i := range t {
			c, err := complete(items, t[i])
			if err != nil {
				return nil, err
			}
			t[i] = c
		}
	}
	return v, nil
}

func generate(kind string) (string, error) {
	switch kind {
	case "password":
		return rand.Text()[:20], nil
	default:
		return "", fmt.Errorf("unknown x-generate %q", kind)
	}
}

func deepCopy(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = deepCopy(x)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, x := range t {
			s[i] = deepCopy(x)
		}
		return s
	default:
		return v
	}
}

// LaunchCommand renders the manifest's launch command.
func (g *Game) LaunchCommand(ctx TemplateContext) ([]string, error) {
	return renderAll(g.LaunchArgs, ctx)
}

func renderAll(ts []*template.Template, ctx TemplateContext) ([]string, error) {
	out := make([]string, len(ts))
	for i, t := range ts {
		var b strings.Builder
		if err := t.Execute(&b, ctx); err != nil {
			return nil, fmt.Errorf("launch.command[%d]: %w", i, err)
		}
		out[i] = b.String()
	}
	return out, nil
}
