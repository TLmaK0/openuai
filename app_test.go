package main

import (
	"context"
	"sync"
	"testing"

	"openuai/internal/config"
	"openuai/internal/llm"
)

// inTreeProvider stands for a provider compiled into the binary.
type inTreeProvider struct{ name string }

func (p inTreeProvider) Chat(context.Context, []llm.Message, string) (*llm.Response, error) {
	return &llm.Response{}, nil
}
func (p inTreeProvider) Name() string     { return p.name }
func (p inTreeProvider) Models() []string { return []string{"m"} }

func appWith(providers map[string]llm.Provider) *App {
	return &App{providers: providers, cfg: &config.Config{}}
}

// registerTestProvider puts a provider in the registry for the duration of a
// test. The registry is package-level, so the removal matters as much as the
// registration: a leaked entry changes what every later test sees.
func registerTestProvider(t *testing.T, d llm.Descriptor) {
	t.Helper()
	llm.Register(d)
	t.Cleanup(func() { llm.Unregister(d.Name) })
}

// buildProviders reads the llm registry twice: once for the loop that builds
// the providers and gathers each one's default model, and again for the
// fallback name. Nothing holds the two reads together, so a provider
// registering in between can become the fallback while being absent from the
// map gathered by the first read. The settle then fills the model from that
// map, finds nothing, and settles a valid provider with an empty model, which
// reaches disk and builds turns.
//
// The interleaving is made deterministic rather than left to the scheduler:
// the late provider registers from inside another provider's constructor,
// which buildProviders calls inside that first loop.
func TestBuildProvidersSettlesOnAFallbackWithItsModel(t *testing.T) {
	t.Cleanup(func() { llm.Unregister("aaa-late") })

	registerTestProvider(t, llm.Descriptor{
		Name:         "zzz-builds-late-one",
		DefaultModel: "zzz-model",
		New: func(llm.Store) llm.Provider {
			// Inside buildProviders' loop: after llm.Descriptors() was read,
			// before llm.DefaultDescriptor() is called. "aaa-late" sorts first
			// and marks itself default, so it wins the fallback either way
			// DefaultDescriptor chooses.
			llm.Register(llm.Descriptor{
				Name:         "aaa-late",
				DefaultModel: "aaa-model",
				Default:      true,
				New:          func(llm.Store) llm.Provider { return inTreeProvider{name: "aaa-late"} },
			})
			return inTreeProvider{name: "zzz-builds-late-one"}
		},
	})

	app := appWith(map[string]llm.Provider{})
	// An unregistered name, so settling has to fall back.
	app.cfg.SetProviderAndModel("not-registered", "")

	app.buildProviders()

	provider, model := app.cfg.ProviderAndModel()
	if provider != "aaa-late" {
		t.Fatalf("settled on provider %q, want the fallback aaa-late", provider)
	}
	if model != "aaa-model" {
		t.Errorf("settled on provider %q with model %q, want aaa-model: the fallback was chosen by a "+
			"registry read the model map does not cover", provider, model)
	}
}

// The provider map is read and written from different goroutines: a turn asks
// for the active provider while the settings screen changes which one that is,
// and buildProviders replaces the map wholesale. Without a guard this is a
// concurrent map access, which aborts the process rather than merely returning
// something stale.
func TestProviderMapIsSafeUnderConcurrentUse(t *testing.T) {
	registerTestProvider(t, llm.Descriptor{
		Name:         "in-tree",
		DefaultModel: "in-tree-model",
		Default:      true,
		New:          func(llm.Store) llm.Provider { return inTreeProvider{name: "in-tree"} },
	})

	app := appWith(map[string]llm.Provider{"in-tree": inTreeProvider{name: "in-tree"}})
	app.cfg.SetProviderAndModel("in-tree", "in-tree-model")

	const rounds = 200
	var wg sync.WaitGroup

	// Rebuilding the whole map, as settling on a provider does.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			app.buildProviders()
		}
	}()

	// Writing a provider into the map.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			app.setProvider("churn", inTreeProvider{name: "churn"})
		}
	}()

	// And what an agent turn does on its own goroutine.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = app.provider("in-tree")
				_ = app.activeProvider()
			}
		}()
	}

	wg.Wait()
}

// The settings screen lists the providers and describes the active one. It
// asks the providers nothing beyond that, so listing them must not depend on
// any of them being usable.
func TestGetProvidersListsAndDescribesTheActiveOne(t *testing.T) {
	registerTestProvider(t, llm.Descriptor{
		Name:         "listed",
		DisplayName:  "Listed",
		Credential:   llm.CredentialSecret,
		DefaultModel: "listed-model",
		New:          func(llm.Store) llm.Provider { return inTreeProvider{name: "listed"} },
	})

	app := appWith(map[string]llm.Provider{"listed": inTreeProvider{name: "listed"}})

	found := false
	for _, s := range app.GetProviders() {
		if s.Name == "listed" {
			found = true
			if s.DisplayName != "Listed" {
				t.Errorf("summary display name = %q, want Listed", s.DisplayName)
			}
		}
	}
	if !found {
		t.Error("a registered provider is missing from the provider list")
	}

	app.cfg.SetProviderAndModel("listed", "listed-model")
	if info := app.GetActiveProvider(); info.Name != "listed" || info.Credential != "secret" {
		t.Errorf("GetActiveProvider() = %+v", info)
	}

	// An unknown active provider is reported as empty rather than crashing the
	// settings screen.
	app.cfg.SetProviderAndModel("never-registered", "")
	if got := app.GetActiveProvider(); got.Name != "" {
		t.Errorf("GetActiveProvider() for an unknown name = %+v, want empty", got)
	}
}

// Shutting down has to survive being called before start-up finished — an
// early quit, or a start-up that failed partway — so every step it takes is
// guarded. The plugin loop that used to be here is gone; these guards are what
// is left, and they are the reason shutdown can run at all in that state.
func TestShutdownSurvivesNothingHavingStarted(t *testing.T) {
	app := appWith(map[string]llm.Provider{})
	// No wake listener, no API server, and a tray that was never started.
	app.shutdown(context.Background())
}
