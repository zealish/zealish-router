// Package config owns loading and validating the YAML configuration.
// It has no dependency on any other internal package.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the fully parsed application configuration. It covers process
// level concerns only — listener, database, auth and admin. Providers and
// model aliases live in the database and are managed through the admin API.
type Config struct {
	Server   Server   `yaml:"server"`
	Database Database `yaml:"database"`
	Auth     Auth     `yaml:"auth"`
	Usage    Usage    `yaml:"usage"`
	Admin    Admin    `yaml:"admin"`
	Router   Router   `yaml:"router"`
}

// Router configures the routing engine's resilience policy.
type Router struct {
	Breaker     Breaker     `yaml:"breaker"`
	HealthCheck HealthCheck `yaml:"health_check"`
}

// Breaker is the default circuit breaker policy. Individual providers may
// override it; these values apply to every provider that does not.
type Breaker struct {
	// FailureThreshold is the number of consecutive retryable failures that
	// takes a provider out of rotation. 0 disables the breaker entirely.
	FailureThreshold int `yaml:"failure_threshold"`
	// Cooldown is how long a tripped provider is skipped before one probe
	// request is allowed through.
	Cooldown time.Duration `yaml:"cooldown"`
}

// HealthCheck configures the background provider liveness probe. Providers
// that fail a probe are skipped by the router until they pass one again.
type HealthCheck struct {
	// Enabled turns the background pinger on.
	Enabled bool `yaml:"enabled"`
	// Interval is how often every provider is pinged.
	Interval time.Duration `yaml:"interval"`
	// Timeout bounds a single ping.
	Timeout time.Duration `yaml:"timeout"`
}

// Server holds HTTP listener settings.
type Server struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	// MaxBodyBytes caps the size of a request body. 0 disables the limit.
	MaxBodyBytes int64 `yaml:"max_body_bytes"`
}

// Address returns the host:port listen address.
func (s Server) Address() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

// Database holds persistence settings.
type Database struct {
	Path string `yaml:"path"`
}

// Auth holds API key authentication settings.
type Auth struct {
	Enabled bool     `yaml:"enabled"`
	APIKeys []string `yaml:"api_keys"`
}

// Usage configures the durable request log.
type Usage struct {
	// RetentionDays deletes usage events older than this many days. 0 keeps
	// every event forever, which is the pre-1.1 behaviour.
	RetentionDays int `yaml:"retention_days"`
}

// Admin configures the internal REST API used by the dashboard.
type Admin struct {
	Enabled bool     `yaml:"enabled"`
	Token   string   `yaml:"token"`
	Origins []string `yaml:"cors_origins"`
}

// Default returns a configuration with safe built-in values.
func Default() *Config {
	return &Config{
		Server: Server{
			Host:            "0.0.0.0",
			Port:            8787,
			ReadTimeout:     30 * time.Second,
			WriteTimeout:    0,
			ShutdownTimeout: 15 * time.Second,
			MaxBodyBytes:    4 << 20,
		},
		Database: Database{Path: "data/router.db"},
		Auth:     Auth{Enabled: true},
		Admin:    Admin{Enabled: true, Origins: []string{"http://localhost:3000"}},
		Router: Router{
			Breaker:     Breaker{FailureThreshold: 5, Cooldown: 30 * time.Second},
			HealthCheck: HealthCheck{Enabled: true, Interval: 30 * time.Second, Timeout: 5 * time.Second},
		},
	}
}

// Load reads and validates the configuration file at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: read %s: %w", path, err)
	}

	cfg := Default()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Validate checks structural invariants of the configuration.
func (c *Config) Validate() error {
	if c.Server.Port <= 0 || c.Server.Port > 65535 {
		return fmt.Errorf("config: invalid server.port %d", c.Server.Port)
	}
	if c.Server.MaxBodyBytes < 0 {
		return fmt.Errorf("config: invalid server.max_body_bytes %d", c.Server.MaxBodyBytes)
	}
	if c.Usage.RetentionDays < 0 {
		return fmt.Errorf("config: invalid usage.retention_days %d", c.Usage.RetentionDays)
	}
	if c.Router.Breaker.FailureThreshold < 0 {
		return fmt.Errorf("config: invalid router.breaker.failure_threshold %d",
			c.Router.Breaker.FailureThreshold)
	}
	if c.Router.Breaker.Cooldown < 0 {
		return fmt.Errorf("config: invalid router.breaker.cooldown %s",
			c.Router.Breaker.Cooldown)
	}
	if c.Router.HealthCheck.Interval < 0 {
		return fmt.Errorf("config: invalid router.health_check.interval %s",
			c.Router.HealthCheck.Interval)
	}
	if c.Router.HealthCheck.Timeout < 0 {
		return fmt.Errorf("config: invalid router.health_check.timeout %s",
			c.Router.HealthCheck.Timeout)
	}
	if c.Database.Path == "" {
		return errors.New("config: database.path is required")
	}
	if c.Admin.Enabled && c.Admin.Token == "" {
		return errors.New("config: admin.token is required when admin.enabled is true")
	}
	return nil
}
