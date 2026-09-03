package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"

	"openuai/internal/config"
	"openuai/internal/llm"
	"openuai/internal/llm/plugin"
)

// countingConn records whether the plugin it stands for was closed.
type countingConn struct {
	mu     *sync.Mutex
	closed *int
}

func (c countingConn) Start(context.Context) error { return nil }

func (c countingConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	*c.closed++
	return nil
}

func (c countingConn) SendRequest(_ context.Context, req transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	return &transport.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: []byte(`{}`)}, nil
}

// closeCounter reports how many times the plugin it belongs to was closed.
type closeCounter struct {
	mu    sync.Mutex
	count int
}

func (c *closeCounter) get() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// startedPluginClient returns a plugin provider with its process already
// running, plus the counter that records its shutdown.
func startedPluginClient(t *testing.T, name string) (*plugin.Client, *closeCounter) {
	t.Helper()

	counter := &closeCounter{}
	client := plugin.NewClient(
		plugin.Description{Name: name},
		func() plugin.Conn { return countingConn{mu: &counter.mu, closed: &counter.count} },
	)
	// Any call starts the process; Chat is enough and its reply is ignored.
	client.Chat(context.Background(), nil, "model")
	return client, counter
}

// inTreeProvider stands for a provider compiled into the binary, which has no
// process to stop.
type inTreeProvider struct{ name string }

func (p inTreeProvider) Chat(context.Context, []llm.Message, string) (*llm.Response, error) {
	return &llm.Response{}, nil
}
func (p inTreeProvider) Name() string     { return p.name }
func (p inTreeProvider) Models() []string { return []string{"m"} }

// closingInTreeProvider is a compiled-in provider that owns something to shut
// down — an HTTP client with an idle connection pool is enough of a reason to
// grow a Close method. It runs no child process, so nothing about it should
// reach the plugin lifecycle.
type closingInTreeProvider struct {
	inTreeProvider
	closed *closeCounter
}

func (p closingInTreeProvider) Close() error {
	p.closed.mu.Lock()
	defer p.closed.mu.Unlock()
	p.closed.count++
	return nil
}

func appWith(providers map[string]llm.Provider) *App {
	return &App{providers: providers, cfg: &config.Config{}}
}

// Quitting the app must stop the providers that run as child processes, or
// they outlive the app that started them.
func TestTakeProviderPluginsCollectsEveryChildProcess(t *testing.T) {
	first, firstClosed := startedPluginClient(t, "first")
	second, secondClosed := startedPluginClient(t, "second")

	app := appWith(map[string]llm.Provider{
		"first":   first,
		"second":  second,
		"in-tree": inTreeProvider{name: "in-tree"},
	})

	taken := app.takeProviderPlugins()
	if len(taken) != 2 {
		t.Fatalf("took %d providers, want the 2 plugins", len(taken))
	}
	for name, p := range taken {
		stopProviderPlugin(name, p)
	}

	if firstClosed.get() != 1 || secondClosed.get() != 1 {
		t.Errorf("plugins closed %d and %d times, want 1 each", firstClosed.get(), secondClosed.get())
	}
	// A provider compiled in is left in place, not taken and not closed.
	if app.provider("in-tree") == nil {
		t.Error("taking the plugins dropped a compiled-in provider")
	}
	if app.provider("first") != nil || app.provider("second") != nil {
		t.Error("a taken plugin is still in the provider map")
	}
}

// The plugin lifecycle asks "does this run as a child process", and the answer
// must not be "does this have a Close method". A compiled-in provider that
// grows one would otherwise be collected as a plugin: closed on shutdown,
// which is probably harmless, and dropped from the provider map, which leaves
// the app with a provider it can no longer serve a turn with.
func TestACompiledInProviderWithACloseIsNotAPlugin(t *testing.T) {
	plug, plugClosed := startedPluginClient(t, "plugin")
	closed := &closeCounter{}
	app := appWith(map[string]llm.Provider{
		"plugin":  plug,
		"in-tree": closingInTreeProvider{inTreeProvider{name: "in-tree"}, closed},
	})

	taken := app.takeProviderPlugins()
	if len(taken) != 1 {
		t.Fatalf("took %d providers, want only the plugin", len(taken))
	}
	if _, ok := taken["plugin"]; !ok {
		t.Errorf("took %v, want the plugin", taken)
	}
	for name, p := range taken {
		stopProviderPlugin(name, p)
	}

	if app.provider("in-tree") == nil {
		t.Error("a compiled-in provider with a Close was dropped from the provider map")
	}
	if closed.get() != 0 {
		t.Errorf("a compiled-in provider was closed %d times, want 0", closed.get())
	}
	if plugClosed.get() != 1 {
		t.Errorf("the plugin was closed %d times, want 1", plugClosed.get())
	}

	// The same by name, which is the path a removal takes.
	stopProviderPlugin("in-tree", app.takeProvider("in-tree"))
	if closed.get() != 0 {
		t.Errorf("removing by name closed a compiled-in provider %d times, want 0", closed.get())
	}
}

// Removing or replacing one plugin must stop that one and leave the rest
// running.
func TestTakeProviderStopsOnlyTheNamedOne(t *testing.T) {
	doomed, doomedClosed := startedPluginClient(t, "doomed")
	kept, keptClosed := startedPluginClient(t, "kept")

	app := appWith(map[string]llm.Provider{"doomed": doomed, "kept": kept})

	stopProviderPlugin("doomed", app.takeProvider("doomed"))

	if doomedClosed.get() != 1 {
		t.Errorf("the named plugin was closed %d times, want 1", doomedClosed.get())
	}
	if keptClosed.get() != 0 {
		t.Errorf("another plugin was closed %d times, want 0", keptClosed.get())
	}
	if app.provider("doomed") != nil {
		t.Error("the named plugin is still in the provider map")
	}
	if app.provider("kept") == nil {
		t.Error("the wrong plugin was dropped")
	}
}

// Stopping a provider that is not a plugin, or one that was never there, must
// not panic — both happen on an ordinary shutdown.
func TestStopProviderPluginToleratesNonPlugins(t *testing.T) {
	app := appWith(map[string]llm.Provider{"in-tree": inTreeProvider{name: "in-tree"}})

	stopProviderPlugin("in-tree", app.takeProvider("in-tree"))
	if app.provider("in-tree") != nil {
		t.Error("the provider was not dropped from the map")
	}

	// An absent name yields nil, and stopping nil is a no-op.
	stopProviderPlugin("never-existed", app.takeProvider("never-existed"))
	if taken := app.takeProviderPlugins(); len(taken) != 0 {
		t.Errorf("took %d providers from an empty map, want 0", len(taken))
	}
}

// A plugin can be added or removed while agent turns are running, so the
// provider map is read and written from different goroutines at once. Without
// a guard this is a concurrent map access, which aborts the process rather
// than merely returning something stale.
func TestProviderMapIsSafeUnderConcurrentUse(t *testing.T) {
	app := appWith(map[string]llm.Provider{"in-tree": inTreeProvider{name: "in-tree"}})
	app.cfg.SetProviderAndModel("in-tree", "")

	const rounds = 200
	var wg sync.WaitGroup

	// Readers: what an agent turn does on its own goroutine.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				_ = app.activeProvider()
				_ = app.provider("churn")
			}
		}()
	}

	// Writers: what the settings screen does when a plugin is added or removed.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			app.setProvider("churn", inTreeProvider{name: "churn"})
			app.takeProvider("churn")
		}
	}()

	wg.Wait()

	// The compiled-in provider is untouched by all that churn.
	if app.provider("in-tree") == nil {
		t.Error("the compiled-in provider went missing")
	}
}

// countingDialer records how many times a plugin process was started.
type countingDialer struct {
	mu      sync.Mutex
	starts  int
	counter *closeCounter
}

func (d *countingDialer) dial() plugin.Conn {
	d.mu.Lock()
	d.starts++
	d.mu.Unlock()
	return countingConn{mu: &d.counter.mu, closed: &d.counter.count}
}

func (d *countingDialer) startCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.starts
}

// Listing the providers must ask them nothing. Gathering readiness here used
// to start every plugin process — on a screen that only shows the active
// provider's readiness — so the app froze for as long as the slowest plugin
// took to answer, on start-up.
func TestGetProvidersStartsNoPluginProcess(t *testing.T) {
	llm.Unregister("listed-plugin")
	dialer := &countingDialer{counter: &closeCounter{}}
	client := plugin.NewClient(
		plugin.Description{Name: "listed-plugin", DisplayName: "Listed", SupportsReady: true},
		dialer.dial,
	)
	if err := llm.RegisterDynamic(plugin.Description{
		Name:        "listed-plugin",
		DisplayName: "Listed",
		Credential:  string(llm.CredentialSecret),
	}.Descriptor(func(llm.Store) llm.Provider { return client })); err != nil {
		t.Fatalf("RegisterDynamic() = %v", err)
	}
	defer llm.Unregister("listed-plugin")

	app := appWith(map[string]llm.Provider{"listed-plugin": client})

	summaries := app.GetProviders()
	if dialer.startCount() != 0 {
		t.Errorf("listing the providers started %d plugin processes, want 0", dialer.startCount())
	}

	var found bool
	for _, s := range summaries {
		if s.Name == "listed-plugin" {
			found = true
			if s.DisplayName != "Listed" {
				t.Errorf("summary display name = %q, want Listed", s.DisplayName)
			}
		}
	}
	if !found {
		t.Error("the plugin is missing from the provider list")
	}

	// Asking about the provider in use does start it, which is the one place
	// that is worth paying for.
	app.cfg.SetProviderAndModel("listed-plugin", "")
	info := app.GetActiveProvider()
	if info.Name != "listed-plugin" || info.Credential != "secret" {
		t.Errorf("GetActiveProvider() = %+v", info)
	}
	if dialer.startCount() != 1 {
		t.Errorf("asking the active provider started %d processes, want 1", dialer.startCount())
	}

	// An unknown active provider is reported as empty rather than crashing
	// the settings screen.
	app.cfg.SetProviderAndModel("never-registered", "")
	if got := app.GetActiveProvider(); got.Name != "" {
		t.Errorf("GetActiveProvider() for an unknown name = %+v, want empty", got)
	}
}

// The fix for plugin processes outliving the app lives in shutdown(), so the
// test has to call shutdown() — covering only the helpers left the call site
// free to be deleted, which is exactly what mutation testing found: removing
// the loop from shutdown() left the whole suite green.
func TestShutdownStopsProviderPlugins(t *testing.T) {
	client, closed := startedPluginClient(t, "plugin")
	app := appWith(map[string]llm.Provider{
		"plugin":  client,
		"in-tree": inTreeProvider{name: "in-tree"},
	})

	// shutdown() also stops the wake listener, the API server and the tray.
	// The first two are nil here and skipped; tray.Stop() is a no-op until
	// the tray has been started.
	app.shutdown(context.Background())

	if closed.get() != 1 {
		t.Errorf("the plugin was closed %d times after shutdown, want 1", closed.get())
	}
	if app.provider("plugin") != nil {
		t.Error("the plugin is still in the provider map after shutdown")
	}
	// A provider compiled into the binary has no process and is left alone.
	if app.provider("in-tree") == nil {
		t.Error("shutdown dropped a compiled-in provider")
	}
}

// A plugin declares the prices of the models it serves, so removing it has to
// withdraw them. Left behind, they price models nothing can serve any more,
// and a later plugin reusing a model name would inherit them.
func TestRemovingAPluginWithdrawsItsPrices(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	// The config needs somewhere to save; ProviderStore.Set and the plugin
	// accessors both persist.
	cfg.SetPath(filepath.Join(dir, "config.json"))

	desc := plugin.Description{
		Name:    "priced",
		Pricing: map[string][2]float64{"priced-large": {4.0, 8.0}},
	}
	raw, err := json.Marshal(desc)
	if err != nil {
		t.Fatalf("encoding the description: %v", err)
	}
	if err := cfg.SetProviderPlugin(config.ProviderPluginConfig{
		Name: "priced", Command: "priced", Description: raw,
	}); err != nil {
		t.Fatalf("SetProviderPlugin() = %v", err)
	}

	llm.SetModelPricing("priced-large", 4.0, 8.0)
	if _, ok := llm.ModelPricing("priced-large"); !ok {
		t.Fatal("the price was not declared to begin with")
	}

	app := appWith(map[string]llm.Provider{})
	app.cfg = cfg
	app.cfg.SetProviderAndModel("something-else", "")

	if errText := app.RemoveProviderPlugin("priced"); errText != "" {
		t.Fatalf("RemoveProviderPlugin() = %q, want no error", errText)
	}

	if price, ok := llm.ModelPricing("priced-large"); ok {
		t.Errorf("the removed plugin's price is still in the table: %v", price)
	}
}

// The cross-check that refuses a self-contradictory description runs in
// Describe, which is only reached when a plugin is added. A cached description
// comes back from a file a person can edit, so an entry asking for a
// credential the plugin cannot accept would otherwise be registered and reach
// the settings screen as a key field that rejects whatever is typed into it.
func TestACachedDescriptionIsCrossCheckedToo(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.SetPath(filepath.Join(dir, "config.json"))

	// A hand-edited entry: it asks for a secret, and declares no support for
	// taking one.
	contradictory, err := json.Marshal(plugin.Description{
		Name:       "handedited",
		Credential: string(llm.CredentialSecret),
	})
	if err != nil {
		t.Fatalf("encoding the description: %v", err)
	}
	sound, err := json.Marshal(plugin.Description{
		Name:           "sound",
		Credential:     string(llm.CredentialSecret),
		SupportsSecret: true,
	})
	if err != nil {
		t.Fatalf("encoding the description: %v", err)
	}
	for _, entry := range []config.ProviderPluginConfig{
		{Name: "handedited", Command: "handedited", Description: contradictory},
		{Name: "sound", Command: "sound", Description: sound},
	} {
		if err := cfg.SetProviderPlugin(entry); err != nil {
			t.Fatalf("SetProviderPlugin(%s) = %v", entry.Name, err)
		}
	}
	t.Cleanup(func() {
		llm.Unregister("handedited")
		llm.Unregister("sound")
	})

	app := appWith(map[string]llm.Provider{})
	app.cfg = cfg
	app.loadProviderPlugins()

	if _, ok := llm.Lookup("handedited"); ok {
		t.Error("a description asking for a credential it cannot accept was registered")
	}
	// The sound entry beside it is still loaded: one bad entry is skipped, not
	// the whole list.
	if _, ok := llm.Lookup("sound"); !ok {
		t.Error("a sound cached description was not registered")
	}
}
