package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// The registry is a JSON file in a GitHub repository. The community adds
// entries through pull requests; OpenUAI downloads the file from
// raw.githubusercontent.com, so no infrastructure is involved.
//
// What the registry holds: schema version 1 holds MCP servers (connectors),
// under "servers". Rules and prompt templates, which AGENTS.md also lists for
// the marketplace, have no install path in the app yet; when they do they get
// their own top-level keys. Unknown top-level keys are ignored, so adding them
// does not break older versions of the app.
//
//	{
//	  "schema_version": 1,
//	  "servers": [ { "name": "GitHub", "command": "npx", ... } ]
//	}

// DefaultRegistryURL is where the catalog is fetched from unless the
// configuration (marketplace_url) or OPENUAI_MARKETPLACE_URL says otherwise.
const DefaultRegistryURL = "https://raw.githubusercontent.com/TLmaK0/openuai/master/registry/marketplace.json"

// SchemaVersion is the registry format this version of the app understands.
// A registry with another schema_version is rejected as a whole and the app
// falls back to its cache or the built-in catalog.
const SchemaVersion = 1

const (
	// CacheTTL is how long a downloaded catalog is used before the registry
	// is asked again. Refresh skips it.
	CacheTTL = 24 * time.Hour
	// maxRegistrySize bounds the download: the catalog is a small text file.
	maxRegistrySize = 1 << 20
	fetchTimeout    = 10 * time.Second
	cacheFileName   = "registry.json"
)

// Source says where the catalog being shown came from.
type Source string

const (
	// SourceRegistry: downloaded from the registry, now or within CacheTTL.
	SourceRegistry Source = "registry"
	// SourceCache: the registry could not be reached; showing the last
	// catalog downloaded, however old it is.
	SourceCache Source = "cache"
	// SourceEmbedded: neither the registry nor a cache was usable; showing
	// the catalog built into the app.
	SourceEmbedded Source = "embedded"
)

// Listing is the catalog as shown to the user, with where it came from.
type Listing struct {
	Entries []CatalogEntry `json:"entries"`
	Source  Source         `json:"source"`
	URL     string         `json:"url"`
	// FetchedAt is when the catalog was downloaded (Unix seconds); 0 for
	// the built-in catalog.
	FetchedAt int64 `json:"fetched_at"`
	// Notice explains a fallback (registry unreachable, invalid...). Empty
	// when the catalog comes from the registry.
	Notice string `json:"notice,omitempty"`
	// Warnings lists the registry entries that were skipped and why.
	Warnings []string `json:"warnings,omitempty"`
}

// registryFile is the registry document. Servers are decoded one by one so
// that a malformed entry is skipped instead of discarding the whole file.
type registryFile struct {
	SchemaVersion int               `json:"schema_version"`
	Servers       []json.RawMessage `json:"servers"`
}

// cacheFile is what is kept on disk: the registry body exactly as served,
// re-validated on every load.
type cacheFile struct {
	URL       string          `json:"url"`
	FetchedAt int64           `json:"fetched_at"`
	ETag      string          `json:"etag,omitempty"`
	Body      json.RawMessage `json:"body"`
}

// ParseRegistry decodes and validates a registry document. Invalid entries
// are skipped and reported in warnings; an error means the document as a
// whole is unusable (not JSON, wrong schema version, no valid entry).
func ParseRegistry(data []byte) (entries []CatalogEntry, warnings []string, err error) {
	var doc registryFile
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, nil, fmt.Errorf("registry is not valid JSON: %w", err)
	}
	if doc.SchemaVersion != SchemaVersion {
		return nil, nil, fmt.Errorf("registry schema_version %d is not supported (this version of OpenUAI reads %d)", doc.SchemaVersion, SchemaVersion)
	}
	seen := map[string]bool{}
	for i, raw := range doc.Servers {
		var e CatalogEntry
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&e); err != nil {
			warnings = append(warnings, fmt.Sprintf("entry %d skipped: %v", i+1, err))
			continue
		}
		if err := normalize(&e); err != nil {
			warnings = append(warnings, fmt.Sprintf("entry %d (%q) skipped: %v", i+1, e.Name, err))
			continue
		}
		key := strings.ToLower(e.Name)
		if seen[key] {
			warnings = append(warnings, fmt.Sprintf("entry %d (%q) skipped: duplicate name", i+1, e.Name))
			continue
		}
		seen[key] = true
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, warnings, errors.New("registry has no valid entry")
	}
	return entries, warnings, nil
}

var validCategories = map[Category]bool{CatOffice: true, CatDev: true, CatSearch: true, CatUtilities: true}

// normalize validates an entry and fills in the optional fields.
func normalize(e *CatalogEntry) error {
	e.Name = strings.TrimSpace(e.Name)
	e.Description = strings.TrimSpace(e.Description)
	switch {
	case e.Name == "":
		return errors.New("name is required")
	case utf8.RuneCountInString(e.Name) > 64:
		return errors.New("name is longer than 64 characters")
	case e.Description == "":
		return errors.New("description is required")
	case !validCategories[e.Category]:
		return fmt.Errorf("unknown category %q", e.Category)
	case e.Command == "":
		return errors.New("command is required")
	case strings.ContainsAny(e.Command, "/\\ \t\n"):
		// A program name looked up on PATH (npx, uvx, docker...), never a
		// path to something a registry entry could have put on disk.
		return fmt.Errorf("command %q must be a bare program name", e.Command)
	}
	if e.AuthType == "" {
		e.AuthType = AuthNone
	}
	switch e.AuthType {
	case AuthNone, AuthOAuth:
	case AuthAPIKey:
		if strings.TrimSpace(e.AuthLabel) == "" {
			return errors.New("auth_type api_key needs an auth_label")
		}
		if !usesSecret(*e) {
			return errors.New("auth_type api_key needs {{SECRET}} in env or args")
		}
	default:
		return fmt.Errorf("unknown auth_type %q", e.AuthType)
	}
	for k := range e.Env {
		if strings.TrimSpace(k) == "" || strings.ContainsAny(k, "= ") {
			return fmt.Errorf("invalid env var name %q", k)
		}
	}
	e.Icon = strings.TrimSpace(e.Icon)
	if e.Icon == "" {
		e.Icon = strings.ToUpper(e.Name)
	}
	if r := []rune(e.Icon); len(r) > 3 {
		e.Icon = string(r[:2])
	}
	return nil
}

func usesSecret(e CatalogEntry) bool {
	for _, v := range e.Env {
		if strings.Contains(v, "{{SECRET}}") {
			return true
		}
	}
	for _, a := range e.Args {
		if strings.Contains(a, "{{SECRET}}") {
			return true
		}
	}
	return false
}

// Store loads the catalog from the registry, keeps a copy on disk and falls
// back to that copy, then to the built-in catalog, when the registry cannot
// be used. It is safe for concurrent use.
type Store struct {
	url      string
	cacheDir string
	client   *http.Client
	now      func() time.Time
	ttl      time.Duration

	mu      sync.Mutex
	current *Listing
}

// NewStore returns a store for the registry at url (DefaultRegistryURL when
// empty) that caches into cacheDir.
func NewStore(url, cacheDir string) *Store {
	if strings.TrimSpace(url) == "" {
		url = DefaultRegistryURL
	}
	return &Store{
		url:      strings.TrimSpace(url),
		cacheDir: cacheDir,
		client:   &http.Client{Timeout: fetchTimeout},
		now:      time.Now,
		ttl:      CacheTTL,
	}
}

// URL returns the registry address the store reads.
func (s *Store) URL() string { return s.url }

// Get returns the catalog, asking the registry only when the copy in memory
// or on disk is older than CacheTTL.
func (s *Store) Get(ctx context.Context) Listing {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil && s.current.Source == SourceRegistry && s.fresh(s.current.FetchedAt) {
		return copyListing(*s.current)
	}
	cache, _ := s.readCache()
	// After a fallback (registry unreachable or invalid) the registry is
	// asked again instead of presenting the cached copy as current: the
	// notice stays until the registry answers.
	fellBack := s.current != nil && s.current.Source != SourceRegistry
	if !fellBack && cache != nil && s.fresh(cache.FetchedAt) {
		if entries, warnings, err := ParseRegistry(cache.Body); err == nil {
			return s.set(Listing{Entries: entries, Source: SourceRegistry, URL: s.url, FetchedAt: cache.FetchedAt, Warnings: warnings})
		}
	}
	return s.fetchLocked(ctx, cache)
}

// Refresh asks the registry now, whatever the age of the cache.
func (s *Store) Refresh(ctx context.Context) Listing {
	s.mu.Lock()
	defer s.mu.Unlock()
	cache, _ := s.readCache()
	return s.fetchLocked(ctx, cache)
}

// Lookup returns the entry called name in the catalog last shown, loading the
// catalog first if nothing has been shown yet. Nil when there is none.
func (s *Store) Lookup(ctx context.Context, name string) *CatalogEntry {
	s.mu.Lock()
	cur := s.current
	s.mu.Unlock()
	var l Listing
	if cur != nil {
		l = *cur
	} else {
		l = s.Get(ctx)
	}
	for i := range l.Entries {
		if l.Entries[i].Name == name {
			e := l.Entries[i]
			return &e
		}
	}
	return nil
}

func (s *Store) fresh(fetchedAt int64) bool {
	age := s.now().Sub(time.Unix(fetchedAt, 0))
	return age >= 0 && age < s.ttl
}

// fetchLocked downloads the registry and, when that fails, falls back to the
// cache (any age) and then to the built-in catalog. s.mu must be held.
func (s *Store) fetchLocked(ctx context.Context, cache *cacheFile) Listing {
	body, etag, notModified, err := s.download(ctx, cache)
	if err == nil && notModified {
		body = cache.Body
		if etag == "" {
			etag = cache.ETag
		}
	}
	if err == nil {
		entries, warnings, perr := ParseRegistry(body)
		if perr == nil {
			now := s.now().Unix()
			s.writeCache(cacheFile{URL: s.url, FetchedAt: now, ETag: etag, Body: body})
			return s.set(Listing{Entries: entries, Source: SourceRegistry, URL: s.url, FetchedAt: now, Warnings: warnings})
		}
		err = perr
	}
	reason := err.Error()
	if cache != nil {
		if entries, warnings, perr := ParseRegistry(cache.Body); perr == nil {
			when := time.Unix(cache.FetchedAt, 0).Format("2006-01-02 15:04")
			return s.set(Listing{
				Entries: entries, Source: SourceCache, URL: s.url, FetchedAt: cache.FetchedAt, Warnings: warnings,
				Notice: fmt.Sprintf("Could not load the marketplace registry (%s). Showing the catalog downloaded on %s.", reason, when),
			})
		}
	}
	return s.set(Listing{
		Entries: Builtin(), Source: SourceEmbedded, URL: s.url,
		Notice: fmt.Sprintf("Could not load the marketplace registry (%s). Showing the built-in catalog.", reason),
	})
}

// download fetches the registry. With a cached copy of the same URL it sends
// If-None-Match, and a 304 comes back as notModified.
func (s *Store) download(ctx context.Context, cache *cacheFile) (body []byte, etag string, notModified bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, "", false, fmt.Errorf("invalid registry URL: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if cache != nil && cache.ETag != "" {
		req.Header.Set("If-None-Match", cache.ETag)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, "", false, fmt.Errorf("registry unreachable: %v", err)
	}
	defer resp.Body.Close()
	etag = resp.Header.Get("ETag")
	if resp.StatusCode == http.StatusNotModified && cache != nil {
		return nil, etag, true, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, "", false, fmt.Errorf("registry answered HTTP %d", resp.StatusCode)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, maxRegistrySize+1))
	if err != nil {
		return nil, "", false, fmt.Errorf("reading registry: %v", err)
	}
	if len(body) > maxRegistrySize {
		return nil, "", false, fmt.Errorf("registry is larger than %d bytes", maxRegistrySize)
	}
	return body, etag, false, nil
}

func (s *Store) set(l Listing) Listing {
	s.current = &l
	return copyListing(l)
}

func copyListing(l Listing) Listing {
	l.Entries = append([]CatalogEntry(nil), l.Entries...)
	l.Warnings = append([]string(nil), l.Warnings...)
	return l
}

func (s *Store) cachePath() string {
	if s.cacheDir == "" {
		return ""
	}
	return filepath.Join(s.cacheDir, cacheFileName)
}

// readCache returns the cached registry, or nil when there is none or it was
// downloaded from another URL.
func (s *Store) readCache() (*cacheFile, error) {
	p := s.cachePath()
	if p == "" {
		return nil, nil
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c cacheFile
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.URL != s.url || len(c.Body) == 0 {
		return nil, nil
	}
	return &c, nil
}

// writeCache stores the registry atomically. A failure only costs the cache.
func (s *Store) writeCache(c cacheFile) {
	p := s.cachePath()
	if p == "" {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	if err := os.MkdirAll(s.cacheDir, 0o700); err != nil {
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, p); err != nil {
		os.Remove(tmp)
	}
}
