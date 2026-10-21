package catalog

// Secret handling for settings fields marked `x-secret: true` in a game's settings schema.
// Reads mask them as {"hasValue": bool}; writes may send that placeholder back to keep
// the stored value. Array items are matched by the array's `x-key` property (e.g. a
// user group's name) when declared, otherwise by index.

// MaskSecrets returns a copy of s with every secret replaced by {"hasValue": bool}.
func (g *Game) MaskSecrets(s Settings) Settings {
	out, _ := walkSecrets(g.settingsDoc, deepCopy(s), func(v any) any {
		str, _ := v.(string)
		return map[string]any{"hasValue": str != ""}
	}).(Settings)
	return out
}

// MapSecrets returns a copy of s with fn applied to every secret value (e.g. to
// encrypt or decrypt them for storage).
func (g *Game) MapSecrets(s Settings, fn func(any) any) Settings {
	out, _ := walkSecrets(g.settingsDoc, deepCopy(s), fn).(Settings)
	return out
}

// MergeSecrets returns in with every {"hasValue": ...} placeholder replaced by the
// corresponding value from stored. A placeholder without a stored counterpart becomes
// "" (and is then generated or rejected by CompleteSettings).
func (g *Game) MergeSecrets(in, stored Settings) Settings {
	out, _ := mergeSecrets(g.settingsDoc, deepCopy(in), stored).(Settings)
	return out
}

func isPlaceholder(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return false
	}
	_, ok = m["hasValue"]
	return ok
}

func walkSecrets(schema map[string]any, v any, fn func(any) any) any {
	if schema == nil || v == nil {
		return v
	}
	if secret, _ := schema["x-secret"].(bool); secret {
		return fn(v)
	}
	switch t := v.(type) {
	case map[string]any:
		props, _ := schema["properties"].(map[string]any)
		for name, p := range props {
			if x, ok := t[name]; ok {
				ps, _ := p.(map[string]any)
				t[name] = walkSecrets(ps, x, fn)
			}
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		for i := range t {
			t[i] = walkSecrets(items, t[i], fn)
		}
	}
	return v
}

func mergeSecrets(schema map[string]any, in, stored any) any {
	if schema == nil || in == nil {
		return in
	}
	if secret, _ := schema["x-secret"].(bool); secret {
		if !isPlaceholder(in) {
			return in
		}
		if s, ok := stored.(string); ok {
			return s
		}
		return ""
	}
	switch t := in.(type) {
	case map[string]any:
		sm, _ := stored.(map[string]any)
		props, _ := schema["properties"].(map[string]any)
		for name, p := range props {
			if x, ok := t[name]; ok {
				ps, _ := p.(map[string]any)
				t[name] = mergeSecrets(ps, x, sm[name])
			}
		}
	case []any:
		items, _ := schema["items"].(map[string]any)
		sa, _ := stored.([]any)
		key, _ := schema["x-key"].(string)
		for i := range t {
			t[i] = mergeSecrets(items, t[i], counterpart(t[i], sa, i, key))
		}
	}
	return in
}

// counterpart finds the stored array item matching item: by x-key value when the array
// declares one, else by index.
func counterpart(item any, stored []any, i int, key string) any {
	if key == "" {
		if i < len(stored) {
			return stored[i]
		}
		return nil
	}
	im, _ := item.(map[string]any)
	want, ok := im[key]
	if !ok {
		return nil
	}
	for _, s := range stored {
		if sm, _ := s.(map[string]any); sm != nil && sm[key] == want {
			return s
		}
	}
	return nil
}
