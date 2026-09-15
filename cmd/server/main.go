// Command server is the single-binary entrypoint of Zealish Router.
// It wires every dependency explicitly and owns the process lifecycle.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/zealish/zealish-router/internal/api"
	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/cache"
	"github.com/zealish/zealish-router/internal/config"
	"github.com/zealish/zealish-router/internal/extension"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
)

// version is overridden at build time with -X main.version=<tag>.
var version = "dev"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "zealish-router: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("zealish-router", flag.ContinueOnError)
	configPath := fs.String("config", "config.yaml", "path to the configuration file")
	logLevel := fs.String("log-level", "info", "log level: debug, info, warn, error")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	command := "serve"
	if len(rest) > 0 {
		command = rest[0]
		rest = rest[1:]
	}

	// version answers before the configuration is touched, so it works on a
	// machine that has no config file yet.
	if command == "version" {
		fmt.Println(version)
		return nil
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}

	logger := newLogger(*logLevel)

	switch command {
	case "serve":
		return serve(cfg, *configPath, logger)
	case "validate":
		logger.Info("configuration is valid", slog.String("path", *configPath))
		return nil
	case "keys":
		return keysCommand(cfg, rest)
	case "models":
		return modelsCommand(cfg)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

// keysCommand implements `keys create|list|revoke`.
func keysCommand(cfg *config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: keys create --name <name> | keys list | keys revoke <id>")
	}

	ctx := context.Background()
	store, err := storage.OpenSQLite(ctx, cfg.Database.Path)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	keys := store.APIKeys()

	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("keys create", flag.ContinueOnError)
		name := fs.String("name", "", "human-readable label for the key")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *name == "" {
			return errors.New("keys create: --name is required")
		}

		generated, err := auth.GenerateKey(*name)
		if err != nil {
			return err
		}
		if err := keys.Create(ctx, generated.Record); err != nil {
			return err
		}
		// The raw key is unrecoverable after this line: only its hash is stored.
		fmt.Printf("id:  %s\nkey: %s\n\nStore this key now; it cannot be shown again.\n",
			generated.Record.ID, generated.Raw)
		return nil

	case "list":
		records, err := keys.List(ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME\tENABLED\tCREATED\tLAST USED")
		for _, k := range records {
			lastUsed := "never"
			if !k.LastUsedAt.IsZero() {
				lastUsed = k.LastUsedAt.Format(time.RFC3339)
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%t\t%s\t%s\n",
				k.ID, k.Name, k.Enabled, k.CreatedAt.Format(time.RFC3339), lastUsed)
		}
		return w.Flush()

	case "revoke":
		if len(args) < 2 {
			return errors.New("usage: keys revoke <id>")
		}
		if err := keys.Delete(ctx, args[1]); err != nil {
			return err
		}
		fmt.Printf("revoked %s\n", args[1])
		return nil

	default:
		return fmt.Errorf("unknown keys subcommand %q", args[0])
	}
}

// modelsCommand prints the stored alias routing table and the combos on top.
func modelsCommand(cfg *config.Config) error {
	ctx := context.Background()
	store, err := storage.OpenSQLite(ctx, cfg.Database.Path)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()

	aliases, err := store.Models().List(ctx)
	if err != nil {
		return err
	}
	combos, err := store.Combos().List(ctx)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ALIAS\tPROVIDER\tMODEL\tFALLBACK")
	for _, m := range aliases {
		fallback := "-"
		if len(m.Fallback) > 0 {
			fallback = strings.Join(m.Fallback, " → ")
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.Alias, m.Provider, m.Model, fallback)
	}
	if len(combos) > 0 {
		_, _ = fmt.Fprintln(w, "\nCOMBO\tSTRATEGY\tENABLED\tMEMBERS")
		for _, c := range combos {
			_, _ = fmt.Fprintf(w, "%s\t%s\t%t\t%s\n",
				c.Name, c.Strategy, c.Enabled, strings.Join(comboMembers(c), " → "))
		}
	}
	return w.Flush()
}

// comboMembers renders a pool, annotating each member with its weight when the
// combo routes by weight.
func comboMembers(c storage.Combo) []string {
	if c.Strategy != storage.ComboWeighted {
		return c.Members
	}
	out := make([]string, len(c.Members))
	for i, member := range c.Members {
		weight := 1
		if i < len(c.Weights) && c.Weights[i] > 0 {
			weight = c.Weights[i]
		}
		out[i] = fmt.Sprintf("%s (%d)", member, weight)
	}
	return out
}

func serve(cfg *config.Config, configPath string, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := storage.OpenSQLite(ctx, cfg.Database.Path)
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	logger.Info("database ready", slog.String("path", cfg.Database.Path))

	// Retention runs for the lifetime of the process; ctx cancellation on
	// shutdown stops it. Traces share the usage window: they describe the same
	// requests, so keeping one past the other has no use.
	retention := time.Duration(cfg.Usage.RetentionDays) * 24 * time.Hour
	go storage.PruneUsage(ctx, store.Usage(), retention, logger)
	go storage.PruneTraces(ctx, store.Traces(), retention, logger)

	collector := metrics.New()
	engine := router.NewEngine(logger, collector)
	engine.SetUsageStore(store.Usage())
	engine.SetTraceStore(store.Traces())
	engine.SetBreakerPolicy(router.BreakerPolicy{
		FailureThreshold: cfg.Router.Breaker.FailureThreshold,
		Cooldown:         cfg.Router.Breaker.Cooldown,
	})
	loader := router.NewLoader(store.Providers(), store.Models(), store.Combos(), store.Proxies(), engine)
	if err := loader.Load(ctx); err != nil {
		return err
	}

	if cfg.Router.HealthCheck.Enabled {
		checker := router.NewHealthChecker(engine, router.HealthCheckPolicy{
			Interval: cfg.Router.HealthCheck.Interval,
			Timeout:  cfg.Router.HealthCheck.Timeout,
		}, logger)
		go checker.Run(ctx)
	}

	authenticator := auth.NewService(cfg.Auth.Enabled, cfg.Auth.APIKeys, store.APIKeys(), logger)
	adminAuth := auth.NewAdminService(cfg.Admin.Enabled, cfg.Admin.Token)
	quota := auth.NewQuota(store.Usage())

	extensions := extension.NewRegistry(store.Settings())
	if err := extensions.Load(ctx); err != nil {
		return err
	}

	var responses *cache.Cache
	if cfg.Cache.Enabled {
		responses = cache.New(cfg.Cache.TTL, cfg.Cache.MaxEntries)
		logger.Info("response cache enabled",
			slog.Duration("ttl", cfg.Cache.TTL),
			slog.Int("max_entries", cfg.Cache.MaxEntries))
	}

	server := api.NewServer(api.Dependencies{
		Config:     cfg,
		Engine:     engine,
		Loader:     loader,
		Store:      store,
		Auth:       authenticator,
		AdminAuth:  adminAuth,
		Quota:      quota,
		Extensions: extensions,
		Cache:      responses,
		Metrics:    collector,
		Logger:     logger,
	})

	// Routing lives in the database, so a configuration reload only refreshes
	// the credentials the YAML still owns. Listener and database settings are
	// fixed for the lifetime of the process.
	go func() {
		err := config.Watch(ctx, configPath, logger, func(next *config.Config) {
			authenticator.SetStaticKeys(next.Auth.APIKeys)
			adminAuth.SetToken(next.Admin.Token)
			logger.Info("configuration reloaded")
		})
		if err != nil {
			logger.Error("config watcher stopped", slog.Any("error", err))
		}
	}()

	errCh := make(chan error, 1)
	go func() { errCh <- server.Start() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("shutdown signal received")
		return server.Shutdown(context.Background())
	}
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl}))
}
