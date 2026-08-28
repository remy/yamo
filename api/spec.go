// Package apispec holds the OpenAPI contract and embeds it into the binary.
//
// The schema lives here, at the top of the repository, because it is the
// published interface rather than an implementation detail — a client author
// should find it without reading the server. Embedding it means a deployed
// binary always serves the contract it actually implements, with no files to
// ship alongside it.
package apispec

import (
	_ "embed"
	"encoding/json"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed openapi.yaml
var yamlBytes []byte

// YAML returns the schema as authored.
func YAML() []byte { return yamlBytes }

var (
	once     sync.Once
	jsonData []byte
	jsonErr  error
)

// JSON returns the schema converted to JSON, which is what code generators and
// browsers prefer. The conversion happens once; the YAML stays the source of
// truth because that is the form a person edits.
func JSON() ([]byte, error) {
	once.Do(func() {
		var doc any
		if jsonErr = yaml.Unmarshal(yamlBytes, &doc); jsonErr != nil {
			return
		}
		jsonData, jsonErr = json.Marshal(doc)
	})
	return jsonData, jsonErr
}
