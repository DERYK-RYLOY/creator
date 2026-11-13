// Command webserver is a simple HTTP server that serves a basic website.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func homeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Creator Website</title>
    <style>
        body {
            font-family: system-ui, sans-serif;
            max-width: 800px;
            margin: 0 auto;
            padding: 2rem;
            background: #0f172a;
            color: #e2e8f0;
        }
        h1 { color: #38bdf8; }
        code { background: #1e293b; padding: 0.2rem 0.4rem; border-radius: 4px; }
        pre { background: #1e293b; padding: 1rem; border-radius: 6px; overflow-x: auto; }
        .card { background: #1e293b; border: 1px solid #334155; border-radius: 8px; padding: 1.5rem; margin: 1rem 0; }
        .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(250px, 1fr)); gap: 1rem; }
        a { color: #38bdf8; text-decoration: none; }
        a:hover { text-decoration: underline; }
    </style>
</head>
<body>
    <h1>Creator Website</h1>
    <p>A simple website served by the Creator webserver.</p>
    <div class="grid">
        <div class="card">
            <h3>Creator</h3>
            <p>A terminal agent that never floods its own context.</p>
        </div>
        <div class="card">
            <h3>Commands</h3>
            <p><code>creator</code> - mission control</p>
            <p><code>creator run</code> - headless agent</p>
            <p><code>creator shell</code> - job runner</p>
            <p><code>creator mcp</code> - MCP server</p>
        </div>
    </div>
</body>
</html>`)
}

func main() {
	handler := http.NewServeMux()
	handler.HandleFunc("/", homeHandler)
	server := &http.Server{
		Addr:              "127.0.0.1:8080",
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	fmt.Println("Starting web server on http://localhost:8080")
	fmt.Println("Visit http://localhost:8080 in your browser")
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "webserver: %v\n", err)
		os.Exit(1)
	}
}
