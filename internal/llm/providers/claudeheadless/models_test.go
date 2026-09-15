package claudeheadless

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestFetchModelsFromCLIInitialization(t *testing.T) {
	withFake(t, "models")
	got, err := New(stubStore{}).FetchModels(context.Background())
	if err != nil || !reflect.DeepEqual(got, []string{"default", "opus[1m]", "new-model"}) {
		t.Fatalf("FetchModels = %v, %v", got, err)
	}
}

func TestFetchModelsRejectsInvalidCatalog(t *testing.T) {
	for _, mode := range []string{"models-empty", "models-error", "models-malformed", "badflag"} {
		t.Run(mode, func(t *testing.T) {
			withFake(t, mode)
			if _, err := New(stubStore{}).FetchModels(context.Background()); err == nil {
				t.Fatal("expected failure")
			}
		})
	}
}

func TestFetchModelsStopsUnresponsiveCLI(t *testing.T) {
	withFake(t, "models-hang")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := New(stubStore{}).FetchModels(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("query did not stop on cancellation")
	}
}

func TestFetchModelsInstalledCLI(t *testing.T) {
	if os.Getenv("OPENUAI_TEST_INSTALLED_CLAUDE") != "1" {
		t.Skip("opt-in local CLI catalog check")
	}
	got, err := New(stubStore{}).FetchModels(context.Background())
	if err != nil || len(got) == 0 {
		t.Fatalf("installed CLI models = %v, %v", got, err)
	}
	t.Logf("Installed Claude Code models: %v", got)
}
