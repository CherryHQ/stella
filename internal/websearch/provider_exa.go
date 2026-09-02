package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const exaMCPURL = "https://mcp.exa.ai/mcp?tools=web_search_exa"

var exaProvider = provider{
	name:      "exa",
	available: envSet("EXA_API_KEY"),
	search:    searchExa,
}

// exaMCPProvider is the anonymous zero-config fallback. It steps aside when
// EXA_API_KEY is set so the same query is never retried anonymously.
var exaMCPProvider = provider{
	name:      "exa",
	available: func(get environment) bool { return !hasEnv(get, "EXA_API_KEY") },
	search: func(ctx context.Context, client *http.Client, _ environment, query string, limit int) ([]sourceResult, error) {
		return searchExaMCP(ctx, client, query, limit)
	},
}

func searchExa(ctx context.Context, client *http.Client, get environment, query string, limit int) ([]sourceResult, error) {
	var response map[string]any
	err := requestJSON(ctx, client, "exa", http.MethodPost, "https://api.exa.ai/search", http.Header{"x-api-key": []string{get("EXA_API_KEY")}}, map[string]any{
		"query": query, "numResults": limit, "contents": map[string]any{"highlights": map[string]any{}},
	}, &response)
	if err != nil {
		return nil, err
	}
	return rows(response["results"])
}

type exaMCPEnvelope struct {
	Result *struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func searchExaMCP(ctx context.Context, client *http.Client, query string, limit int) ([]sourceResult, error) {
	data, err := requestProvider(ctx, client, "exa", http.MethodPost, exaMCPURL, http.Header{
		"Accept": []string{"application/json, text/event-stream"},
	}, map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name": "web_search_exa",
			"arguments": map[string]any{
				"query": query, "numResults": limit,
			},
		},
	})
	if err != nil {
		return nil, err
	}

	envelope, err := parseExaMCPEnvelope(data)
	if err != nil {
		return nil, err
	}
	if envelope.Error != nil {
		return nil, fmt.Errorf("exa: MCP error %d", envelope.Error.Code)
	}
	for _, item := range envelope.Result.Content {
		if item.Type != "text" || strings.TrimSpace(item.Text) == "" {
			continue
		}
		if envelope.Result.IsError {
			return nil, errors.New("exa: MCP returned an error")
		}
		return parseExaMCPResults(item.Text)
	}
	return nil, errors.New("exa: MCP returned empty content")
}

func parseExaMCPEnvelope(data []byte) (exaMCPEnvelope, error) {
	candidates := make([][]byte, 0, 2)
	for line := range bytes.SplitSeq(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if payload, ok := bytes.CutPrefix(line, []byte("data:")); ok {
			candidates = append(candidates, bytes.TrimSpace(payload))
		}
	}
	candidates = append(candidates, bytes.TrimSpace(data))

	for _, candidate := range candidates {
		var envelope exaMCPEnvelope
		if len(candidate) == 0 || json.Unmarshal(candidate, &envelope) != nil {
			continue
		}
		if envelope.Result != nil || envelope.Error != nil {
			return envelope, nil
		}
	}
	return exaMCPEnvelope{}, errors.New("exa: MCP returned invalid JSON-RPC content")
}

func parseExaMCPResults(text string) ([]sourceResult, error) {
	var results []sourceResult
	for block := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n---") {
		var (
			result  sourceResult
			content []string
			capture bool
		)
		for line := range strings.SplitSeq(block, "\n") {
			switch {
			case result.Title == "" && strings.HasPrefix(line, "Title: "):
				result.Title = strings.TrimSpace(strings.TrimPrefix(line, "Title: "))
			case result.URL == "" && strings.HasPrefix(line, "URL: "):
				result.URL = strings.TrimSpace(strings.TrimPrefix(line, "URL: "))
			case strings.HasPrefix(line, "Text: "):
				capture = true
				content = append(content, strings.TrimSpace(strings.TrimPrefix(line, "Text: ")))
			case line == "Highlights:":
				capture = true
			case capture:
				content = append(content, line)
			}
		}
		if result.URL != "" {
			result.Snippet = strings.Join(strings.Fields(strings.Join(content, "\n")), " ")
			results = append(results, result)
		}
	}
	if len(results) == 0 {
		return nil, errors.New("exa: MCP response has no result list")
	}
	return results, nil
}
