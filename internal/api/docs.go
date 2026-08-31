package api

import (
	_ "embed"
	"net/http"

	apispec "github.com/remy/yamo/api"
)

//go:embed docs.html
var docsHTML []byte

func (s *Server) serveSpecYAML(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="openapi.yaml"`)
	_, _ = w.Write(apispec.YAML())
}

func (s *Server) serveSpecJSON(w http.ResponseWriter, r *http.Request) {
	b, err := apispec.JSON()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(b)
}

// serveDocs returns a browsable view of the schema.
//
// The page is written here rather than pulled from a documentation service,
// because a NAS may have no outbound access at all and documentation that only
// works online is not documentation.
func (s *Server) serveDocs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(docsHTML)
}
