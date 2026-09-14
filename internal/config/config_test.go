package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeConfigFile writes a config fixture. Every fixture disables the admin
// API: Default() enables it, and Validate then demands a token that is beside
// the point for these tests.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	full := "admin:\n  enabled: false\n" + body
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestBreakerDefaultsWhenAbsent(t *testing.T) {
	path := writeConfigFile(t, "database:\n  path: data/router.db\n")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Router.Breaker.FailureThreshold != 5 {
		t.Errorf("failure_threshold = %d, want the built-in 5", cfg.Router.Breaker.FailureThreshold)
	}
	if cfg.Router.Breaker.Cooldown != 30*time.Second {
		t.Errorf("cooldown = %s, want the built-in 30s", cfg.Router.Breaker.Cooldown)
	}
}

func TestBreakerReadFromYAML(t *testing.T) {
	path := writeConfigFile(t, `database:
  path: data/router.db
router:
  breaker:
    failure_threshold: 3
    cooldown: 45s
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Router.Breaker.FailureThreshold != 3 {
		t.Errorf("failure_threshold = %d, want 3", cfg.Router.Breaker.FailureThreshold)
	}
	if cfg.Router.Breaker.Cooldown != 45*time.Second {
		t.Errorf("cooldown = %s, want 45s", cfg.Router.Breaker.Cooldown)
	}
}

func TestBreakerZeroThresholdDisablesRatherThanDefaulting(t *testing.T) {
	// 0 is a meaningful value, not "unset": it must survive the default merge.
	path := writeConfigFile(t, `database:
  path: data/router.db
router:
  breaker:
    failure_threshold: 0
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Router.Breaker.FailureThreshold != 0 {
		t.Errorf("failure_threshold = %d, want 0 to disable the breaker",
			cfg.Router.Breaker.FailureThreshold)
	}
}

func TestBreakerRejectsNegativeValues(t *testing.T) {
	for _, body := range []string{
		"database:\n  path: d\nrouter:\n  breaker:\n    failure_threshold: -1\n",
		"database:\n  path: d\nrouter:\n  breaker:\n    cooldown: -5s\n",
	} {
		if _, err := Load(writeConfigFile(t, body)); err == nil {
			t.Errorf("Load accepted a negative breaker value:\n%s", body)
		}
	}
}

// The shipped example is the documentation users copy; it must stay loadable.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("load config.example.yaml: %v", err)
	}
	if cfg.Router.Breaker.FailureThreshold != 5 {
		t.Errorf("example failure_threshold = %d, want 5", cfg.Router.Breaker.FailureThreshold)
	}
	if cfg.Router.Breaker.Cooldown != 30*time.Second {
		t.Errorf("example cooldown = %s, want 30s", cfg.Router.Breaker.Cooldown)
	}
}
