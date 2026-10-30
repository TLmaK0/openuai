package main

import (
	"encoding/json"
	"testing"

	"openuai/internal/config"
	"openuai/internal/marketplace"
)

func TestMarketplaceRegistryURLPrecedence(t *testing.T) {
	t.Setenv(marketplaceURLEnv, "")
	a := &App{cfg: &config.Config{}}
	if got := a.marketplaceRegistryURL(); got != marketplace.DefaultRegistryURL {
		t.Fatalf("default: %q", got)
	}

	cfg := &config.Config{}
	if err := json.Unmarshal([]byte(`{"marketplace_url":"http://config.example/r.json"}`), cfg); err != nil {
		t.Fatal(err)
	}
	a = &App{cfg: cfg}
	if got := a.marketplaceRegistryURL(); got != "http://config.example/r.json" {
		t.Fatalf("config: %q", got)
	}

	t.Setenv(marketplaceURLEnv, "http://env.example/r.json")
	if got := a.marketplaceRegistryURL(); got != "http://env.example/r.json" {
		t.Fatalf("env: %q", got)
	}
}
