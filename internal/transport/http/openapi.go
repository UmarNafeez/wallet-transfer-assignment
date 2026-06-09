package transporthttp

import (
	_ "embed"
	"net/http"
)

//go:embed openapi.yaml
var openapiSpec []byte

const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Wallet Transfer API</title>
  <link rel="stylesheet" href="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/4.18.3/swagger-ui.min.css" integrity="sha512-MTkaf6CARD9g4xsUTIY6FltlB0hzq3x2it0A8NUr9MTG0nAZ2E+lqO+vV7dD7zxdeDdJ5VM7U3ykVcjG3LvaWw==" crossorigin="anonymous" referrerpolicy="no-referrer" />
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/4.18.3/swagger-ui-bundle.min.js" integrity="sha512-qQbGsX9VOJ4wtw7uwShIDnAiI72bBNqxDMAqxG3lWfIeqc9GQ5fC1beznjYYOXWqyDp1KZd5GcQRXG5W+SB1nQ==" crossorigin="anonymous" referrerpolicy="no-referrer"></script>
  <script src="https://cdnjs.cloudflare.com/ajax/libs/swagger-ui/4.18.3/swagger-ui-standalone-preset.min.js" integrity="sha512-2vnnL9VJEmtuR+3vJh0iIPryhddXOfSI1u80HI+RGmEczoFSwpgl7Md8QwGYBSqssR2pDx2p4p8CekE5m9B5+Q==" crossorigin="anonymous" referrerpolicy="no-referrer"></script>
  <script>
    window.onload = function() {
      const ui = SwaggerUIBundle({
        url: '/openapi.yaml',
        dom_id: '#swagger-ui',
        presets: [SwaggerUIBundle.presets.apis, SwaggerUIStandalonePreset],
        layout: 'StandaloneLayout',
      });
      window.ui = ui;
    };
  </script>
</body>
</html>`

func (s *Server) handleOpenAPISpec(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	setCorrelationHeader(w, r.Context())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(openapiSpec)
}

func (s *Server) handleSwaggerUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	setCorrelationHeader(w, r.Context())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(swaggerUIHTML))
}
