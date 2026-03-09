package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
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
	"github.com/vaayne/anna/cron"
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

	// Cron jobs CRUD — direct file I/O, no scheduler needed.
	cronDir := cfg.Cron.DataDir
	if cronDir == "" {
		cronDir = filepath.Join(cfg.Workspace, "cron")
	}

	mux.HandleFunc("GET /api/cron/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := loadCronJobs(cronDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	})

	mux.HandleFunc("POST /api/cron/jobs", func(w http.ResponseWriter, r *http.Request) {
		var body cronJobJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
		if body.Name == "" || body.Message == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and message are required"})
			return
		}
		sched, err := parseCronSchedule(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		jobs, err := loadCronJobs(cronDir)
		if err != nil {
			jobs = nil
		}

		job := cron.Job{
			ID:          generateShortID(),
			Name:        body.Name,
			Schedule:    sched,
			Message:     body.Message,
			SessionMode: body.SessionMode,
			Enabled:     body.Enabled,
			CreatedAt:   time.Now(),
		}
		if job.SessionMode == "" {
			job.SessionMode = cron.SessionReuse
		}
		jobs = append(jobs, job)

		if err := saveCronJobs(cronDir, jobs); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, job)
	})

	mux.HandleFunc("PUT /api/cron/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body cronJobJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}

		jobs, err := loadCronJobs(cronDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		found := false
		for i, j := range jobs {
			if j.ID == id {
				if body.Name != "" {
					jobs[i].Name = body.Name
				}
				if body.Message != "" {
					jobs[i].Message = body.Message
				}
				if body.SessionMode != "" {
					jobs[i].SessionMode = body.SessionMode
				}
				jobs[i].Enabled = body.Enabled
				if sched, err := parseCronSchedule(body); err == nil {
					jobs[i].Schedule = sched
				}
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			return
		}

		if err := saveCronJobs(cronDir, jobs); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("DELETE /api/cron/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		jobs, err := loadCronJobs(cronDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		filtered := make([]cron.Job, 0, len(jobs))
		found := false
		for _, j := range jobs {
			if j.ID == id {
				found = true
				continue
			}
			filtered = append(filtered, j)
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
			return
		}

		if err := saveCronJobs(cronDir, filtered); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

	// Merge providers: update api_key/base_url but preserve existing Models.
	existing := cfg.Providers
	cfg.Providers = make(map[string]ProviderConfig, len(body.Providers))
	for name, p := range body.Providers {
		pc := ProviderConfig{APIKey: p.APIKey, BaseURL: p.BaseURL}
		if prev, ok := existing[name]; ok {
			pc.Models = prev.Models
		}
		cfg.Providers[name] = pc
	}

	cfg.Channels.Telegram = TelegramConfig{
		Token:      body.Channels.Telegram.Token,
		NotifyChat: body.Channels.Telegram.NotifyChat,
		ChannelID:  body.Channels.Telegram.ChannelID,
		GroupMode:  body.Channels.Telegram.GroupMode,
		AllowedIDs: body.Channels.Telegram.AllowedIDs,
	}
}

// saveConfig reads the existing config.yaml, merges onboarding fields on top
// (preserving runner, cron, and other sections), and writes it back.
func saveConfig(cfg *Config) error {
	path := configPath()

	// Read existing file as a raw map to preserve unknown sections.
	existing := make(map[string]any)
	data, err := os.ReadFile(path)
	if err == nil {
		_ = yaml.Unmarshal(data, &existing)
	}

	// Overlay onboarding-managed fields.
	setIfNonEmpty(existing, "provider", cfg.Provider)
	setIfNonEmpty(existing, "model", cfg.Model)
	setOrDelete(existing, "model_strong", cfg.ModelStrong)
	setOrDelete(existing, "model_fast", cfg.ModelFast)
	setOrDelete(existing, "workspace", cfg.Workspace)

	// Merge providers: update api_key/base_url, preserve everything else
	// (models, headers, etc.) in each provider entry.
	if len(cfg.Providers) > 0 {
		existingProvs, _ := existing["providers"].(map[string]any)
		if existingProvs == nil {
			existingProvs = make(map[string]any)
		}

		// Remove providers no longer in the onboarding set.
		for name := range existingProvs {
			if _, ok := cfg.Providers[name]; !ok {
				delete(existingProvs, name)
			}
		}

		for name, p := range cfg.Providers {
			pm, _ := existingProvs[name].(map[string]any)
			if pm == nil {
				pm = make(map[string]any)
			}
			setOrDelete(pm, "api_key", p.APIKey)
			setOrDelete(pm, "base_url", p.BaseURL)
			existingProvs[name] = pm
		}
		existing["providers"] = existingProvs
	} else {
		delete(existing, "providers")
	}

	// Merge telegram channel config.
	tg := cfg.Channels.Telegram
	if tg.Token != "" || tg.NotifyChat != "" || tg.ChannelID != "" || tg.GroupMode != "" || len(tg.AllowedIDs) > 0 {
		existingChannels, _ := existing["channels"].(map[string]any)
		if existingChannels == nil {
			existingChannels = make(map[string]any)
		}
		tgMap, _ := existingChannels["telegram"].(map[string]any)
		if tgMap == nil {
			tgMap = make(map[string]any)
		}
		setOrDelete(tgMap, "token", tg.Token)
		setOrDelete(tgMap, "notify_chat", tg.NotifyChat)
		setOrDelete(tgMap, "channel_id", tg.ChannelID)
		setOrDelete(tgMap, "group_mode", tg.GroupMode)
		if len(tg.AllowedIDs) > 0 {
			tgMap["allowed_ids"] = tg.AllowedIDs
		} else {
			delete(tgMap, "allowed_ids")
		}
		existingChannels["telegram"] = tgMap
		existing["channels"] = existingChannels
	}

	return writeYAMLFile(path, existing)
}

// setIfNonEmpty sets key in m if value is non-empty, otherwise leaves it as-is.
func setIfNonEmpty(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	}
}

// setOrDelete sets key in m if value is non-empty, otherwise deletes it.
func setOrDelete(m map[string]any, key, value string) {
	if value != "" {
		m[key] = value
	} else {
		delete(m, key)
	}
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

// cronJobJSON is the JSON shape for cron job create/update.
type cronJobJSON struct {
	Name        string `json:"name"`
	Message     string `json:"message"`
	Cron        string `json:"cron"`
	Every       string `json:"every"`
	SessionMode string `json:"session_mode"`
	Enabled     bool   `json:"enabled"`
}

func parseCronSchedule(body cronJobJSON) (cron.Schedule, error) {
	sched := cron.Schedule{Cron: body.Cron, Every: body.Every}
	count := 0
	if sched.Cron != "" {
		count++
	}
	if sched.Every != "" {
		count++
	}
	if count == 0 {
		return sched, fmt.Errorf("schedule requires cron or every")
	}
	if count > 1 {
		return sched, fmt.Errorf("schedule must have exactly one of cron or every")
	}
	if sched.Every != "" {
		if _, err := time.ParseDuration(sched.Every); err != nil {
			return sched, fmt.Errorf("invalid duration %q: %w", sched.Every, err)
		}
	}
	return sched, nil
}

func loadCronJobs(dataDir string) ([]cron.Job, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "jobs.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []cron.Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("parse jobs.json: %w", err)
	}
	return jobs, nil
}

func saveCronJobs(dataDir string, jobs []cron.Job) error {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dataDir, "jobs.json.tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dataDir, "jobs.json"))
}

func generateShortID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
