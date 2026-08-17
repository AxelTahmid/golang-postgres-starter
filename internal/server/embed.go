package server

import "embed"

// OpenAPISpec contains the reviewed generated contract served by this build.
//
//go:embed openapi.json
var OpenAPISpec embed.FS
