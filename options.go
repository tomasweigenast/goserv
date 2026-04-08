package goserv

import (
	"fmt"
	"log/slog"
	"net"
	"reflect"
	"strconv"
	"time"

	"github.com/tomasweigenast/goserv/codec"
)

// ============================================================================
// Option interfaces
// ============================================================================

// ServerOption configures a Server.
type ServerOption interface {
	applyServer(*Server)
}

// GroupOption configures a RouteGroup.
type GroupOption interface {
	applyGroup(*RouteGroup)
}

// RouteOption is an option that applies to both Server and RouteGroup.
type RouteOption interface {
	ServerOption
	GroupOption
}

type serverOptionFunc func(*Server)

func (f serverOptionFunc) applyServer(s *Server) { f(s) }

type sharedOptionFunc func(*routeConfig)

func (f sharedOptionFunc) applyServer(s *Server)    { f(&s.routeConfig) }
func (f sharedOptionFunc) applyGroup(g *RouteGroup) { f(&g.routeConfig) }

// ============================================================================
// Shared options (Server and RouteGroup)
// ============================================================================

// WithLogger sets up the logger the server will use.
// If no logger is configured, slog.Default() is used
func WithLogger(logger *slog.Logger) RouteOption {
	return sharedOptionFunc(func(cfg *routeConfig) {
		cfg.logger = logger
	})
}

// WithInputCodec appends an InputCodec to the codec list.
// Codecs are tried in registration order against the request Content-Type header.
func WithInputCodec(c codec.InputCodec) RouteOption {
	return sharedOptionFunc(func(cfg *routeConfig) {
		cfg.inputCodecs = append(cfg.inputCodecs, c)
	})
}

// WithOutputCodec appends an OutputCodec to the codec list.
// Codecs are tried in registration order against the request Accept header.
func WithOutputCodec(c codec.OutputCodec) RouteOption {
	return sharedOptionFunc(func(cfg *routeConfig) {
		cfg.outputCodecs = append(cfg.outputCodecs, c)
	})
}

// WithCodec registers c as an InputCodec, OutputCodec, or both.
func WithCodec(c any) RouteOption {
	return sharedOptionFunc(func(cfg *routeConfig) {
		if ic, ok := c.(codec.InputCodec); ok {
			cfg.inputCodecs = append(cfg.inputCodecs, ic)
		}
		if oc, ok := c.(codec.OutputCodec); ok {
			cfg.outputCodecs = append(cfg.outputCodecs, oc)
		}
	})
}

// WithMiddleware adds one or more middlewares to the pipeline.
// First-registered middleware is outermost (runs first).
func WithMiddleware(middlewares ...Middleware) RouteOption {
	return sharedOptionFunc(func(cfg *routeConfig) {
		cfg.middlewares = append(cfg.middlewares, middlewares...)
	})
}

// ============================================================================
// Server-only options
// ============================================================================

// WithShutdownTimeout sets the server timeout for shutdown operation
func WithShutdownTimeout(timeout time.Duration) ServerOption {
	return serverOptionFunc(func(s *Server) {
		s.shutdownTimeout = timeout
	})
}

// WithPort sets the port the server listens on. Default: 8080.
func WithPort(port int) ServerOption {
	return serverOptionFunc(func(s *Server) {
		s.addr = fmt.Sprintf(":%d", port)
	})
}

// WithAddress sets the IP+port the server listens on. Default: :8080.
func WithAddress(addr string) ServerOption {
	return serverOptionFunc(func(s *Server) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			panic(fmt.Errorf("unable to parse addr: %s", err))
		}
		if _, err := strconv.Atoi(port); err != nil {
			panic(fmt.Errorf("wrong port number: %s", err))
		}
		s.addr = net.JoinHostPort(host, port)
	})
}

// WithPathPattern registers a named path parameter constraint factory.
// The name is referenced in route patterns, e.g. :id[mypattern].
func WithPathPattern(name string, factory PathPatternFactory) ServerOption {
	return serverOptionFunc(func(s *Server) {
		s.pathPatterns[name] = factory
	})
}

// WithErrorHandler sets a custom error handler that translates handler errors
// into HTTP responses.
func WithErrorHandler(h ErrorHandler) ServerOption {
	return serverOptionFunc(func(s *Server) {
		s.errorHandler = h
	})
}

// WithPathParamDecoder registers a custom decoder for path parameters.
// Use NewPathParamDecoder to wrap a plain parse function:
//
//	WithPathParamDecoder(NewPathParamDecoder(time.ParseDuration))
func WithPathParamDecoder(dec PathParamDecoder) ServerOption {
	return serverOptionFunc(func(s *Server) {
		s.pathParamDecoders[reflect.TypeOf(dec.Zero())] = dec
	})
}
