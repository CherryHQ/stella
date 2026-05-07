package web

import (
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

type viteChunk struct {
	File           string   `json:"file"`
	CSS            []string `json:"css"`
	IsEntry        bool     `json:"isEntry"`
	DynamicImports []string `json:"dynamicImports"`
}

type viteManifest map[string]viteChunk

var (
	manifestOnce  sync.Once
	manifestCache viteManifest
	manifestErr   error
)

func loadManifest() (viteManifest, error) {
	manifestOnce.Do(func() {
		// Resolve path relative to this file at runtime.
		_, file, _, _ := runtime.Caller(0)
		dir := filepath.Dir(file)
		data, err := os.ReadFile(filepath.Join(dir, "static", "dist", ".vite", "manifest.json"))
		if err != nil {
			manifestErr = err
			return
		}
		manifestErr = json.Unmarshal(data, &manifestCache)
	})
	return manifestCache, manifestErr
}

// ViteDevTags returns script tags pointing at the Vite dev server for a given entry.
func ViteDevTags(entry string) template.HTML {
	return template.HTML(fmt.Sprintf(
		`<script type="module" src="http://localhost:5173/@vite/client"></script>`+"\n"+
			`<script type="module" src="http://localhost:5173/src/entries/%s.tsx"></script>`,
		template.HTMLEscapeString(entry),
	))
}

// ViteProdTags resolves the hashed asset URLs from the manifest and returns
// the appropriate <link> and <script> tags.
func ViteProdTags(entry string) template.HTML {
	manifest, err := loadManifest()
	if err != nil {
		return template.HTML(fmt.Sprintf("<!-- vite manifest error: %s -->", err))
	}
	key := "src/entries/" + entry + ".tsx"
	chunk, ok := manifest[key]
	if !ok {
		return template.HTML(fmt.Sprintf("<!-- vite entry not found: %s -->", template.HTMLEscapeString(key)))
	}
	var out strings.Builder
	for _, css := range chunk.CSS {
		fmt.Fprintf(&out, `<link rel="stylesheet" href="/static/dist/%s">`+"\n",
			template.HTMLEscapeString(css))
	}
	fmt.Fprintf(&out, `<script type="module" src="/static/dist/%s"></script>`+"\n",
		template.HTMLEscapeString(chunk.File))
	return template.HTML(out.String())
}

// ViteEntry returns dev or prod tags based on APP_ENV environment variable.
// Set APP_ENV=development to use the Vite dev server.
func ViteEntry(entry string) template.HTML {
	if os.Getenv("APP_ENV") == "development" {
		return ViteDevTags(entry)
	}
	return ViteProdTags(entry)
}
