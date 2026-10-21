// Package mods installs third-party mods into game instances.
package mods

import (
	"context"
	"io"
)

// Provider is a mod source. Implementations: Thunderstore, NexusMods.
type Provider interface {
	// Name is the manifest provider type ("thunderstore", "nexus").
	Name() string
	// Search lists packages for a game. scope is the manifest's community or gameDomain.
	Search(ctx context.Context, scope, query string) ([]Package, error)
	// Resolve returns ref plus its full dependency closure, dependencies first.
	Resolve(ctx context.Context, scope string, ref Ref) ([]Ref, error)
	// Download writes the package archive for ref to w.
	// It returns ErrManualDownload when the user must supply the file (e.g. Nexus without Premium).
	Download(ctx context.Context, scope string, ref Ref, w io.Writer) (Artifact, error)
}

// Ref identifies one version of a package within a provider.
type Ref struct {
	ID      string `json:"id"` // Thunderstore "Namespace-Name", Nexus "<modId>:<fileId>"
	Version string `json:"version"`
}

// Package is a search result.
type Package struct {
	Ref          Ref      `json:"ref"`
	Name         string   `json:"name"`
	Author       string   `json:"author"`
	Summary      string   `json:"summary"`
	Dependencies []Ref    `json:"dependencies"`
	Categories   []string `json:"categories,omitempty"`
}

// Artifact describes a downloaded archive.
type Artifact struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

// ErrManualDownload means the provider cannot download for this user; the UI asks for an upload.
var ErrManualDownload = manualDownloadError{}

type manualDownloadError struct{}

func (manualDownloadError) Error() string { return "mods: provider requires manual download" }
