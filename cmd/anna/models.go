package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/ai"
	"github.com/vaayne/anna/internal/ai/providers/anthropic"
	"github.com/vaayne/anna/internal/ai/providers/openai"
	openairesponse "github.com/vaayne/anna/internal/ai/providers/openai-response"
	"github.com/vaayne/anna/internal/channel"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
)

// CachedModel is the on-disk representation of a model in models.json.
type CachedModel struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ModelsCache is the top-level structure for models.json in the workspace.
type ModelsCache struct {
	UpdatedAt time.Time     `json:"updated_at"`
	Models    []CachedModel `json:"models"`
}

func modelsCachePath() string {
	return filepath.Join(config.CachePath(), "models.json")
}

// LoadModelsCache reads the cached models from the workspace models.json.
func LoadModelsCache() (*ModelsCache, error) {
	data, err := os.ReadFile(modelsCachePath())
	if err != nil {
		return nil, err
	}
	var cache ModelsCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return nil, fmt.Errorf("parse models cache: %w", err)
	}
	return &cache, nil
}

// SaveModelsCache writes the models cache to the cache directory.
func SaveModelsCache(cache *ModelsCache) error {
	path := modelsCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal models cache: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// fetchModelsFromProviders queries all configured providers (from the DB Store)
// for their model lists.
func fetchModelsFromProviders(ctx context.Context, store config.Store) []CachedModel {
	seen := make(map[string]bool)
	var models []CachedModel

	add := func(provider, model string) {
		key := provider + "/" + model
		if seen[key] {
			return
		}
		seen[key] = true
		models = append(models, CachedModel{Provider: provider, Model: model})
	}

	providers, err := store.ListProviders(ctx)
	if err != nil {
		slog.Warn("failed to list providers", "error", err)
		return nil
	}

	for _, prov := range providers {
		p := newStreamProviderFromCreds(prov.ID, prov.APIKey, prov.BaseURL)
		if p == nil {
			continue
		}
		lister, ok := p.(ai.ModelLister)
		if !ok {
			continue
		}
		fetchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		listed, err := lister.ListModels(fetchCtx)
		cancel()
		if err != nil {
			slog.Warn("failed to list models from provider", "provider", prov.ID, "error", err)
			continue
		}
		for _, m := range listed {
			add(prov.ID, m.ID)
		}
	}

	return models
}

// collectModelsFromStore builds the list of available provider/model pairs
// using the Store and models cache.
func collectModelsFromStore(ctx context.Context, store config.Store, snap *config.Snapshot) []channel.ModelOption {
	seen := make(map[string]bool)
	var models []channel.ModelOption

	add := func(provider, model string) {
		key := provider + "/" + model
		if seen[key] {
			return
		}
		seen[key] = true
		models = append(models, channel.ModelOption{Provider: provider, Model: model})
	}

	// Current model first.
	add(snap.Provider, snap.Model)

	// Load from cache.
	if cache, err := LoadModelsCache(); err == nil {
		for _, m := range cache.Models {
			add(m.Provider, m.Model)
		}
		return models
	}

	// Fallback: list from provider API via store.
	providers, err := store.ListProviders(ctx)
	if err == nil {
		for _, prov := range providers {
			add(prov.ID, snap.Model)
		}
	}

	return models
}

// newStreamProviderFromCreds creates an ai.ProviderAdapter from raw credentials.
func newStreamProviderFromCreds(name, apiKey, baseURL string) ai.ProviderAdapter {
	switch name {
	case "anthropic":
		return anthropic.New(anthropic.Config{BaseURL: baseURL, APIKey: apiKey})
	case "openai":
		return openai.New(openai.Config{BaseURL: baseURL, APIKey: apiKey})
	case "openai-response":
		return openairesponse.New(openairesponse.Config{BaseURL: baseURL, APIKey: apiKey})
	default:
		return nil
	}
}

// openStore is a helper that opens the DB and returns a Store.
func openStore() (config.Store, error) {
	db, err := appdb.OpenDB(config.DBPath())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	store := config.NewDBStore(db)
	if err := store.SeedDefaults(context.Background()); err != nil {
		return nil, fmt.Errorf("seed defaults: %w", err)
	}
	return store, nil
}

// defaultSnapshot returns a snapshot for the first enabled agent.
func defaultSnapshot(ctx context.Context, store config.Store) (*config.Snapshot, error) {
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil || len(agents) == 0 {
		return nil, fmt.Errorf("no enabled agents found")
	}
	return store.Snapshot(ctx, agents[0].ID)
}

func modelsCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "models",
		Usage: "Manage available models",
		Subcommands: []*ucli.Command{
			modelsListCommand(),
			modelsUpdateCommand(),
			modelsCurrentCommand(),
			modelsSetCommand(),
			modelsSearchCommand(),
		},
		Action: modelsListAction,
	}
}

func modelsListCommand() *ucli.Command {
	return &ucli.Command{
		Name:   "list",
		Usage:  "List all available models grouped by provider",
		Action: modelsListAction,
	}
}

func modelsListAction(c *ucli.Context) error {
	store, err := openStore()
	if err != nil {
		return err
	}
	snap, err := defaultSnapshot(c.Context, store)
	if err != nil {
		return err
	}

	models := collectModelsFromStore(c.Context, store, snap)
	printModelsGrouped(models, snap.Provider, snap.Model)
	return nil
}

func modelsUpdateCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "update",
		Usage: "Fetch models from all provider APIs and update the cache",
		Action: func(c *ucli.Context) error {
			store, err := openStore()
			if err != nil {
				return err
			}

			providers, err := store.ListProviders(c.Context)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Fetching models from %d provider(s)...\n", len(providers))

			cached := fetchModelsFromProviders(c.Context, store)
			cache := &ModelsCache{
				UpdatedAt: time.Now().UTC(),
				Models:    cached,
			}

			if err := SaveModelsCache(cache); err != nil {
				return fmt.Errorf("save models cache: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Cached %d models to %s\n", len(cached), modelsCachePath())
			return nil
		},
	}
}

func modelsCurrentCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "current",
		Usage: "Show the active provider and model",
		Action: func(c *ucli.Context) error {
			store, err := openStore()
			if err != nil {
				return err
			}
			snap, err := defaultSnapshot(c.Context, store)
			if err != nil {
				return err
			}
			fmt.Printf("%s/%s\n", snap.Provider, snap.Model)
			return nil
		},
	}
}

func modelsSetCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "set",
		Usage:     "Switch the active model (e.g. anna models set openai/gpt-4o)",
		ArgsUsage: "<provider/model>",
		Action: func(c *ucli.Context) error {
			arg := c.Args().First()
			if arg == "" {
				return fmt.Errorf("usage: anna models set <provider/model>")
			}

			provider, model, ok := strings.Cut(arg, "/")
			if !ok || provider == "" || model == "" {
				return fmt.Errorf("invalid format %q, expected provider/model", arg)
			}

			store, err := openStore()
			if err != nil {
				return err
			}

			// Update the default agent's model in the DB.
			agents, err := store.ListEnabledAgents(c.Context)
			if err != nil || len(agents) == 0 {
				return fmt.Errorf("no enabled agents found")
			}
			agent := agents[0]
			agent.ProviderID = provider
			agent.Model = model
			if err := store.UpdateAgent(c.Context, agent); err != nil {
				return fmt.Errorf("update agent: %w", err)
			}

			fmt.Printf("Switched to %s/%s\n", provider, model)
			return nil
		},
	}
}

func modelsSearchCommand() *ucli.Command {
	return &ucli.Command{
		Name:      "search",
		Usage:     "Search models by name (e.g. anna models search gpt)",
		ArgsUsage: "<query>",
		Action: func(c *ucli.Context) error {
			query := strings.ToLower(c.Args().First())
			if query == "" {
				return fmt.Errorf("usage: anna models search <query>")
			}

			store, err := openStore()
			if err != nil {
				return err
			}
			snap, err := defaultSnapshot(c.Context, store)
			if err != nil {
				return err
			}

			models := collectModelsFromStore(c.Context, store, snap)
			var matched []channel.ModelOption
			for _, m := range models {
				label := strings.ToLower(m.Provider + "/" + m.Model)
				if strings.Contains(label, query) {
					matched = append(matched, m)
				}
			}

			if len(matched) == 0 {
				fmt.Fprintf(os.Stderr, "No models matching %q\n", query)
				return nil
			}

			printModelsGrouped(matched, snap.Provider, snap.Model)
			return nil
		},
	}
}

// printModelsGrouped prints models grouped by provider, marking the active one.
func printModelsGrouped(models []channel.ModelOption, activeProvider, activeModel string) {
	grouped := make(map[string][]string)
	var provOrder []string
	seen := make(map[string]bool)

	for _, m := range models {
		if !seen[m.Provider] {
			seen[m.Provider] = true
			provOrder = append(provOrder, m.Provider)
		}
		grouped[m.Provider] = append(grouped[m.Provider], m.Model)
	}

	for _, prov := range provOrder {
		fmt.Printf("%s:\n", prov)
		for _, model := range grouped[prov] {
			marker := "  "
			suffix := ""
			if prov == activeProvider && model == activeModel {
				marker = "* "
				suffix = " (current)"
			}
			fmt.Printf("  %s%s%s\n", marker, model, suffix)
		}
	}
}
