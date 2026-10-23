// Copyright 2026 Midnight Authors. SPDX-License-Identifier: Apache-2.0.
package graphql

import (
	"net/http"
	"strings"
)

// PlaygroundHandler returns the GraphiQL playground HTML. It must be
// mounted under a dev-only path; production callers should leave the
// dev flag unset so the router never wires this route.
func PlaygroundHandler(endpoint string) http.Handler {
	html := strings.ReplaceAll(playgroundHTML, "{{ENDPOINT}}", endpoint)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(html))
	})
}

// playgroundHTML is the minimal GraphiQL bootstrap. It loads the
// CDN-hosted GraphiQL bundle so the binary has no JS asset embedded.
// The endpoint placeholder is rewritten at handler-construction time.
const playgroundHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8" />
<title>Midnight GraphiQL</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/graphiql@3/graphiql.min.css" />
<style>
body { margin: 0; height: 100vh; }
#graphiql { height: 100vh; }
</style>
</head>
<body>
<div id="graphiql">Loading Midnight GraphiQL...</div>
<script crossorigin src="https://unpkg.com/react@18/umd/react.production.min.js"></script>
<script crossorigin src="https://unpkg.com/react-dom@18/umd/react-dom.production.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/graphiql@3/graphiql.min.js"></script>
<script>
  const fetcher = GraphiQL.createFetcher({ url: "{{ENDPOINT}}" });
  const root = ReactDOM.createRoot(document.getElementById('graphiql'));
  root.render(
    React.createElement(GraphiQL, {
      fetcher,
      defaultEditorToolsVisibility: true,
      defaultQuery: "# Midnight GraphQL gateway (/graphql/v2)\n# Mirrors Railway's public schema shape.\n#\nquery PingMidnight {\n  serverInfo {\n    apiVersion\n    midnightVersion\n    graphqlSchemaSource\n  }\n}",
    })
  );
</script>
</body>
</html>
`
