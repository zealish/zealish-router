package config

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const baseYAML = `
server:
  port: 8787
admin:
  enabled: false
auth:
  enabled: true
  api_keys:
    - %s
`

// writeConfig writes a valid configuration whose only varying field is the
// static API key, which the tests use as the reload marker.
func writeConfig(t *testing.T, path, marker string) {
	t.Helper()
	body := fmt.Appendf(nil, baseYAML, marker)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// marker returns the reload marker carried by cfg.
func marker(cfg *Config) string {
	if len(cfg.Auth.APIKeys) == 0 {
		return ""
	}
	return cfg.Auth.APIKeys[0]
}

// startWatch runs Watch until the test ends and returns the channel of configs
// it produced.
func startWatch(t *testing.T, path string) <-chan *Config {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	changes := make(chan *Config, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		if err := Watch(ctx, path, logger, func(c *Config) { changes <- c }); err != nil {
			t.Errorf("Watch: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	// Give the watcher a moment to register before the test mutates the file.
	time.Sleep(100 * time.Millisecond)
	return changes
}

func waitForConfig(t *testing.T, changes <-chan *Config) *Config {
	t.Helper()
	select {
	case cfg := <-changes:
		return cfg
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a config reload")
		return nil
	}
}

func TestWatchDeliversValidReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, "gpt-5-upstream")

	changes := startWatch(t, path)
	writeConfig(t, path, "gpt-5-turbo")

	cfg := waitForConfig(t, changes)
	if got := marker(cfg); got != "gpt-5-turbo" {
		t.Errorf("reloaded marker = %q, want gpt-5-turbo", got)
	}
}

func TestWatchRejectsInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, "gpt-5-upstream")

	changes := startWatch(t, path)

	// admin.enabled without a token: Validate must reject it.
	bad := "server:\n  port: 8787\nadmin:\n  enabled: true\n"
	if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	select {
	case cfg := <-changes:
		t.Fatalf("invalid config was delivered: %+v", cfg.Auth)
	case <-time.After(600 * time.Millisecond):
	}

	// A later valid write must still be picked up: one bad save does not
	// poison the watcher.
	writeConfig(t, path, "recovered")
	if got := marker(waitForConfig(t, changes)); got != "recovered" {
		t.Errorf("marker = %q, want recovered", got)
	}
}

func TestWatchSurvivesAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	writeConfig(t, path, "gpt-5-upstream")

	changes := startWatch(t, path)

	// Write-temp-then-rename, as editors and config managers do. This swaps
	// the inode, which is why Watch follows the directory.
	tmp := filepath.Join(dir, ".config.yaml.tmp")
	writeConfig(t, tmp, "atomically-replaced")
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if got := marker(waitForConfig(t, changes)); got != "atomically-replaced" {
		t.Errorf("marker = %q, want atomically-replaced", got)
	}
}

func TestWatchCoalescesBurstWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, "v0")

	changes := startWatch(t, path)

	for i := range 5 {
		writeConfig(t, path, "burst")
		if i < 4 {
			time.Sleep(10 * time.Millisecond)
		}
	}

	if got := marker(waitForConfig(t, changes)); got != "burst" {
		t.Errorf("marker = %q, want burst", got)
	}
	// Five rapid writes inside the settle window collapse into one reload.
	select {
	case cfg := <-changes:
		t.Fatalf("burst produced a second reload: %+v", cfg.Auth)
	case <-time.After(400 * time.Millisecond):
	}
}

func TestWatchStopsOnContextCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeConfig(t, path, "gpt-5-upstream")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		done <- Watch(ctx, path, logger, func(*Config) {})
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Watch returned %v, want nil on cancel", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Watch did not return after cancel")
	}
}

func TestWatchMissingDirectoryIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "config.yaml")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	if err := Watch(context.Background(), path, logger, func(*Config) {}); err == nil {
		t.Fatal("expected an error watching a missing directory")
	}
}
