package llm

import (
	"context"
	"errors"
	"testing"
)

// resetRegistry empties the package-level registry so each test starts from a
// known state.
func resetRegistry(t *testing.T) {
	t.Helper()
	registryMu.Lock()
	descriptors = map[string]Descriptor{}
	registryMu.Unlock()
}

// fakeProvider implements Provider and nothing else, so the core has to fall
// back to its defaults for every optional capability.
type fakeProvider struct {
	name   string
	models []string
}

func (f *fakeProvider) Chat(context.Context, []Message, string) (*Response, error) {
	return &Response{Content: "ok", Model: "fake"}, nil
}
func (f *fakeProvider) Name() string     { return f.name }
func (f *fakeProvider) Models() []string { return f.models }

// fullProvider implements every optional capability.
type fullProvider struct {
	fakeProvider
	ready       bool
	fetched     []string
	fetchErr    error
	secretGiven string
	loginCalls  int
}

func (f *fullProvider) Ready() bool { return f.ready }
func (f *fullProvider) SetSecret(secret string) error {
	f.secretGiven = secret
	return nil
}
func (f *fullProvider) Login() error {
	f.loginCalls++
	return nil
}
func (f *fullProvider) FetchModels(context.Context) ([]string, error) {
	return f.fetched, f.fetchErr
}

func newDescriptor(name string) Descriptor {
	return Descriptor{
		Name: name,
		New:  func(Store) Provider { return &fakeProvider{name: name} },
	}
}

func TestRegisterAndLookup(t *testing.T) {
	resetRegistry(t)
	Register(newDescriptor("beta"))
	Register(newDescriptor("alpha"))

	d, ok := Lookup("alpha")
	if !ok {
		t.Fatal("Lookup(alpha) = not found, want found")
	}
	if d.Name != "alpha" {
		t.Errorf("Lookup(alpha).Name = %q, want alpha", d.Name)
	}
	if _, ok := Lookup("missing"); ok {
		t.Error("Lookup(missing) = found, want not found")
	}
}

// Descriptors must not leak package initialisation order to the UI, which
// renders them in the order given.
func TestDescriptorsSortedByName(t *testing.T) {
	resetRegistry(t)
	Register(newDescriptor("zulu"))
	Register(newDescriptor("alpha"))
	Register(newDescriptor("mike"))

	var got []string
	for _, d := range Descriptors() {
		got = append(got, d.Name)
	}
	want := []string{"alpha", "mike", "zulu"}
	if len(got) != len(want) {
		t.Fatalf("Descriptors() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Descriptors() = %v, want %v", got, want)
		}
	}
}

func TestRegisterRejectsBadDescriptors(t *testing.T) {
	cases := map[string]Descriptor{
		"no name":        {New: func(Store) Provider { return nil }},
		"no constructor": {Name: "nameless"},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			resetRegistry(t)
			defer func() {
				if recover() == nil {
					t.Errorf("Register(%s) did not panic", name)
				}
			}()
			Register(d)
		})
	}

	t.Run("duplicate", func(t *testing.T) {
		resetRegistry(t)
		Register(newDescriptor("twice"))
		defer func() {
			if recover() == nil {
				t.Error("registering the same name twice did not panic")
			}
		}()
		Register(newDescriptor("twice"))
	})
}

func TestDefaultDescriptor(t *testing.T) {
	resetRegistry(t)
	if _, ok := DefaultDescriptor(); ok {
		t.Error("DefaultDescriptor() on an empty registry = found, want not found")
	}

	// With nothing marked, the first by name wins.
	Register(newDescriptor("zulu"))
	Register(newDescriptor("alpha"))
	d, ok := DefaultDescriptor()
	if !ok || d.Name != "alpha" {
		t.Errorf("DefaultDescriptor() = %q/%v, want alpha/true", d.Name, ok)
	}

	// An explicit mark beats alphabetical order.
	marked := newDescriptor("zzz")
	marked.Default = true
	Register(marked)
	d, ok = DefaultDescriptor()
	if !ok || d.Name != "zzz" {
		t.Errorf("DefaultDescriptor() = %q/%v, want zzz/true", d.Name, ok)
	}
}

func TestReady(t *testing.T) {
	if Ready(nil) {
		t.Error("Ready(nil) = true, want false")
	}
	// A provider that does not report readiness is assumed ready.
	if !Ready(&fakeProvider{name: "plain"}) {
		t.Error("Ready(provider without ReadinessReporter) = false, want true")
	}
	if Ready(&fullProvider{ready: false}) {
		t.Error("Ready(not-ready provider) = true, want false")
	}
	if !Ready(&fullProvider{ready: true}) {
		t.Error("Ready(ready provider) = false, want true")
	}
}

func TestAvailableModels(t *testing.T) {
	static := []string{"static-1"}

	t.Run("nil provider", func(t *testing.T) {
		if got := AvailableModels(context.Background(), nil); got != nil {
			t.Errorf("AvailableModels(nil) = %v, want nil", got)
		}
	})

	t.Run("static list when the provider cannot fetch", func(t *testing.T) {
		got := AvailableModels(context.Background(), &fakeProvider{models: static})
		if len(got) != 1 || got[0] != "static-1" {
			t.Errorf("AvailableModels() = %v, want %v", got, static)
		}
	})

	t.Run("live list wins", func(t *testing.T) {
		p := &fullProvider{ready: true, fetched: []string{"live-1", "live-2"}}
		p.models = static
		got := AvailableModels(context.Background(), p)
		if len(got) != 2 || got[0] != "live-1" {
			t.Errorf("AvailableModels() = %v, want the live list", got)
		}
	})

	t.Run("static list when the fetch fails", func(t *testing.T) {
		p := &fullProvider{ready: true, fetchErr: errors.New("network down")}
		p.models = static
		got := AvailableModels(context.Background(), p)
		if len(got) != 1 || got[0] != "static-1" {
			t.Errorf("AvailableModels() = %v, want %v", got, static)
		}
	})

	t.Run("static list when the fetch returns nothing", func(t *testing.T) {
		p := &fullProvider{ready: true, fetched: []string{}}
		p.models = static
		got := AvailableModels(context.Background(), p)
		if len(got) != 1 || got[0] != "static-1" {
			t.Errorf("AvailableModels() = %v, want %v", got, static)
		}
	})

	t.Run("no fetch attempted before the provider is ready", func(t *testing.T) {
		p := &fullProvider{ready: false, fetched: []string{"live-1"}}
		p.models = static
		got := AvailableModels(context.Background(), p)
		if len(got) != 1 || got[0] != "static-1" {
			t.Errorf("AvailableModels() = %v, want %v", got, static)
		}
	})
}

func TestSetSecretAndLogin(t *testing.T) {
	full := &fullProvider{}
	if err := SetSecret(full, "sk-test"); err != nil {
		t.Fatalf("SetSecret() = %v, want nil", err)
	}
	if full.secretGiven != "sk-test" {
		t.Errorf("secret given = %q, want sk-test", full.secretGiven)
	}
	if err := Login(full); err != nil {
		t.Fatalf("Login() = %v, want nil", err)
	}
	if full.loginCalls != 1 {
		t.Errorf("login calls = %d, want 1", full.loginCalls)
	}

	// A provider without the capability must fail rather than silently do
	// nothing, so the UI can report it.
	plain := &fakeProvider{name: "plain"}
	if err := SetSecret(plain, "sk-test"); err == nil {
		t.Error("SetSecret(provider without SecretSetter) = nil, want an error")
	}
	if err := Login(plain); err == nil {
		t.Error("Login(provider without LoginStarter) = nil, want an error")
	}
}

// Info is what the UI renders from, so every field has to survive the trip,
// readiness included.
func TestDescriptorInfo(t *testing.T) {
	d := Descriptor{
		Name:              "example",
		DisplayName:       "Example (API key)",
		Credential:        CredentialSecret,
		SecretPlaceholder: "sk-example-...",
		LoginLabel:        "unused",
	}

	info := d.Info(&fullProvider{ready: true})
	if info.Name != "example" || info.DisplayName != "Example (API key)" {
		t.Errorf("Info() names = %q/%q", info.Name, info.DisplayName)
	}
	if info.Credential != "secret" {
		t.Errorf("Info().Credential = %q, want secret", info.Credential)
	}
	if info.SecretPlaceholder != "sk-example-..." {
		t.Errorf("Info().SecretPlaceholder = %q", info.SecretPlaceholder)
	}
	if !info.Ready {
		t.Error("Info().Ready = false, want true")
	}

	// A provider that failed to build reports as not ready rather than
	// crashing the settings screen.
	if d.Info(nil).Ready {
		t.Error("Info(nil).Ready = true, want false")
	}
}
