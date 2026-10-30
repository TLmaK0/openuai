package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The MCP handler, when given, is served at /mcp for every method the
// streamable HTTP transport uses; without it the route does not exist.
func TestMCPRoute(t *testing.T) {
	var hits int
	mcp := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusTeapot)
	})
	s := New(Config{}, Handlers{MCP: mcp})
	for _, m := range []string{http.MethodPost, http.MethodGet, http.MethodDelete} {
		w := httptest.NewRecorder()
		s.echo.ServeHTTP(w, httptest.NewRequest(m, "/mcp", nil))
		if w.Code != http.StatusTeapot {
			t.Errorf("%s /mcp = %d", m, w.Code)
		}
	}
	if hits != 3 {
		t.Errorf("handler hit %d times", hits)
	}

	w := httptest.NewRecorder()
	New(Config{}, Handlers{}).echo.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if w.Code != http.StatusNotFound {
		t.Errorf("without handler: %d, want 404", w.Code)
	}
}
