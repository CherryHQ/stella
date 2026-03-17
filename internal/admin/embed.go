package admin

import (
	"bytes"
	"embed"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
)

//go:embed ui
var adminUI embed.FS

var (
	assembledUI  []byte
	assembleOnce sync.Once
	includeRE    = regexp.MustCompile(`\{@include\s+([^}]+)\}`)
)

// assembleHTML reads ui/index.html and replaces {@include path} markers
// with the contents of the referenced files from the embedded filesystem.
func assembleHTML() []byte {
	shell, err := adminUI.ReadFile("ui/index.html")
	if err != nil {
		return []byte("admin UI not found")
	}

	result := includeRE.ReplaceAllFunc(shell, func(match []byte) []byte {
		sub := includeRE.FindSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		path := "ui/" + strings.TrimSpace(string(sub[1]))
		data, err := adminUI.ReadFile(path)
		if err != nil {
			return []byte(fmt.Sprintf("/* %s not found */", path))
		}
		return bytes.TrimSpace(data)
	})

	return result
}

func (s *Server) serveUI(w http.ResponseWriter, r *http.Request) {
	assembleOnce.Do(func() {
		assembledUI = assembleHTML()
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(assembledUI)
}
