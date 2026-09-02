package main

import (
	"context"
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
	app.cfg.Provider = "in-tree"

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
	app.cfg.Provider = "listed-plugin"
	info := app.GetActiveProvider()
	if info.Name != "listed-plugin" || info.Credential != "secret" {
		t.Errorf("GetActiveProvider() = %+v", info)
	}
	if dialer.startCount() != 1 {
		t.Errorf("asking the active provider started %d processes, want 1", dialer.startCount())
	}

	// An unknown active provider is reported as empty rather than crashing
	// the settings screen.
	app.cfg.Provider = "never-registered"
	if got := app.GetActiveProvider(); got.Name != "" {
		t.Errorf("GetActiveProvider() for an unknown name = %+v, want empty", got)
	}
}
