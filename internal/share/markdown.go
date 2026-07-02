package share

import (
	"bytes"
	"html/template"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

var mdConverter = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		extension.Typographer,
	),
	goldmark.WithRendererOptions(
		html.WithUnsafe(),
	),
)

var mdTemplate = template.Must(template.New("markdown").Parse(mdTemplateHTML))

const mdTemplateHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
*,*::before,*::after{box-sizing:border-box}
:root{
  --fg:#1a1a2e;--fg2:#4a4a68;--bg:#fff;--bg2:#f6f6f9;
  --border:#e2e2ea;--accent:#5046e5;--accent-bg:#eef;
  --code-bg:#f4f4f8;--code-border:#dddde4;
  --font-sans:system-ui,-apple-system,"Segoe UI",Roboto,sans-serif;
  --font-mono:"SF Mono",Menlo,Consolas,"Liberation Mono",monospace;
}
@media(prefers-color-scheme:dark){
  :root{
    --fg:#e4e4ed;--fg2:#9e9eb8;--bg:#151520;--bg2:#1c1c2e;
    --border:#2e2e44;--accent:#818cf8;--accent-bg:rgba(129,140,248,.12);
    --code-bg:#1c1c2e;--code-border:#2e2e44;
  }
}
html{font-size:16px;-webkit-text-size-adjust:100%}
body{
  margin:0;padding:0;
  font-family:var(--font-sans);
  color:var(--fg);background:var(--bg);
  line-height:1.7;
}
.container{
  max-width:740px;margin:0 auto;
  padding:2.5rem 1.5rem 4rem;
}
header{margin-bottom:2rem;padding-bottom:1.5rem;border-bottom:1px solid var(--border)}
header h1{margin:0 0 .25rem;font-size:1.75rem;font-weight:700;line-height:1.3;letter-spacing:-.02em}
header .meta{font-size:.85rem;color:var(--fg2);margin-top:.35rem}
header .meta span+span::before{content:"·";margin:0 .5em;opacity:.5}
header .meta a{color:var(--accent);text-decoration:none}
header .meta a:hover{text-decoration:underline}

.summary{
  margin-bottom:1.5rem;padding:1rem 1.25rem;
  background:var(--accent-bg);border-left:3px solid var(--accent);
  border-radius:0 8px 8px 0;font-size:.9rem;line-height:1.6;
}
.summary-label{
  display:flex;align-items:center;gap:.4em;
  font-size:.75rem;font-weight:600;text-transform:uppercase;
  letter-spacing:.05em;color:var(--accent);margin-bottom:.5rem;
}
.summary-label svg{width:14px;height:14px}
.summary p{margin:.25rem 0}

.tags{display:flex;flex-wrap:wrap;gap:.4rem;margin-bottom:1.5rem}
.tags span{
  font-size:.75rem;font-weight:500;
  padding:.2rem .6rem;border-radius:999px;
  background:var(--bg2);border:1px solid var(--border);color:var(--fg2);
}

article h1{font-size:1.6rem;margin:2rem 0 .75rem;font-weight:700;letter-spacing:-.01em}
article h2{font-size:1.3rem;margin:1.75rem 0 .5rem;font-weight:650}
article h3{font-size:1.1rem;margin:1.5rem 0 .5rem;font-weight:600}
article h4,article h5,article h6{font-size:1rem;margin:1.25rem 0 .5rem;font-weight:600}
article p{margin:.75rem 0}
article a{color:var(--accent);text-decoration:underline;text-underline-offset:2px}
article a:hover{text-decoration-thickness:2px}

article img{max-width:100%;height:auto;border-radius:8px;margin:.75rem 0}

article blockquote{
  margin:1rem 0;padding:.75rem 1.25rem;
  border-left:3px solid var(--accent);background:var(--accent-bg);
  border-radius:0 6px 6px 0;
}
article blockquote p{margin:.25rem 0}

article code{
  font-family:var(--font-mono);font-size:.875em;
  background:var(--code-bg);border:1px solid var(--code-border);
  padding:.15em .35em;border-radius:4px;
}
article pre{
  margin:1rem 0;padding:1rem 1.25rem;
  background:var(--code-bg);border:1px solid var(--code-border);
  border-radius:8px;overflow-x:auto;
  line-height:1.5;
}
article pre code{
  background:none;border:none;padding:0;
  font-size:.85rem;
}

article ul,article ol{margin:.75rem 0;padding-left:1.75rem}
article li{margin:.25rem 0}
article li::marker{color:var(--fg2)}
article ul.contains-task-list{list-style:none;padding-left:.5rem}
article input[type="checkbox"]{margin-right:.5em}

article table{
  width:100%;border-collapse:collapse;margin:1rem 0;
  font-size:.9rem;
}
article th,article td{
  padding:.5rem .75rem;
  border:1px solid var(--border);
  text-align:left;
}
article th{background:var(--bg2);font-weight:600}
article tr:nth-child(even){background:var(--bg2)}

article hr{border:none;border-top:1px solid var(--border);margin:2rem 0}

article .footnotes{font-size:.85rem;color:var(--fg2);margin-top:2rem;padding-top:1rem;border-top:1px solid var(--border)}
</style>
</head>
<body>
<div class="container">
  <header>
    <h1>{{.Title}}</h1>
    {{- if .HasMeta}}
    <div class="meta">
      {{- if .Author}}<span>{{.Author}}</span>{{end}}
      {{- if .SourceURL}}<span><a href="{{.SourceURL}}" target="_blank" rel="noopener">Source</a></span>{{end}}
      {{- if .ExpiresAt}}<span>Expires {{.ExpiresAt}}</span>{{end}}
    </div>
    {{- end}}
  </header>
  {{- if .Summary}}
  <div class="summary">
    <div class="summary-label"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m12 3-1.9 5.8a2 2 0 0 1-1.3 1.3L3 12l5.8 1.9a2 2 0 0 1 1.3 1.3L12 21l1.9-5.8a2 2 0 0 1 1.3-1.3L21 12l-5.8-1.9a2 2 0 0 1-1.3-1.3Z"/></svg>AI Summary</div>
    {{.RenderedSummary}}
  </div>
  {{- end}}
  {{- if .Tags}}
  <div class="tags">{{range .Tags}}<span>{{.}}</span>{{end}}</div>
  {{- end}}
  <article>{{.Body}}</article>
</div>
</body>
</html>`

type mdTemplateData struct {
	Title           string
	Author          string
	SourceURL       string
	ExpiresAt       string
	Summary         string
	RenderedSummary template.HTML
	Tags            []string
	Body            template.HTML
}

func (d mdTemplateData) HasMeta() bool {
	return d.Author != "" || d.SourceURL != "" || d.ExpiresAt != ""
}

type RenderMarkdownOpts struct {
	Title     string
	Author    string
	SourceURL string
	ExpiresAt string
	Summary   string
	Tags      []string
}

func RenderMarkdownPage(opts RenderMarkdownOpts, markdown []byte) ([]byte, error) {
	var body bytes.Buffer
	if err := mdConverter.Convert(markdown, &body); err != nil {
		return nil, err
	}
	var renderedSummary template.HTML
	if opts.Summary != "" {
		var sb bytes.Buffer
		if err := mdConverter.Convert([]byte(opts.Summary), &sb); err == nil {
			renderedSummary = template.HTML(sb.String())
		}
	}
	data := mdTemplateData{Title: opts.Title, Author: opts.Author, SourceURL: opts.SourceURL, ExpiresAt: opts.ExpiresAt, Summary: opts.Summary, RenderedSummary: renderedSummary, Tags: opts.Tags, Body: template.HTML(body.String())}
	var page bytes.Buffer
	if err := mdTemplate.Execute(&page, data); err != nil {
		return nil, err
	}
	return page.Bytes(), nil
}
