package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"openuai/internal/logger"
	"openuai/internal/marketplace"
)

// marketplaceURLEnv overrides the registry URL of the configuration
// (marketplace_url), so a registry can be tried without editing config.json.
const marketplaceURLEnv = "OPENUAI_MARKETPLACE_URL"

// marketplaceState holds the marketplace store, built on first use because it
// needs the configuration that startup loads.
type marketplaceState struct {
	once  sync.Once
	store *marketplace.Store
}

// marketplaceRegistryURL picks the registry: environment, then configuration,
// then the default registry in the OpenUAI repository.
func (a *App) marketplaceRegistryURL() string {
	if u := strings.TrimSpace(os.Getenv(marketplaceURLEnv)); u != "" {
		return u
	}
	if a.cfg != nil {
		if u := strings.TrimSpace(a.cfg.MarketplaceURL()); u != "" {
			return u
		}
	}
	return marketplace.DefaultRegistryURL
}

func (a *App) marketplaceStore() *marketplace.Store {
	a.market.once.Do(func() {
		cacheDir := ""
		if a.cfg != nil && a.cfg.Path() != "" {
			cacheDir = filepath.Join(a.cfg.ConfigDir(), "marketplace")
		}
		a.market.store = marketplace.NewStore(a.marketplaceRegistryURL(), cacheDir)
		logger.Info("Marketplace registry: %s", a.market.store.URL())
	})
	return a.market.store
}

func (a *App) marketplaceCtx() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// logMarketplace reports where a catalog came from and what was skipped.
func logMarketplace(l marketplace.Listing) marketplace.Listing {
	logger.Info("Marketplace catalog: %d entries from %s (%s)", len(l.Entries), l.Source, l.URL)
	if l.Notice != "" {
		logger.Info("Marketplace: %s", l.Notice)
	}
	for _, w := range l.Warnings {
		logger.Info("Marketplace registry warning: %s", w)
	}
	return l
}
