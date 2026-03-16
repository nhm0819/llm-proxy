// Package docs serves the OpenAPI spec and Swagger UI.
//
// Routes:
//
//	GET /docs/          Swagger UI (HTML, loads from CDN)
//	GET /docs/openapi.yaml  Raw OpenAPI 3.0 spec
package docs

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var specBytes []byte

// Register mounts the docs routes on mux.
//
//	/docs/             → Swagger UI
//	/docs/openapi.yaml → raw spec
func Register(mux *http.ServeMux) {
	mux.HandleFunc("/docs/openapi.yaml", serveSpec)
	mux.HandleFunc("/docs/", serveUI)
}

func serveSpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(specBytes)
}

func serveUI(w http.ResponseWriter, r *http.Request) {
	// Redirect bare /docs to /docs/
	if r.URL.Path == "/docs" {
		http.Redirect(w, r, "/docs/", http.StatusMovedPermanently)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(swaggerUIHTML))
}

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="ko">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>LLM Proxy API Docs</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css" />
  <style>
    body { margin: 0; }
    #swagger-ui .topbar { display: none; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
  <script>
    window.onload = () => {
      SwaggerUIBundle({
        url: "/docs/openapi.yaml",
        dom_id: "#swagger-ui",
        presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset],
        layout: "BaseLayout",
        deepLinking: true,
        displayRequestDuration: true,
        persistAuthorization: true,
      });
    };
  </script>
</body>
</html>`
