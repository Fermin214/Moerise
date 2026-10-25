package moerise

import (
	_ "embed"
	"encoding/json"
)

//go:embed openapi.json
var openAPIJSON []byte

//go:embed docs.html
var docsHTML []byte

func openAPISpec() json.RawMessage { return json.RawMessage(openAPIJSON) }
