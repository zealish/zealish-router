// Package api owns the HTTP transport layer: routing table, middleware,
// handlers and the server lifecycle. It translates HTTP to domain calls only.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/zealish/zealish-router/internal/auth"
	"github.com/zealish/zealish-router/internal/cache"
	"github.com/zealish/zealish-router/internal/config"
	"github.com/zealish/zealish-router/internal/extension"
	"github.com/zealish/zealish-router/internal/metrics"
	"github.com/zealish/zealish-router/internal/router"
	"github.com/zealish/zealish-router/internal/storage"
)

// Dependencies are the collaborators injected into the HTTP layer.
type Dependencies struct {
	Config    *config.Config
	Engine    *router.Engine
	Loader    *router.Loader
	Store     storage.Store
	Auth      auth.Authenticator
	AdminAuth auth.Authenticator
	// Quota enforces per-key rate limits and budgets on /v1. Nil disables it.
	Quota *auth.Quota
	// Extensions applies enabled request extensions (RTK, sanitization)
	// before routing. Nil disables the extension system entirely.
	Extensions *extension.Registry
	// Cache serves repeated non-streaming completions. Nil disables it.
	Cache   *cache.Cache
	Metrics *metrics.Metrics
	Logger  *slog.Logger
}

// Server wraps the HTTP listener and its lifecycle.
type Server struct {
	http     *http.Server
	logger   *slog.Logger
	shutdown time.Duration
}

// NewServer builds the HTTP server and its routing table.
func NewServer(deps Dependencies) *Server {
	h := newHandler(deps)

	return &Server{
		http: &http.Server{
			Addr:         deps.Config.Server.Address(),
			Handler:      newRoutes(deps, h),
			ReadTimeout:  deps.Config.Server.ReadTimeout,
			WriteTimeout: deps.Config.Server.WriteTimeout,
		},
		logger:   deps.Logger,
		shutdown: deps.Config.Server.ShutdownTimeout,
	}
}

func newRoutes(deps Dependencies, h *handler) http.Handler {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(recoverer(deps.Logger))
	r.Use(requestLogger(deps.Logger))
	r.Use(instrument(deps.Metrics))

	r.Get("/health", h.health)
	r.Handle("/metrics", deps.Metrics.Handler())

	r.Route("/v1", func(v1 chi.Router) {
		v1.Use(limitBody(deps.Config.Server.MaxBodyBytes))
		v1.Use(authenticate(deps.Auth))
		v1.Use(enforceQuota(deps.Quota))
		v1.Use(traceRequests())
		v1.Get("/models", h.listModels)
		v1.Post("/chat/completions", h.chatCompletions)
		v1.Post("/embeddings", h.embeddings)
		// The Anthropic dialect, for clients that speak Messages rather than
		// Chat Completions. Same auth, quota, allowlist and routing.
		v1.Post("/messages", h.messages)
		// The OpenAI Responses dialect, which Codex CLI and the current
		// OpenAI SDKs default to. Same auth, quota, allowlist and routing.
		v1.Post("/responses", h.responses)
	})

	if deps.Config.Admin.Enabled {
		admin := newAdminHandler(deps)
		r.Route("/api/v1", func(api chi.Router) {
			api.Use(cors(deps.Config.Admin.Origins))
			api.Use(limitBody(deps.Config.Server.MaxBodyBytes))
			api.Use(authenticate(deps.AdminAuth))
			admin.routes(api)
		})
	}

	return r
}

// Start blocks serving HTTP until the listener stops.
func (s *Server) Start() error {
	s.logger.Info("http server listening", slog.String("addr", s.http.Addr))
	if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s.shutdown)
	defer cancel()
	return s.http.Shutdown(ctx)
}
