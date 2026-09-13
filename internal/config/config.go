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
	Admin    Admin    `yaml:"admin"`
}

// Server holds HTTP listener settings.
type Server struct {
	Host            string        `yaml:"host"`
	Port            int           `yaml:"port"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
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
		},
		Database: Database{Path: "data/router.db"},
		Auth:     Auth{Enabled: true},
		Admin:    Admin{Enabled: true, Origins: []string{"http://localhost:3000"}},
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
	if c.Database.Path == "" {
		return errors.New("config: database.path is required")
	}
	if c.Admin.Enabled && c.Admin.Token == "" {
		return errors.New("config: admin.token is required when admin.enabled is true")
	}
	return nil
}
