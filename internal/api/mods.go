package api

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"time"

	"steamcmd-serve/internal/mods"
	"steamcmd-serve/internal/store"
)

// maxModUpload bounds multipart mod uploads (archive plus small form fields).
const maxModUpload = 1<<30 + 1<<20

type modBody struct {
	ID          string    `json:"id"`
	Provider    string    `json:"provider"`
	Ref         mods.Ref  `json:"ref"`
	SHA256      string    `json:"sha256"`
	Explicit    bool      `json:"explicit"`
	RequiredBy  []string  `json:"requiredBy"`
	InstalledAt time.Time `json:"installedAt"`
}

func toMod(m *store.Mod) modBody {
	req := m.RequiredBy
	if req == nil {
		req = []string{}
	}
	return modBody{ID: m.ID, Provider: m.Provider, Ref: mods.Ref{ID: m.PackageID, Version: m.Version},
		SHA256: m.SHA256, Explicit: m.Explicit, RequiredBy: req, InstalledAt: m.InstalledAt}
}

func (h *Handler) searchMods(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	provider := q.Get("provider")
	if provider != "thunderstore" && provider != "nexus" {
		badRequest(w, "provider must be thunderstore or nexus")
		return
	}
	if len(q.Get("q")) > 100 {
		badRequest(w, "q is limited to 100 characters")
		return
	}
	pkgs, err := h.svc.SearchMods(r.Context(), r.PathValue("gameId"), provider, q.Get("q"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if pkgs == nil {
		pkgs = []mods.Package{}
	}
	for i := range pkgs {
		if pkgs[i].Dependencies == nil {
			pkgs[i].Dependencies = []mods.Ref{}
		}
	}
	writeJSON(w, http.StatusOK, pkgs)
}

func (h *Handler) listInstanceMods(w http.ResponseWriter, r *http.Request) {
	list, err := h.svc.ListMods(r.Context(), r.PathValue("instanceId"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]modBody, 0, len(list))
	for _, m := range list {
		out = append(out, toMod(m))
	}
	writeJSON(w, http.StatusOK, out)
}

type modInstall struct {
	Provider string   `json:"provider"`
	Ref      mods.Ref `json:"ref"`
}

// installMod accepts JSON {provider, ref} (download from the provider) or multipart
// form fields provider, ref (JSON) and file (a manually downloaded .zip, sent last).
func (h *Handler) installMod(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("instanceId")
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mt != "multipart/form-data" {
		var req modInstall
		if !decodeBody(w, r, &req, true) {
			return
		}
		if req.Provider == "" || req.Ref.ID == "" || req.Ref.Version == "" {
			badRequest(w, "provider, ref.id and ref.version are required")
			return
		}
		h.job(w, r)(h.svc.InstallMod(r.Context(), id, req.Provider, req.Ref))
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxModUpload)
	reader, err := r.MultipartReader()
	if err != nil {
		badRequest(w, "invalid multipart body")
		return
	}
	var req modInstall
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			badRequest(w, "multipart body needs provider, ref and file (file last)")
			return
		}
		if err != nil {
			badRequest(w, "invalid multipart body")
			return
		}
		switch part.FormName() {
		case "provider":
			b, _ := io.ReadAll(io.LimitReader(part, 64))
			req.Provider = string(b)
		case "ref":
			if err := json.NewDecoder(io.LimitReader(part, 4096)).Decode(&req.Ref); err != nil {
				badRequest(w, "ref must be JSON {\"id\": string, \"version\": string}")
				return
			}
		case "file":
			if req.Provider == "" || req.Ref.ID == "" || req.Ref.Version == "" {
				badRequest(w, "send provider and ref before file")
				return
			}
			h.job(w, r)(h.svc.InstallModUpload(r.Context(), id, req.Provider, req.Ref, part))
			return
		}
	}
}

func (h *Handler) uninstallMod(w http.ResponseWriter, r *http.Request) {
	h.job(w, r)(h.svc.UninstallMod(r.Context(), r.PathValue("instanceId"), r.PathValue("modId")))
}
