package goserv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"time"

	"github.com/tomasweigenast/goserv/codec"
	"github.com/tomasweigenast/goserv/pathparam"
)

type Server struct {
	routeConfig
	addr              string
	mux               *http.ServeMux
	pathPatterns      map[string]pathparam.PatternFactory
	pathParamDecoders map[reflect.Type]pathparam.ParamDecoder
	errorHandler      ErrorHandler
	shutdownTimeout   time.Duration
}

// NewServer returns a Server configured with the given options.
// JSONCodec and the built-in path patterns ("uuid", "regex") are registered by default.
func NewServer(opts ...ServerOption) *Server {
	jc := codec.NewJSONCodec()
	s := &Server{
		routeConfig: routeConfig{
			inputCodecs:  []codec.InputCodec{jc},
			outputCodecs: []codec.OutputCodec{jc},
			logger:       slog.Default(),
		},
		addr:              ":8080",
		mux:               http.NewServeMux(),
		pathPatterns:      map[string]pathparam.PatternFactory{"uuid": pathparam.UUIDPatternFactory, "regex": pathparam.RegexPatternFactory},
		pathParamDecoders: make(map[reflect.Type]pathparam.ParamDecoder),
		errorHandler:      defaultErrorHandler,
		shutdownTimeout:   5 * time.Second,
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
			inputCodecs:  append([]codec.InputCodec(nil), s.inputCodecs...),
			outputCodecs: append([]codec.OutputCodec(nil), s.outputCodecs...),
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
