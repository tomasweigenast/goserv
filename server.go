package goserv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"time"
)

// RequestHandler is a typed handler convenience alias.
type RequestHandler func(r *Context) Response

// RouteDefiner ensures groups passed to RegisterRouteGroup have the Routes method.
type RouteDefiner interface {
	Routes(g *RouteGroup)
}

// ============================================================================
// Shared state
// ============================================================================

// routeConfig holds codec and middleware state shared by Server and RouteGroup.
type routeConfig struct {
	inputCodecs  []InputCodec
	outputCodecs []OutputCodec
	middlewares  []Middleware
	logger       *slog.Logger
}

// ============================================================================
// Server
// ============================================================================

type Server struct {
	routeConfig
	addr              string
	mux               *http.ServeMux
	pathPatterns      map[string]PathPatternFactory
	pathParamDecoders map[reflect.Type]PathParamDecoder
	errorHandler      ErrorHandler

	shutdownTimeout time.Duration
}

// NewServer returns a Server configured with the given options.
// JSONCodec and the built-in path patterns ("uuid", "regex") are registered by default.
func NewServer(opts ...ServerOption) *Server {
	jc := NewJSONCodec()
	s := &Server{
		routeConfig: routeConfig{
			inputCodecs:  []InputCodec{jc},
			outputCodecs: []OutputCodec{jc},
			logger:       slog.Default(),
		},
		addr:              ":8080",
		mux:               http.NewServeMux(),
		pathPatterns:      map[string]PathPatternFactory{"uuid": UUIDPatternFactory, "regex": RegexPatternFactory},
		pathParamDecoders: make(map[reflect.Type]PathParamDecoder),
		errorHandler:      defaultErrorHandler,

		shutdownTimeout: 5 * time.Second,
	}
	for _, opt := range opts {
		opt.applyServer(s)
	}
	return s
}

// ServeHTTP implements http.Handler, allowing the server to be used with
// httptest.NewServer and other standard HTTP tooling.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) Listen(ctx context.Context) error {
	srv := http.Server{
		Addr:    s.addr,
		Handler: s.mux,
	}

	serverError := make(chan error, 1)

	go func() {
		s.logger.Info("server started", "addr", s.addr)
		serverError <- srv.ListenAndServe()
	}()

	select {
	case err := <-serverError:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("server error: %w", err)

	case <-ctx.Done():
		s.logger.Info("shutting down server...", "timeout", s.shutdownTimeout)

		shutdownCtx, cancel := context.WithTimeout(context.Background(), s.shutdownTimeout)
		defer cancel()

		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown failed: %w", err)
		}

		s.logger.Info("server stopped gracefully")
		return nil
	}
}

func (s *Server) RegisterRouteGroup(prefix string, route any, opts ...GroupOption) *RouteGroup {
	g := &RouteGroup{
		routeConfig: routeConfig{
			// Copy server codecs so group overrides don't mutate the server's slices.
			inputCodecs:  append([]InputCodec(nil), s.inputCodecs...),
			outputCodecs: append([]OutputCodec(nil), s.outputCodecs...),
		},
		s:      s,
		prefix: prefix,
	}
	for _, opt := range opts {
		opt.applyGroup(g)
	}
	definer, ok := route.(RouteDefiner)
	if !ok {
		panic(fmt.Sprintf("route type %T must implement interface { Routes(*RouteGroup) }", route))
	}
	definer.Routes(g)
	return g
}

// ============================================================================
// RouteGroup
// ============================================================================

type RouteGroup struct {
	routeConfig
	s      *Server
	prefix string
}
