// Package api owns the HTTP contract shared by the server and generated web client.
package api

import _ "embed"

//go:embed openapi.json
var OpenAPI []byte
