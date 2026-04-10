package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	ucli "github.com/urfave/cli/v2"
	"github.com/vaayne/anna/internal/config"
	appdb "github.com/vaayne/anna/internal/db"
	"github.com/vaayne/anna/internal/pluginhost"
	pkgchannel "github.com/vaayne/anna/pkg/channel"
	providerapi "github.com/vaayne/anna/pkg/providers"
)

// fetchModelsFromProviders queries all configured providers (from the DB Store)
// for their model lists.
func fetchModelsFromProviders(ctx context.Context, store config.Store, host *pluginhost.Host) []config.CachedModel {
	seen := make(map[string]bool)
	var models []config.CachedModel

	add := func(provider, model string) {
		key := provider + "/" + model
		if seen[key] {
			return
		}
		seen[key] = true
		models = append(models, config.CachedModel{Provider: provider, Model: model})
	}

	providers, err := store.ListProviders(ctx)
	if err != nil {
		slog.Warn("failed to list providers", "error", err)
		return nil
	}

	for _, prov := range providers {
		p, err := host.BuildProvider(prov.ID, map[string]any{
			"api_key":  prov.APIKey,
			"base_url": prov.BaseURL,
		})
		if err != nil {
			slog.Debug("failed to build provider", "provider", prov.ID, "error", err)
			continue
		}
		if p == nil {
			continue
		}
		lister, ok := p.(providerapi.ModelLister)
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

func newProviderHost(store config.Store) (*pluginhost.Host, error) {
	host := pluginhost.New(store)
	if err := host.LoadDefaultCatalog(); err != nil {
		return nil, err
	}
	return host, nil
}

// collectModelsFromStore builds the list of available provider/model pairs
// using the Store and models cache.
func collectModelsFromStore(ctx context.Context, store config.Store, snap *config.Snapshot) []pkgchannel.ModelOption {
	collector := newModelOptionCollector()
	collector.Add(snap.Provider, snap.Model)

	if cache, err := config.LoadModelsCache(); err == nil {
		for _, model := range cache.Models {
			collector.Add(model.Provider, model.Model)
		}
		return collector.Models()
	}

	providers, err := store.ListProviders(ctx)
	if err == nil {
		for _, provider := range providers {
			collector.Add(provider.ID, snap.Model)
		}
	}

	return collector.Models()
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

func openStoreAndDefaultSnapshot(ctx context.Context) (config.Store, *config.Snapshot, error) {
	store, err := openStore()
	if err != nil {
		return nil, nil, err
	}
	snap, err := defaultSnapshot(ctx, store)
	if err != nil {
		return nil, nil, err
	}
	return store, snap, nil
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
	store, snap, err := openStoreAndDefaultSnapshot(c.Context)
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

			host, err := newProviderHost(store)
			if err != nil {
				return fmt.Errorf("load plugin host: %w", err)
			}
			cached := fetchModelsFromProviders(c.Context, store, host)
			cache := &config.ModelsCache{
				UpdatedAt: time.Now().UTC(),
				Models:    cached,
			}

			if err := config.SaveModelsCache(cache); err != nil {
				return fmt.Errorf("save models cache: %w", err)
			}

			fmt.Fprintf(os.Stderr, "Cached %d models to %s\n", len(cached), config.ModelsCachePath())
			return nil
		},
	}
}

func modelsCurrentCommand() *ucli.Command {
	return &ucli.Command{
		Name:  "current",
		Usage: "Show the active provider and model",
		Action: func(c *ucli.Context) error {
			_, snap, err := openStoreAndDefaultSnapshot(c.Context)
			if err != nil {
				return err
			}
			fmt.Printf("%s\n", snap.Model)
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
			provider, model, err := parseProviderModelArg(arg)
			if err != nil {
				return err
			}

			store, err := openStore()
			if err != nil {
				return err
			}
			if err := updateDefaultAgentModel(c.Context, store, arg); err != nil {
				return err
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
			query := strings.TrimSpace(c.Args().First())
			if query == "" {
				return fmt.Errorf("usage: anna models search <query>")
			}

			store, snap, err := openStoreAndDefaultSnapshot(c.Context)
			if err != nil {
				return err
			}

			matched := searchModels(collectModelsFromStore(c.Context, store, snap), query)
			if len(matched) == 0 {
				fmt.Fprintf(os.Stderr, "No models matching %q\n", query)
				return nil
			}

			printModelsGrouped(matched, snap.Provider, snap.Model)
			return nil
		},
	}
}

func parseProviderModelArg(arg string) (string, string, error) {
	if arg == "" {
		return "", "", fmt.Errorf("usage: anna models set <provider/model>")
	}
	provider, model, ok := strings.Cut(arg, "/")
	if !ok || provider == "" || model == "" {
		return "", "", fmt.Errorf("invalid format %q, expected provider/model", arg)
	}
	return provider, model, nil
}

func updateDefaultAgentModel(ctx context.Context, store config.Store, model string) error {
	agents, err := store.ListEnabledAgents(ctx)
	if err != nil || len(agents) == 0 {
		return fmt.Errorf("no enabled agents found")
	}
	agent := agents[0]
	agent.Model = model
	if err := store.UpdateAgent(ctx, agent); err != nil {
		return fmt.Errorf("update agent: %w", err)
	}
	return nil
}

func searchModels(models []pkgchannel.ModelOption, query string) []pkgchannel.ModelOption {
	query = strings.ToLower(query)
	var matched []pkgchannel.ModelOption
	for _, model := range models {
		label := strings.ToLower(model.Provider + "/" + model.Model)
		if strings.Contains(label, query) {
			matched = append(matched, model)
		}
	}
	return matched
}

type modelOptionCollector struct {
	seen   map[string]bool
	models []pkgchannel.ModelOption
}

func newModelOptionCollector() *modelOptionCollector {
	return &modelOptionCollector{seen: make(map[string]bool)}
}

func (c *modelOptionCollector) Add(provider, model string) {
	key := provider + "/" + model
	if c.seen[key] {
		return
	}
	c.seen[key] = true
	c.models = append(c.models, pkgchannel.ModelOption{Provider: provider, Model: model})
}

func (c *modelOptionCollector) Models() []pkgchannel.ModelOption {
	return c.models
}

// printModelsGrouped prints models grouped by provider, marking the active one.
func printModelsGrouped(models []pkgchannel.ModelOption, activeProvider, activeModel string) {
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
