package marketplace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const goodRegistry = `{
  "schema_version": 1,
  "servers": [
    {"name": "Echo", "description": "Echo server", "icon": "EC", "category": "Utilities",
     "command": "npx", "args": ["-y", "echo-mcp"], "auth_type": "none"},
    {"name": "Keyed", "description": "Needs a key", "category": "Dev",
     "command": "uvx", "args": ["keyed"], "env": {"KEY": "{{SECRET}}"},
     "auth_type": "api_key", "auth_label": "Key", "auth_env_var": "KEY"}
  ],
  "rules": [{"future": true}]
}`

// registryServer serves body (changeable) and counts the requests.
type registryServer struct {
	*httptest.Server
	mu     sync.Mutex
	body   string
	status int
	etag   string
	hits   atomic.Int32
}

func newRegistryServer(t *testing.T, body string) *registryServer {
	rs := &registryServer{body: body, status: http.StatusOK}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.hits.Add(1)
		rs.mu.Lock()
		defer rs.mu.Unlock()
		if rs.etag != "" {
			if r.Header.Get("If-None-Match") == rs.etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", rs.etag)
		}
		w.WriteHeader(rs.status)
		w.Write([]byte(rs.body))
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *registryServer) set(status int, body string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.status, rs.body = status, body
}

func names(l Listing) []string {
	var n []string
	for _, e := range l.Entries {
		n = append(n, e.Name)
	}
	return n
}

func TestGetLoadsFromRegistryAndCaches(t *testing.T) {
	rs := newRegistryServer(t, goodRegistry)
	dir := t.TempDir()
	s := NewStore(rs.URL, dir)

	l := s.Get(context.Background())
	if l.Source != SourceRegistry || l.Notice != "" {
		t.Fatalf("source=%s notice=%q", l.Source, l.Notice)
	}
	if got := names(l); !reflect.DeepEqual(got, []string{"Echo", "Keyed"}) {
		t.Fatalf("entries = %v", got)
	}
	if l.Entries[1].Icon != "KE" {
		t.Errorf("derived icon = %q, want KE", l.Entries[1].Icon)
	}
	if _, err := os.Stat(filepath.Join(dir, cacheFileName)); err != nil {
		t.Fatalf("cache not written: %v", err)
	}

	// Within the TTL neither this store nor a new one (next app start) asks
	// the registry again.
	s.Get(context.Background())
	l2 := NewStore(rs.URL, dir).Get(context.Background())
	if rs.hits.Load() != 1 {
		t.Fatalf("registry hit %d times, want 1", rs.hits.Load())
	}
	if l2.Source != SourceRegistry || len(l2.Entries) != 2 {
		t.Fatalf("from cache: source=%s entries=%d", l2.Source, len(l2.Entries))
	}
}

func TestExpiredCacheIsRefetched(t *testing.T) {
	rs := newRegistryServer(t, goodRegistry)
	dir := t.TempDir()
	now := time.Now()
	s := NewStore(rs.URL, dir)
	s.now = func() time.Time { return now }
	s.Get(context.Background())

	now = now.Add(CacheTTL + time.Minute)
	s.Get(context.Background())
	if rs.hits.Load() != 2 {
		t.Fatalf("registry hit %d times, want 2", rs.hits.Load())
	}
}

func TestRefreshIgnoresTTL(t *testing.T) {
	rs := newRegistryServer(t, goodRegistry)
	s := NewStore(rs.URL, t.TempDir())
	s.Get(context.Background())
	rs.set(http.StatusOK, strings.Replace(goodRegistry, `"Echo"`, `"Echo 2"`, 1))
	l := s.Refresh(context.Background())
	if rs.hits.Load() != 2 || l.Entries[0].Name != "Echo 2" {
		t.Fatalf("hits=%d entries=%v", rs.hits.Load(), names(l))
	}
}

func TestNotModifiedKeepsCachedCatalog(t *testing.T) {
	rs := newRegistryServer(t, goodRegistry)
	rs.etag = `"v1"`
	s := NewStore(rs.URL, t.TempDir())
	s.Get(context.Background())
	l := s.Refresh(context.Background())
	if l.Source != SourceRegistry || len(l.Entries) != 2 {
		t.Fatalf("after 304: source=%s entries=%d notice=%q", l.Source, len(l.Entries), l.Notice)
	}
}

func TestUnreachableFallsBackToCacheThenBuiltin(t *testing.T) {
	rs := newRegistryServer(t, goodRegistry)
	dir := t.TempDir()
	NewStore(rs.URL, dir).Get(context.Background())
	url := rs.URL
	rs.Close()

	l := NewStore(url, dir).Refresh(context.Background())
	if l.Source != SourceCache || len(l.Entries) != 2 {
		t.Fatalf("source=%s entries=%d", l.Source, len(l.Entries))
	}
	if !strings.Contains(l.Notice, "unreachable") || !strings.Contains(l.Notice, "downloaded on") {
		t.Errorf("notice = %q", l.Notice)
	}

	l = NewStore(url, t.TempDir()).Get(context.Background())
	if l.Source != SourceEmbedded || len(l.Entries) != len(Catalog) {
		t.Fatalf("source=%s entries=%d", l.Source, len(l.Entries))
	}
	if !strings.Contains(l.Notice, "built-in catalog") {
		t.Errorf("notice = %q", l.Notice)
	}
}

func TestCacheOfAnotherURLIsNotUsed(t *testing.T) {
	rs := newRegistryServer(t, goodRegistry)
	dir := t.TempDir()
	NewStore(rs.URL, dir).Get(context.Background())
	l := NewStore("http://127.0.0.1:1/other.json", dir).Get(context.Background())
	if l.Source != SourceEmbedded {
		t.Fatalf("source = %s, want embedded", l.Source)
	}
}

func TestBadResponsesFallBack(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		want   string
	}{
		"http error":    {http.StatusNotFound, "nope", "HTTP 404"},
		"not json":      {http.StatusOK, "<html>", "not valid JSON"},
		"newer schema":  {http.StatusOK, `{"schema_version": 2, "servers": []}`, "schema_version 2"},
		"no valid item": {http.StatusOK, `{"schema_version": 1, "servers": [{"name": ""}]}`, "no valid entry"},
		"too big":       {http.StatusOK, strings.Repeat(" ", maxRegistrySize+10), "larger than"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rs := newRegistryServer(t, c.body)
			rs.set(c.status, c.body)
			dir := t.TempDir()
			l := NewStore(rs.URL, dir).Get(context.Background())
			if l.Source != SourceEmbedded || !strings.Contains(l.Notice, c.want) {
				t.Fatalf("source=%s notice=%q, want embedded with %q", l.Source, l.Notice, c.want)
			}
			if _, err := os.Stat(filepath.Join(dir, cacheFileName)); !os.IsNotExist(err) {
				t.Errorf("a bad registry must not be cached (stat err %v)", err)
			}
		})
	}
}

func TestInvalidEntriesAreSkippedWithWarnings(t *testing.T) {
	doc := `{"schema_version": 1, "servers": [
	  {"name": "Good", "description": "ok", "category": "Dev", "command": "npx"},
	  {"name": "good", "description": "duplicate", "category": "Dev", "command": "npx"},
	  {"name": "NoDesc", "category": "Dev", "command": "npx"},
	  {"name": "BadCat", "description": "x", "category": "Games", "command": "npx"},
	  {"name": "PathCmd", "description": "x", "category": "Dev", "command": "/tmp/evil"},
	  {"name": "NoCmd", "description": "x", "category": "Dev"},
	  {"name": "BadAuth", "description": "x", "category": "Dev", "command": "npx", "auth_type": "magic"},
	  {"name": "KeyNoSecret", "description": "x", "category": "Dev", "command": "npx", "auth_type": "api_key", "auth_label": "k"},
	  {"name": "Typo", "description": "x", "category": "Dev", "command": "npx", "argz": []},
	  {"name": "WrongType", "description": "x", "category": "Dev", "command": 5},
	  "just a string"
	]}`
	entries, warnings, err := ParseRegistry([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name != "Good" || entries[0].AuthType != AuthNone {
		t.Fatalf("entries = %+v", entries)
	}
	if len(warnings) != 10 {
		t.Fatalf("got %d warnings, want 10: %v", len(warnings), warnings)
	}
	for _, w := range warnings {
		if !strings.Contains(w, "skipped") {
			t.Errorf("warning without 'skipped': %q", w)
		}
	}

	// Warnings reach the listing shown to the user.
	rs := newRegistryServer(t, doc)
	l := NewStore(rs.URL, t.TempDir()).Get(context.Background())
	if l.Source != SourceRegistry || len(l.Warnings) != 10 {
		t.Fatalf("source=%s warnings=%d", l.Source, len(l.Warnings))
	}
}

func TestLookupAndInstallFromRegistry(t *testing.T) {
	rs := newRegistryServer(t, goodRegistry)
	s := NewStore(rs.URL, t.TempDir())
	if e := s.Lookup(context.Background(), "GitHub"); e != nil {
		t.Fatalf("GitHub is built-in only, but Lookup found it in the registry")
	}
	e := s.Lookup(context.Background(), "Keyed")
	if e == nil {
		t.Fatal("Keyed not found")
	}
	cfg := Install(*e, "s3cret")
	if cfg.Command != "uvx" || cfg.Env["KEY"] != "s3cret" || !cfg.AutoStart {
		t.Fatalf("install = %+v", cfg)
	}
}

func TestDefaultURL(t *testing.T) {
	if got := NewStore("  ", "").URL(); got != DefaultRegistryURL {
		t.Fatalf("URL = %q", got)
	}
	if !strings.HasPrefix(DefaultRegistryURL, "https://raw.githubusercontent.com/TLmaK0/openuai/master/") {
		t.Fatalf("unexpected default registry %q", DefaultRegistryURL)
	}
}

// The registry file in this repository must be valid and hold the same
// entries as the built-in fallback.
func TestRepositoryRegistryMatchesBuiltin(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "registry", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	entries, warnings, err := ParseRegistry(data)
	if err != nil || len(warnings) > 0 {
		t.Fatalf("registry/marketplace.json: err=%v warnings=%v", err, warnings)
	}
	builtin := Builtin()
	for i := range builtin {
		if err := normalize(&builtin[i]); err != nil {
			t.Fatalf("built-in entry %q is invalid: %v", builtin[i].Name, err)
		}
	}
	a, _ := json.Marshal(entries)
	b, _ := json.Marshal(builtin)
	if string(a) != string(b) {
		t.Fatalf("registry/marketplace.json and the built-in Catalog differ:\nregistry: %s\nbuiltin:  %s", a, b)
	}
}
