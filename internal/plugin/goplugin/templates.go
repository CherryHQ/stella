package goplugin

import "text/template"

// pluginModule holds metadata for a single Go plugin module.
type pluginModule struct {
	// Module is the Go module path (e.g., "example.com/my-plugin").
	Module string
	// LocalPath is the absolute local directory path for the replace directive.
	LocalPath string
}

// templateData is the data passed to both templates.
type templateData struct {
	AnnaModule    string
	AnnaLocalPath string
	Plugins       []pluginModule
}

// pluginsGoTmpl generates a plugins.go that blank-imports every plugin
// (triggering their init() -> plugin.Register() calls). This file is placed
// alongside the copied cmd/anna source so it is part of the same package main.
var pluginsGoTmpl = template.Must(template.New("plugins.go").Parse(`package main

import (
{{- range .Plugins}}
	_ "{{.Module}}"
{{- end}}
)
`))

// goModTmpl generates a go.mod that requires anna and all plugin modules,
// with replace directives pointing each module to its local directory.
var goModTmpl = template.Must(template.New("go.mod").Parse(`module anna-custom

go 1.25

require (
	{{.AnnaModule}} v0.0.0
{{- range .Plugins}}
	{{.Module}} v0.0.0
{{- end}}
)

replace {{.AnnaModule}} => {{.AnnaLocalPath}}

{{- range .Plugins}}
replace {{.Module}} => {{.LocalPath}}
{{- end}}
`))
