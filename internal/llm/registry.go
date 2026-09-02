package llm

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// CredentialKind says how a provider is authenticated, so that callers can
// ask for the right credential without knowing which provider they are
// talking to.
type CredentialKind string

const (
	// CredentialSecret: the user supplies a secret string (an API key).
	CredentialSecret CredentialKind = "secret"
	// CredentialLogin: the user authenticates interactively, in a browser.
	CredentialLogin CredentialKind = "login"
)

// Store is the settings slot a provider is given for its own credentials and
// state. The core hands each provider a Store keyed by the provider's name,
// which is why the core declares no provider-specific configuration fields.
// Set persists immediately.
type Store interface {
	Get(key string) string
	Set(key, value string) error
}

// Descriptor is everything the core needs to know about a provider: how to
// build it, what to call it on screen, and how it is authenticated. Providers
// register one from an init(), so no core file names a provider.
type Descriptor struct {
	// Name is the stable identifier persisted in the configuration.
	Name string
	// DisplayName is shown to the user in place of Name.
	DisplayName string
	// Credential says which credential this provider needs.
	Credential CredentialKind
	// SecretPlaceholder hints at the expected shape of a CredentialSecret
	// (for example "sk-ant-..."). Unused for CredentialLogin.
	SecretPlaceholder string
	// LoginLabel is the call to action for a CredentialLogin provider (for
	// example "Login with ChatGPT"). Unused for CredentialSecret.
	LoginLabel string
	// DefaultModel is the model selected when this provider is first chosen.
	DefaultModel string
	// Default marks the provider a fresh installation starts on. At most one
	// registered provider sets it.
	Default bool
	// New builds the provider. It is called once per Store.
	New func(store Store) Provider
}

// The interfaces below are optional: the core probes for them and degrades
// gracefully when a provider does not implement one.

// ReadinessReporter reports whether the provider holds the credentials it
// needs to serve a request. A provider that does not implement it is assumed
// to be always ready.
type ReadinessReporter interface {
	Ready() bool
}

// SecretSetter accepts the secret of a CredentialSecret provider.
type SecretSetter interface {
	SetSecret(secret string) error
}

// LoginStarter runs the interactive login of a CredentialLogin provider.
type LoginStarter interface {
	Login() error
}

// ModelFetcher lists the models actually available to the current account.
// Providers whose model list is gated server-side implement it; the core falls
// back to Models() when it is absent or fails.
type ModelFetcher interface {
	FetchModels(ctx context.Context) ([]string, error)
}

var (
	registryMu  sync.RWMutex
	descriptors = map[string]Descriptor{}
)

// Register makes a provider available to the core. It is meant to be called
// from a provider package's init(). Registering the same name twice, or a
// descriptor without a Name or a New, is a programming error and panics.
func Register(d Descriptor) {
	if d.Name == "" {
		panic("llm: provider registered without a name")
	}
	if d.New == nil {
		panic("llm: provider " + d.Name + " registered without a constructor")
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := descriptors[d.Name]; dup {
		panic("llm: provider registered twice: " + d.Name)
	}
	descriptors[d.Name] = d
}

// RegisterDynamic registers a provider discovered at runtime rather than
// compiled in, so a bad one is reported instead of crashing the process.
func RegisterDynamic(d Descriptor) error {
	if d.Name == "" {
		return fmt.Errorf("provider registered without a name")
	}
	if d.New == nil {
		return fmt.Errorf("provider %q registered without a constructor", d.Name)
	}

	registryMu.Lock()
	defer registryMu.Unlock()
	if _, dup := descriptors[d.Name]; dup {
		return fmt.Errorf("a provider named %q is already registered", d.Name)
	}
	descriptors[d.Name] = d
	return nil
}

// Unregister removes a dynamically registered provider. Unregistering an
// unknown name does nothing.
func Unregister(name string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	delete(descriptors, name)
}

// Descriptors returns every registered provider, ordered by name so the order
// does not depend on package initialisation.
func Descriptors() []Descriptor {
	registryMu.RLock()
	defer registryMu.RUnlock()

	out := make([]Descriptor, 0, len(descriptors))
	for _, d := range descriptors {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// DefaultDescriptor returns the provider a fresh installation starts on: the
// one marked Default, or the first by name when none is. It reports false
// when nothing is registered.
func DefaultDescriptor() (Descriptor, bool) {
	all := Descriptors()
	for _, d := range all {
		if d.Default {
			return d, true
		}
	}
	if len(all) > 0 {
		return all[0], true
	}
	return Descriptor{}, false
}

// Lookup returns the descriptor registered under name.
func Lookup(name string) (Descriptor, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := descriptors[name]
	return d, ok
}

// ProviderSummary names a registered provider for a list. It deliberately
// carries no readiness: asking a provider whether it is ready can be
// expensive — one that runs as a separate process has to be started first —
// and a list of providers has no use for it.
type ProviderSummary struct {
	Name        string `json:"name"`
	DisplayName string `json:"display_name"`
}

// Summary names d for a list of providers, without asking it anything.
func (d Descriptor) Summary() ProviderSummary {
	display := d.DisplayName
	if display == "" {
		display = d.Name
	}
	return ProviderSummary{Name: d.Name, DisplayName: display}
}

// ProviderInfo is the view of a registered provider handed to the UI. It
// carries what the UI needs in order to render a provider it has never heard
// of: a label, which credential widget to draw, and whether the provider can
// already serve requests.
type ProviderInfo struct {
	Name              string `json:"name"`
	DisplayName       string `json:"display_name"`
	Credential        string `json:"credential"`
	SecretPlaceholder string `json:"secret_placeholder,omitempty"`
	LoginLabel        string `json:"login_label,omitempty"`
	Ready             bool   `json:"ready"`
}

// Info describes a built provider for the UI. p may be nil, in which case the
// provider reports as not ready.
func (d Descriptor) Info(p Provider) ProviderInfo {
	return ProviderInfo{
		Name:              d.Name,
		DisplayName:       d.DisplayName,
		Credential:        string(d.Credential),
		SecretPlaceholder: d.SecretPlaceholder,
		LoginLabel:        d.LoginLabel,
		Ready:             Ready(p),
	}
}

// Ready reports whether p can serve a request. Providers that do not
// implement ReadinessReporter are assumed ready; a nil provider is not.
func Ready(p Provider) bool {
	if p == nil {
		return false
	}
	if r, ok := p.(ReadinessReporter); ok {
		return r.Ready()
	}
	return true
}

// AvailableModels returns the models p offers, preferring the live list when
// the provider can fetch one and falling back to its static list otherwise.
func AvailableModels(ctx context.Context, p Provider) []string {
	if p == nil {
		return nil
	}
	if f, ok := p.(ModelFetcher); ok && Ready(p) {
		if models, err := f.FetchModels(ctx); err == nil && len(models) > 0 {
			return models
		}
	}
	return p.Models()
}

// SetSecret hands secret to p. It fails when p takes no secret, which is the
// case for providers authenticated interactively.
func SetSecret(p Provider, secret string) error {
	s, ok := p.(SecretSetter)
	if !ok {
		return fmt.Errorf("provider %q takes no secret", name(p))
	}
	return s.SetSecret(secret)
}

// Login runs p's interactive login. It fails when p has none.
func Login(p Provider) error {
	l, ok := p.(LoginStarter)
	if !ok {
		return fmt.Errorf("provider %q has no interactive login", name(p))
	}
	return l.Login()
}

func name(p Provider) string {
	if p == nil {
		return ""
	}
	return p.Name()
}
