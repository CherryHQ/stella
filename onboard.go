package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/ai/stream"
	"gopkg.in/yaml.v3"
)

//go:embed onboard.html
var onboardFS embed.FS

func onboardCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "onboard",
		Usage: "Open a web page to set up anna for the first time",
		Flags: []ucli.Flag{
			&ucli.IntFlag{
				Name:  "port",
				Usage: "Port to listen on (0 = random)",
				Value: 0,
			},
		},
		Action: func(c *ucli.Context) error {
			return runOnboard(c.Context, c.Int("port"))
		},
	}
}

func runOnboard(ctx context.Context, port int) error {
	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	mux := http.NewServeMux()

	// Serve the single-page HTML.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		data, _ := onboardFS.ReadFile("onboard.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	// GET /api/config — return current config + cached models.
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		// Build per-provider model lists from cache.
		models := make(map[string][]string)
		if cache, err := LoadModelsCache(); err == nil {
			for _, m := range cache.Models {
				models[m.Provider] = append(models[m.Provider], m.Model)
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"config": cfgToJSON(cfg),
			"models": models,
		})
	})

	// POST /api/config — save config from JSON body.
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		var body configJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
		applyJSONToConfig(cfg, &body)

		if err := saveConfig(cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed: " + err.Error()})
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// POST /api/providers/{name}/models — fetch models from provider API.
	mux.HandleFunc("POST /api/providers/{name}/models", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("name")

		var body struct {
			APIKey  string `json:"api_key"`
			BaseURL string `json:"base_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON"})
			return
		}

		if body.APIKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "api_key is required"})
			return
		}

		provCfg := ProviderConfig{APIKey: body.APIKey, BaseURL: body.BaseURL}
		provider := newStreamProvider(name, provCfg)
		if provider == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown provider: " + name})
			return
		}

		lister, ok := provider.(stream.ModelLister)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": name + " does not support model listing"})
			return
		}

		fetchCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		listed, err := lister.ListModels(fetchCtx)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "fetch failed: " + err.Error()})
			return
		}

		modelIDs := make([]string, 0, len(listed))
		for _, m := range listed {
			modelIDs = append(modelIDs, m.ID)
		}

		// Update the global models cache with these results.
		mergeProviderModelsCache(name, modelIDs)

		writeJSON(w, http.StatusOK, map[string]any{"models": modelIDs})
	})

	// Listen on requested port (0 = random).
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() { _ = ln.Close() }()

	addr := ln.Addr().String()
	url := "http://" + addr
	fmt.Printf("Anna setup running at %s\n", url)

	openBrowser(url)

	srv := &http.Server{Handler: mux}

	// Shutdown on context cancellation.
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("serve: %w", err)
	}
	return nil
}

// configJSON is the JSON shape exchanged with the frontend.
type configJSON struct {
	Provider    string                  `json:"provider"`
	Model       string                  `json:"model"`
	ModelStrong string                  `json:"model_strong"`
	ModelFast   string                  `json:"model_fast"`
	Workspace   string                  `json:"workspace"`
	Providers   map[string]providerJSON `json:"providers"`
	Channels    channelsJSON            `json:"channels"`
}

type providerJSON struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
}

type channelsJSON struct {
	Telegram telegramJSON `json:"telegram"`
}

type telegramJSON struct {
	Token      string  `json:"token"`
	NotifyChat string  `json:"notify_chat"`
	ChannelID  string  `json:"channel_id"`
	GroupMode  string  `json:"group_mode"`
	AllowedIDs []int64 `json:"allowed_ids"`
}

func cfgToJSON(cfg *Config) configJSON {
	providers := make(map[string]providerJSON, len(cfg.Providers))
	for name, p := range cfg.Providers {
		providers[name] = providerJSON{APIKey: p.APIKey, BaseURL: p.BaseURL}
	}
	return configJSON{
		Provider:    cfg.Provider,
		Model:       cfg.Model,
		ModelStrong: cfg.ModelStrong,
		ModelFast:   cfg.ModelFast,
		Workspace:   cfg.Workspace,
		Providers:   providers,
		Channels: channelsJSON{
			Telegram: telegramJSON{
				Token:      cfg.Channels.Telegram.Token,
				NotifyChat: cfg.Channels.Telegram.NotifyChat,
				ChannelID:  cfg.Channels.Telegram.ChannelID,
				GroupMode:  cfg.Channels.Telegram.GroupMode,
				AllowedIDs: cfg.Channels.Telegram.AllowedIDs,
			},
		},
	}
}

func applyJSONToConfig(cfg *Config, body *configJSON) {
	cfg.Provider = body.Provider
	cfg.Model = body.Model
	cfg.ModelStrong = body.ModelStrong
	cfg.ModelFast = body.ModelFast
	cfg.Workspace = body.Workspace

	cfg.Providers = make(map[string]ProviderConfig, len(body.Providers))
	for name, p := range body.Providers {
		cfg.Providers[name] = ProviderConfig{APIKey: p.APIKey, BaseURL: p.BaseURL}
	}

	cfg.Channels.Telegram = TelegramConfig{
		Token:      body.Channels.Telegram.Token,
		NotifyChat: body.Channels.Telegram.NotifyChat,
		ChannelID:  body.Channels.Telegram.ChannelID,
		GroupMode:  body.Channels.Telegram.GroupMode,
		AllowedIDs: body.Channels.Telegram.AllowedIDs,
	}
}

// saveConfig writes the config to ~/.anna/config.yaml.
func saveConfig(cfg *Config) error {
	return saveConfigTo(configPath(), cfg)
}

// mergeProviderModelsCache updates the models cache for a single provider,
// preserving models from other providers.
func mergeProviderModelsCache(providerName string, modelIDs []string) {
	cache, err := LoadModelsCache()
	if err != nil {
		cache = &ModelsCache{}
	}

	// Keep models from other providers.
	var kept []CachedModel
	for _, m := range cache.Models {
		if m.Provider != providerName {
			kept = append(kept, m)
		}
	}

	// Add new models for this provider.
	for _, id := range modelIDs {
		kept = append(kept, CachedModel{Provider: providerName, Model: id})
	}

	cache.Models = kept
	cache.UpdatedAt = time.Now().UTC()

	if err := SaveModelsCache(cache); err != nil {
		slog.Warn("failed to save models cache", "error", err)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if cmd != nil {
		if err := cmd.Start(); err != nil {
			slog.Warn("failed to open browser", "error", err)
			return
		}
		go func() { _ = cmd.Wait() }()
	}
}

// saveConfigTo marshals cfg to YAML and writes it to path,
// stripping zero-value fields for a clean config file.
func saveConfigTo(path string, cfg *Config) error {
	// Build a clean map to avoid writing empty/default values.
	out := make(map[string]any)

	if cfg.Provider != "" && cfg.Provider != "anthropic" {
		out["provider"] = cfg.Provider
	}
	if cfg.Model != "" && cfg.Model != "claude-sonnet-4-6" {
		out["model"] = cfg.Model
	}
	if cfg.ModelStrong != "" {
		out["model_strong"] = cfg.ModelStrong
	}
	if cfg.ModelFast != "" {
		out["model_fast"] = cfg.ModelFast
	}
	if cfg.Workspace != "" {
		out["workspace"] = cfg.Workspace
	}

	if len(cfg.Providers) > 0 {
		provs := make(map[string]any, len(cfg.Providers))
		for name, p := range cfg.Providers {
			pm := make(map[string]any)
			if p.APIKey != "" {
				pm["api_key"] = p.APIKey
			}
			if p.BaseURL != "" {
				pm["base_url"] = p.BaseURL
			}
			if len(p.Models) > 0 {
				pm["models"] = p.Models
			}
			provs[name] = pm
		}
		out["providers"] = provs
	}

	tg := cfg.Channels.Telegram
	if hasTelegramConfig(tg) {
		tgMap := make(map[string]any)
		if tg.Token != "" {
			tgMap["token"] = tg.Token
		}
		if tg.NotifyChat != "" {
			tgMap["notify_chat"] = tg.NotifyChat
		}
		if tg.ChannelID != "" {
			tgMap["channel_id"] = tg.ChannelID
		}
		if tg.GroupMode != "" {
			tgMap["group_mode"] = tg.GroupMode
		}
		if len(tg.AllowedIDs) > 0 {
			tgMap["allowed_ids"] = tg.AllowedIDs
		}
		out["channels"] = map[string]any{"telegram": tgMap}
	}

	return writeYAMLFile(path, out)
}

func hasTelegramConfig(tg TelegramConfig) bool {
	return tg.Token != "" || tg.NotifyChat != "" || tg.ChannelID != "" ||
		tg.GroupMode != "" || len(tg.AllowedIDs) > 0
}

// writeYAMLFile writes a map as YAML to the given path.
func writeYAMLFile(path string, data map[string]any) error {
	out, err := yaml.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	return os.WriteFile(path, out, 0o644)
}
