// Package catalog loads and validates game definitions (games/<id>/manifest.yaml).
package catalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"text/template"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const manifestSchemaURL = "https://steamcmd-serve.dev/schemas/manifest.schema.json"

// Manifest mirrors games/manifest.schema.json.
type Manifest struct {
	ID    string `yaml:"id"`
	Name  string `yaml:"name"`
	Steam struct {
		ServerAppID int    `yaml:"serverAppId"`
		GameAppID   int    `yaml:"gameAppId"`
		Platform    string `yaml:"platform"`
		Branch      string `yaml:"branch"`
		Anonymous   *bool  `yaml:"anonymous"`
	} `yaml:"steam"`
	Runtime struct {
		Image string            `yaml:"image"`
		Env   map[string]string `yaml:"env"`
	} `yaml:"runtime"`
	Launch struct {
		Command            []string `yaml:"command"`
		Workdir            string   `yaml:"workdir"`
		StopSignal         string   `yaml:"stopSignal"`
		StopTimeoutSeconds int      `yaml:"stopTimeoutSeconds"`
	} `yaml:"launch"`
	Ports    []Port `yaml:"ports"`
	Settings struct {
		Schema string `yaml:"schema"`
		File   *struct {
			Path     string `yaml:"path"`
			Format   string `yaml:"format"`
			Template string `yaml:"template"`
		} `yaml:"file"`
	} `yaml:"settings"`
	Persist []struct {
		Name   string `yaml:"name"`
		Path   string `yaml:"path"`
		Backup bool   `yaml:"backup"`
	} `yaml:"persist"`
	Readiness struct {
		LogRegex       string `yaml:"logRegex"`
		TimeoutSeconds int    `yaml:"timeoutSeconds"`
	} `yaml:"readiness"`
	Resources struct {
		MemoryMB int `yaml:"memoryMB"`
		DiskGB   int `yaml:"diskGB"`
	} `yaml:"resources"`
	Mods struct {
		Loader    string `yaml:"loader"`
		Providers []struct {
			Type       string `yaml:"type"`
			Community  string `yaml:"community"`
			GameDomain string `yaml:"gameDomain"`
		} `yaml:"providers"`
	} `yaml:"mods"`
}

// Port is one network port a game server listens on.
type Port struct {
	Name        string `yaml:"name"`
	Default     int    `yaml:"default"`
	Protocol    string `yaml:"protocol"`
	Description string `yaml:"description"`
	Derive      *struct {
		From   string `yaml:"from"`
		Offset int    `yaml:"offset"`
	} `yaml:"derive"`
}

// Game is a validated manifest plus its compiled artifacts.
type Game struct {
	Manifest       Manifest
	SettingsSchema *jsonschema.Schema
	SettingsRaw    json.RawMessage // served to the UI for form generation
	ConfigTemplate *template.Template
	LaunchArgs     []*template.Template
	Ready          *regexp.Regexp
}

// TemplateFuncs are available to launch args and config templates.
var TemplateFuncs = template.FuncMap{
	"json": func(v any) (string, error) {
		b, err := json.Marshal(v)
		return string(b), err
	},
}

// Load reads every <dir>/manifest.yaml in fsys. fsys must contain manifest.schema.json at its root.
func Load(fsys fs.FS) (map[string]*Game, error) {
	schema, err := compileFromFS(fsys, "manifest.schema.json", manifestSchemaURL)
	if err != nil {
		return nil, fmt.Errorf("manifest schema: %w", err)
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, err
	}
	games := map[string]*Game{}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		g, err := loadGame(fsys, e.Name(), schema)
		if err != nil {
			errs = append(errs, fmt.Errorf("game %q: %w", e.Name(), err))
			continue
		}
		games[g.Manifest.ID] = g
	}
	return games, errors.Join(errs...)
}

// IDs returns sorted game ids.
func IDs(games map[string]*Game) []string {
	ids := make([]string, 0, len(games))
	for id := range games {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func loadGame(fsys fs.FS, dir string, schema *jsonschema.Schema) (*Game, error) {
	raw, err := fs.ReadFile(fsys, path.Join(dir, "manifest.yaml"))
	if err != nil {
		return nil, err
	}
	var doc any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if err := schema.Validate(normalize(doc)); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	g := &Game{}
	if err := yaml.Unmarshal(raw, &g.Manifest); err != nil {
		return nil, err
	}
	m := &g.Manifest
	if m.ID != dir {
		return nil, fmt.Errorf("id %q must match directory name", m.ID)
	}
	if err := checkPorts(m.Ports); err != nil {
		return nil, err
	}

	schemaPath := path.Join(dir, m.Settings.Schema)
	if g.SettingsRaw, err = fs.ReadFile(fsys, schemaPath); err != nil {
		return nil, fmt.Errorf("settings schema: %w", err)
	}
	if g.SettingsSchema, err = compileFromFS(fsys, schemaPath, "game:///"+schemaPath); err != nil {
		return nil, fmt.Errorf("settings schema: %w", err)
	}

	if f := m.Settings.File; f != nil && f.Template != "" {
		src, err := fs.ReadFile(fsys, path.Join(dir, f.Template))
		if err != nil {
			return nil, fmt.Errorf("config template: %w", err)
		}
		if g.ConfigTemplate, err = template.New(f.Template).Funcs(TemplateFuncs).Option("missingkey=error").Parse(string(src)); err != nil {
			return nil, fmt.Errorf("config template: %w", err)
		}
	}
	for i, arg := range m.Launch.Command {
		t, err := template.New(fmt.Sprintf("arg%d", i)).Funcs(TemplateFuncs).Option("missingkey=error").Parse(arg)
		if err != nil {
			return nil, fmt.Errorf("launch.command[%d]: %w", i, err)
		}
		g.LaunchArgs = append(g.LaunchArgs, t)
	}
	if g.Ready, err = regexp.Compile(m.Readiness.LogRegex); err != nil {
		return nil, fmt.Errorf("readiness.logRegex: %w", err)
	}
	return g, nil
}

func checkPorts(ports []Port) error {
	byName := map[string]Port{}
	for _, p := range ports {
		if _, dup := byName[p.Name]; dup {
			return fmt.Errorf("duplicate port name %q", p.Name)
		}
		byName[p.Name] = p
	}
	for _, p := range ports {
		if p.Derive == nil {
			continue
		}
		base, ok := byName[p.Derive.From]
		if !ok || base.Derive != nil {
			return fmt.Errorf("port %q: derive.from %q must name a non-derived port", p.Name, p.Derive.From)
		}
		if base.Default+p.Derive.Offset != p.Default {
			return fmt.Errorf("port %q: default %d != %s default %d %+d", p.Name, p.Default, base.Name, base.Default, p.Derive.Offset)
		}
	}
	return nil
}

func compileFromFS(fsys fs.FS, file, url string) (*jsonschema.Schema, error) {
	raw, err := fs.ReadFile(fsys, file)
	if err != nil {
		return nil, err
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(url, doc); err != nil {
		return nil, err
	}
	return c.Compile(url)
}

// normalize converts YAML-decoded values into the JSON data model the validator expects.
func normalize(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			t[k] = normalize(x)
		}
		return t
	case []any:
		for i, x := range t {
			t[i] = normalize(x)
		}
		return t
	case int:
		return json.Number(fmt.Sprint(t))
	case float64:
		return json.Number(fmt.Sprint(t))
	default:
		return v
	}
}
