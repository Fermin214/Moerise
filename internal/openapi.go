package moerise

import (
	_ "embed"
	"encoding/json"
)

var openAPIJSON = buildOpenAPISpec()

//go:embed docs.html
var docsHTML []byte

func openAPISpec() json.RawMessage { return json.RawMessage(openAPIJSON) }
