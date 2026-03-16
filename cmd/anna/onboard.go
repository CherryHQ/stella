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
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/scheduler"
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
	// 1. Create ANNA_HOME.
	home := config.AnnaHome()
	if err := os.MkdirAll(home, 0o755); err != nil {
		return fmt.Errorf("create anna home: %w", err)
	}

	// 2. Open DB.
	dbPath := filepath.Join(home, "anna.db")
	db, err := appdb.OpenDB(dbPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	// 3. Create config Store.
	store := config.NewDBStore(db)

	// 4. Seed defaults.
	if err := store.SeedDefaults(ctx); err != nil {
		return fmt.Errorf("seed defaults: %w", err)
	}

	mux := http.NewServeMux()

	// Serve the single-page HTML.
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		data, _ := onboardFS.ReadFile("onboard.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	// GET /api/config — return current config from DB + cached models.
	mux.HandleFunc("GET /api/config", func(w http.ResponseWriter, r *http.Request) {
		cfgJSON, err := storeToConfigJSON(r.Context(), store)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		// Build per-provider model lists from cache.
		models := make(map[string][]string)
		if cache, err := LoadModelsCache(); err == nil {
			for _, m := range cache.Models {
				models[m.Provider] = append(models[m.Provider], m.Model)
			}
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"config": cfgJSON,
			"models": models,
		})
	})

	// POST /api/config — save config from JSON body to DB.
	mux.HandleFunc("POST /api/config", func(w http.ResponseWriter, r *http.Request) {
		var body onboardConfigJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}

		if err := applyConfigJSONToStore(r.Context(), store, &body); err != nil {
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

		provider := newStreamProviderFromCreds(name, body.APIKey, body.BaseURL)
		if provider == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown provider: " + name})
			return
		}

		lister, ok := provider.(ai.ModelLister)
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

		mergeProviderModelsCache(name, modelIDs)

		writeJSON(w, http.StatusOK, map[string]any{"models": modelIDs})
	})

	// Scheduler jobs CRUD — file-based for now.
	schedulerDir := filepath.Join(home, "workspaces", "anna", "scheduler")

	mux.HandleFunc("GET /api/scheduler/jobs", func(w http.ResponseWriter, r *http.Request) {
		jobs, err := loadSchedulerJobs(schedulerDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
	})

	mux.HandleFunc("POST /api/scheduler/jobs", func(w http.ResponseWriter, r *http.Request) {
		var body schedulerJobJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}
		if body.Name == "" || body.Message == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and message are required"})
			return
		}
		sched, err := parseSchedule(body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}

		jobs, err := loadSchedulerJobs(schedulerDir)
		if err != nil {
			jobs = nil
		}

		job := scheduler.Job{
			ID:          generateShortID(),
			Name:        body.Name,
			Schedule:    sched,
			Message:     body.Message,
			SessionMode: body.SessionMode,
			Enabled:     body.Enabled,
			CreatedAt:   time.Now(),
		}
		if job.SessionMode == "" {
			job.SessionMode = scheduler.SessionReuse
		}
		jobs = append(jobs, job)

		if err := saveSchedulerJobs(schedulerDir, jobs); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, job)
	})

	mux.HandleFunc("PUT /api/scheduler/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var body schedulerJobJSON
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON: " + err.Error()})
			return
		}

		jobs, err := loadSchedulerJobs(schedulerDir)
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
				if sched, err := parseSchedule(body); err == nil {
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

		if err := saveSchedulerJobs(schedulerDir, jobs); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save failed: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("DELETE /api/scheduler/jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		jobs, err := loadSchedulerJobs(schedulerDir)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		filtered := make([]scheduler.Job, 0, len(jobs))
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

		if err := saveSchedulerJobs(schedulerDir, filtered); err != nil {
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

// --- JSON types for the onboard API ---

// onboardConfigJSON is the JSON shape exchanged with the frontend.
type onboardConfigJSON struct {
	Provider    string                     `json:"provider"`
	Model       string                     `json:"model"`
	ModelStrong string                     `json:"model_strong"`
	ModelFast   string                     `json:"model_fast"`
	Workspace   string                     `json:"workspace"`
	Providers   map[string]onboardProvJSON `json:"providers"`
	Channels    onboardChannelsJSON        `json:"channels"`
}

type onboardProvJSON struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
}

type onboardChannelsJSON struct {
	Telegram json.RawMessage `json:"telegram"`
	QQ       json.RawMessage `json:"qq"`
	Feishu   json.RawMessage `json:"feishu"`
}

// storeToConfigJSON reads the current state from the Store and returns the
// onboard JSON representation.
func storeToConfigJSON(ctx context.Context, store config.Store) (onboardConfigJSON, error) {
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil {
		return onboardConfigJSON{}, err
	}

	var defaultAgent config.Agent
	for _, a := range agents {
		if a.ID == "anna" {
			defaultAgent = a
			break
		}
	}
	if defaultAgent.ID == "" && len(agents) > 0 {
		defaultAgent = agents[0]
	}

	providers, err := store.ListProviders(ctx)
	if err != nil {
		return onboardConfigJSON{}, err
	}

	provMap := make(map[string]onboardProvJSON, len(providers))
	for _, p := range providers {
		provMap[p.ID] = onboardProvJSON{APIKey: p.APIKey, BaseURL: p.BaseURL}
	}

	out := onboardConfigJSON{
		Provider:    defaultAgent.ProviderID,
		Model:       defaultAgent.Model,
		ModelStrong: defaultAgent.ModelStrong,
		ModelFast:   defaultAgent.ModelFast,
		Workspace:   defaultAgent.Workspace,
		Providers:   provMap,
	}

	// Load channel configs.
	for _, chID := range []string{"telegram", "qq", "feishu"} {
		ch, err := store.GetChannel(ctx, chID)
		if err != nil {
			continue // not configured yet
		}
		raw := json.RawMessage(ch.Config)
		switch chID {
		case "telegram":
			out.Channels.Telegram = raw
		case "qq":
			out.Channels.QQ = raw
		case "feishu":
			out.Channels.Feishu = raw
		}
	}

	return out, nil
}

// applyConfigJSONToStore writes the onboard config to the DB via the Store.
func applyConfigJSONToStore(ctx context.Context, store config.Store, body *onboardConfigJSON) error {
	// Update providers.
	for id, p := range body.Providers {
		existing, err := store.GetProvider(ctx, id)
		if err != nil {
			// Create new provider.
			if err := store.CreateProvider(ctx, config.Provider{
				ID:      id,
				Name:    id,
				APIKey:  p.APIKey,
				BaseURL: p.BaseURL,
			}); err != nil {
				return fmt.Errorf("create provider %q: %w", id, err)
			}
		} else {
			existing.APIKey = p.APIKey
			existing.BaseURL = p.BaseURL
			if err := store.UpdateProvider(ctx, existing); err != nil {
				return fmt.Errorf("update provider %q: %w", id, err)
			}
		}
	}

	// Update default agent (anna) with new model settings.
	agent, err := store.GetAgent(ctx, "anna")
	if err == nil {
		agent.ProviderID = body.Provider
		agent.Model = body.Model
		agent.ModelStrong = body.ModelStrong
		agent.ModelFast = body.ModelFast
		if body.Workspace != "" {
			agent.Workspace = body.Workspace
		}
		if err := store.UpdateAgent(ctx, agent); err != nil {
			return fmt.Errorf("update agent: %w", err)
		}
	}

	// Update channels.
	if len(body.Channels.Telegram) > 0 {
		if err := store.UpsertChannel(ctx, config.Channel{
			ID:      "telegram",
			Enabled: true,
			Config:  string(body.Channels.Telegram),
		}); err != nil {
			return fmt.Errorf("upsert telegram channel: %w", err)
		}
	}
	if len(body.Channels.QQ) > 0 {
		if err := store.UpsertChannel(ctx, config.Channel{
			ID:      "qq",
			Enabled: true,
			Config:  string(body.Channels.QQ),
		}); err != nil {
			return fmt.Errorf("upsert qq channel: %w", err)
		}
	}
	if len(body.Channels.Feishu) > 0 {
		if err := store.UpsertChannel(ctx, config.Channel{
			ID:      "feishu",
			Enabled: true,
			Config:  string(body.Channels.Feishu),
		}); err != nil {
			return fmt.Errorf("upsert feishu channel: %w", err)
		}
	}

	return nil
}

// mergeProviderModelsCache updates the models cache for a single provider,
// preserving models from other providers.
func mergeProviderModelsCache(providerName string, modelIDs []string) {
	cache, err := LoadModelsCache()
	if err != nil {
		cache = &ModelsCache{}
	}

	var kept []CachedModel
	for _, m := range cache.Models {
		if m.Provider != providerName {
			kept = append(kept, m)
		}
	}

	for _, id := range modelIDs {
		kept = append(kept, CachedModel{Provider: providerName, Model: id})
	}

	cache.Models = kept
	cache.UpdatedAt = time.Now().UTC()

	if err := SaveModelsCache(cache); err != nil {
		slog.Warn("failed to save models cache", "error", err)
	}
}

// schedulerJobJSON is the JSON shape for scheduler job create/update.
type schedulerJobJSON struct {
	Name        string `json:"name"`
	Message     string `json:"message"`
	Cron        string `json:"cron"`
	Every       string `json:"every"`
	SessionMode string `json:"session_mode"`
	Enabled     bool   `json:"enabled"`
}

func parseSchedule(body schedulerJobJSON) (scheduler.Schedule, error) {
	sched := scheduler.Schedule{Cron: body.Cron, Every: body.Every}
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

func loadSchedulerJobs(dataDir string) ([]scheduler.Job, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, "jobs.json"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []scheduler.Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return nil, fmt.Errorf("parse jobs.json: %w", err)
	}
	return jobs, nil
}

func saveSchedulerJobs(dataDir string, jobs []scheduler.Job) error {
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
