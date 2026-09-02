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
