package claude

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"openuai/internal/llm"
)

type modelTransport func(*http.Request) (*http.Response, error)

func (f modelTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func modelResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestFetchModelsPaginatesWithCredentials(t *testing.T) {
	calls := 0
	p := &ClaudeProvider{apiKey: "test-key", httpClient: &http.Client{Transport: modelTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "GET" || r.URL.Host != "api.anthropic.com" || r.URL.Path != "/v1/models" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
		}
		if r.Header.Get("x-api-key") != "test-key" || r.Header.Get("anthropic-version") != anthropicAPIVersion {
			t.Error("missing authentication/version headers")
		}
		if calls == 1 {
			if r.URL.Query().Get("after_id") != "" {
				t.Error("first request has a cursor")
			}
			return modelResponse(200, `{"data":[{"id":"model-a"}],"has_more":true,"last_id":"cursor/+"}`), nil
		}
		if r.URL.Query().Get("after_id") != "cursor/+" {
			t.Error("pagination cursor not preserved")
		}
		return modelResponse(200, `{"data":[{"id":"model-a"},{"id":"model-b"},{"id":""}],"has_more":false}`), nil
	})}}
	got, err := p.FetchModels(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"model-a", "model-b"}) || calls != 2 {
		t.Fatalf("FetchModels = %v, %v; calls=%d", got, err, calls)
	}
}

func TestModelQueryFailuresUseFallback(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unauthorized", 401, `secret must not be exposed`},
		{"malformed", 200, `{`},
		{"empty", 200, `{"data":[]}`},
		{"missing cursor", 200, `{"data":[{"id":"a"}],"has_more":true}`},
		{"repeated cursor", 200, `{"data":[{"id":"a"}],"has_more":true,"last_id":"a"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			p := &ClaudeProvider{apiKey: "test-key", httpClient: &http.Client{Transport: modelTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 4 {
					t.Fatal("pagination did not stop")
				}
				return modelResponse(tc.status, tc.body), nil
			})}}
			if _, err := p.FetchModels(context.Background()); err == nil {
				t.Fatal("expected query failure")
			}
			if got := llm.AvailableModels(context.Background(), p); !reflect.DeepEqual(got, p.Models()) {
				t.Fatalf("fallback = %v", got)
			}
		})
	}
}

func TestFetchModelsHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &ClaudeProvider{apiKey: "test-key", httpClient: &http.Client{Transport: modelTransport(func(r *http.Request) (*http.Response, error) {
		return nil, r.Context().Err()
	})}}
	if _, err := p.FetchModels(ctx); err == nil {
		t.Fatal("canceled query succeeded")
	}
}
