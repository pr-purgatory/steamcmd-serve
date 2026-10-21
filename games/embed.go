// Package games embeds the built-in game definitions and the manifest schema.
package games

import "embed"

// FS holds manifest.schema.json and one directory per built-in game.
//
//go:embed manifest.schema.json */manifest.yaml */*.json */*.tmpl
var FS embed.FS
