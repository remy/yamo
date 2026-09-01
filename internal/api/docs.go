package api

import (
	_ "embed"
	"net/http"

	apispec "github.com/remy/yamo/api"
)

//go:embed docs.html
var docsHTML []byte

// The icon the docs page and the bare server wear. It is embedded for the same
// reason the schema and the docs page are: a NAS may have no outbound access,
// and one static binary is the whole deployment story here.
//
//go:embed favicon.png
var faviconPNG []byte

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

// serveFavicon serves the icon, at both the conventional .ico path and its
// real one. A PNG under .ico is what every browser since the mid-2000s
// expects, and shipping an actual ICO to satisfy the extension would mean a
// second copy of the same picture in the binary.
//
// It is cached for a day: it is the one asset a browser asks for on every page
// load, and it cannot change without a new binary.
func (s *Server) serveFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(faviconPNG)
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
