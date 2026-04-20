package admin

import (
	"net/http"

	builtinres "github.com/vaayne/anna/plugins/tools/builtin"
)

// builtinResourceSummary is the list-row shape. Content is omitted to keep
// catalog payloads small; fetch a specific resource for the full body.
type builtinResourceSummary struct {
	Kind        string         `json:"kind"`
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	Hash        string         `json:"hash,omitempty"`
}

// builtinResourceFull is the detail shape, including the markdown body.
type builtinResourceFull struct {
	builtinResourceSummary
	Content string `json:"content"`
}

func toSummary(r builtinres.Resource) builtinResourceSummary {
	return builtinResourceSummary{
		Kind:        string(r.Kind),
		ID:          r.ID,
		Name:        r.Name,
		Description: r.Description,
		Tags:        r.Tags,
		Metadata:    r.Metadata,
		Hash:        r.Hash,
	}
}

// parseBuiltinKind maps the URL-form kind string to a Kind. Returns false for
// unknown kinds.
func parseBuiltinKind(s string) (builtinres.Kind, bool) {
	for _, k := range builtinres.AllKinds() {
		if string(k) == s {
			return k, true
		}
	}
	return "", false
}

func (s *Server) listBuiltinResources(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseBuiltinKind(r.PathValue("kind"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown builtin kind")
		return
	}
	reg, err := builtinres.Default()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resources := reg.List(kind)
	out := make([]builtinResourceSummary, len(resources))
	for i, res := range resources {
		out[i] = toSummary(res)
	}
	writeData(w, http.StatusOK, out)
}

func (s *Server) getBuiltinResource(w http.ResponseWriter, r *http.Request) {
	kind, ok := parseBuiltinKind(r.PathValue("kind"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown builtin kind")
		return
	}
	id := r.PathValue("id")
	reg, err := builtinres.Default()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	res, ok := reg.Get(kind, id)
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	writeData(w, http.StatusOK, builtinResourceFull{
		builtinResourceSummary: toSummary(res),
		Content:                res.Content,
	})
}
