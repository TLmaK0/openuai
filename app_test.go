package main

import (
	"context"
	"testing"

	"github.com/mark3labs/mcp-go/client/transport"

	"openuai/internal/llm"
	"openuai/internal/llm/plugin"
)

// countingConn records whether the plugin it stands for was closed.
type countingConn struct{ closed *int }

func (c countingConn) Start(context.Context) error { return nil }
func (c countingConn) Close() error                { *c.closed++; return nil }
func (c countingConn) SendRequest(_ context.Context, req transport.JSONRPCRequest) (*transport.JSONRPCResponse, error) {
	return &transport.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: []byte(`{}`)}, nil
}

// startedPluginClient returns a plugin provider with its process already
// running, plus the counter that records its shutdown.
func startedPluginClient(t *testing.T, name string) (*plugin.Client, *int) {
	t.Helper()

	closed := 0
	client := plugin.NewClient(
		plugin.Description{Name: name},
		func() plugin.Conn { return countingConn{closed: &closed} },
	)
	// Any call starts the process; Chat is enough and its reply is ignored.
	client.Chat(context.Background(), nil, "model")
	return client, &closed
}

// inTreeProvider stands for a provider compiled into the binary, which has no
// process to stop.
type inTreeProvider struct{}

func (inTreeProvider) Chat(context.Context, []llm.Message, string) (*llm.Response, error) {
	return &llm.Response{}, nil
}
func (inTreeProvider) Name() string     { return "in-tree" }
func (inTreeProvider) Models() []string { return []string{"m"} }

// Quitting the app must stop the providers that run as child processes, or
// they outlive the app that started them.
func TestStopProviderPluginsStopsEveryChildProcess(t *testing.T) {
	first, firstClosed := startedPluginClient(t, "first")
	second, secondClosed := startedPluginClient(t, "second")

	providers := map[string]llm.Provider{
		"first":   first,
		"second":  second,
		"in-tree": inTreeProvider{},
	}

	stopProviderPlugins(providers)

	if *firstClosed != 1 || *secondClosed != 1 {
		t.Errorf("plugins closed %d and %d times, want 1 each", *firstClosed, *secondClosed)
	}
	// A provider compiled in is left alone, not dropped.
	if _, ok := providers["in-tree"]; !ok {
		t.Error("stopping the plugins dropped a compiled-in provider")
	}
	if len(providers) != 1 {
		t.Errorf("providers left = %d, want only the compiled-in one", len(providers))
	}
}

// Removing or replacing one plugin must stop that one and leave the rest
// running.
func TestStopProviderPluginStopsOnlyTheNamedOne(t *testing.T) {
	doomed, doomedClosed := startedPluginClient(t, "doomed")
	kept, keptClosed := startedPluginClient(t, "kept")

	providers := map[string]llm.Provider{"doomed": doomed, "kept": kept}

	stopProviderPlugin(providers, "doomed")

	if *doomedClosed != 1 {
		t.Errorf("the named plugin was closed %d times, want 1", *doomedClosed)
	}
	if *keptClosed != 0 {
		t.Errorf("another plugin was closed %d times, want 0", *keptClosed)
	}
	if _, ok := providers["doomed"]; ok {
		t.Error("the named plugin is still in the provider map")
	}
	if _, ok := providers["kept"]; !ok {
		t.Error("the wrong plugin was dropped")
	}
}

// Stopping a provider that was never started, or one that is not a plugin at
// all, must not panic — both happen on an ordinary shutdown.
func TestStopProviderPluginToleratesNonPlugins(t *testing.T) {
	providers := map[string]llm.Provider{"in-tree": inTreeProvider{}}

	stopProviderPlugin(providers, "in-tree")
	if _, ok := providers["in-tree"]; ok {
		t.Error("the provider was not dropped from the map")
	}

	// An unknown name is a no-op rather than a crash.
	stopProviderPlugin(providers, "never-existed")
	stopProviderPlugins(map[string]llm.Provider{})
	stopProviderPlugins(nil)
}
