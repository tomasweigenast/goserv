package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tomasweigenast/goserv"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	server := goserv.NewServer(
		goserv.WithPort(2222),
		goserv.WithErrorHandler(func(err error, r *goserv.Context) goserv.Response {
			// Custom error handler: log the error, then delegate to default behaviour.
			fmt.Printf("error handling %s %s: %v\n", r.Request().Method, r.Request().URL.Path, err)
			return goserv.DefaultErrorHandler(err, r)
		}),
	)

	server.RegisterRouteGroup("/patterns", &ShowcaseRoute{})

	fmt.Println("")
	fmt.Println("  GET  /patterns/nothing          — returns nothing (200)")
	fmt.Println("  GET  /patterns/response         — returns Response directly (escape hatch)")
	fmt.Println("  GET  /patterns/data             — returns typed data (200)")
	fmt.Println("  GET  /patterns/data-nil         — returns nil pointer (204)")
	fmt.Println("  GET  /patterns/error-ok         — returns error, nil (200)")
	fmt.Println("  GET  /patterns/error-err        — returns error, non-nil (*HttpError → 404)")
	fmt.Println("  GET  /patterns/error-plain      — returns plain error (500 via error handler)")
	fmt.Println("  GET  /patterns/data-error-ok    — returns (T, nil) (200)")
	fmt.Println("  GET  /patterns/data-error-nil   — returns (nil, nil) (204)")
	fmt.Println("  GET  /patterns/data-error-err   — returns (nil, *HttpError) (409)")
	fmt.Println("  GET  /patterns/resp-error-ok    — returns (Response, nil) (201)")
	fmt.Println("  GET  /patterns/resp-error-err   — returns (nil, *HttpError) (422)")
	fmt.Println("  GET  /patterns/context          — *goserv.Context injection")
	fmt.Println("  GET  /patterns/std-context/:id  — context.Context injection")
	fmt.Println("  GET  /patterns/path/:n          — path param (int32)")
	fmt.Println("  POST /patterns/body             — body decoding")
	fmt.Println("  GET  /patterns/chained          — response with chained headers")
	fmt.Println("")

	if err := server.Listen(ctx); err != nil {
		log.Fatal(err)
	}
}

// ============================================================================
// Showcase types
// ============================================================================

type Item struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type CreateItemRequest struct {
	Name string `json:"name"`
}

// ============================================================================
// Showcase route
// ============================================================================

type ShowcaseRoute struct{}

func (s *ShowcaseRoute) Routes(g *goserv.RouteGroup) {
	// Pattern 1: func() — returns nothing
	g.Map("GET /nothing", s.ReturnsNothing)

	// Pattern 2: func() Response — escape hatch, full control
	g.Map("GET /response", s.ReturnsResponse)

	// Pattern 3: func() T — typed data, auto-wrapped as 200
	g.Map("GET /data", s.ReturnsData)

	// Pattern 3b: func() T where T is nil — 204 No Content
	g.Map("GET /data-nil", s.ReturnsNilData)

	// Pattern 4: func() error, nil — 200 no body
	g.Map("GET /error-ok", s.ReturnsErrorOk)

	// Pattern 4b: func() error, *HttpError — uses its status code (404)
	g.Map("GET /error-err", s.ReturnsErrorErr)

	// Pattern 4c: func() error, plain error — error handler maps to 500
	g.Map("GET /error-plain", s.ReturnsErrorPlain)

	// Pattern 5: func() (T, error), nil error — 200 with body
	g.Map("GET /data-error-ok", s.ReturnsDataAndErrorOk)

	// Pattern 5b: func() (T, error), nil T + nil error — 204
	g.Map("GET /data-error-nil", s.ReturnsDataAndErrorNil)

	// Pattern 5c: func() (T, error), *HttpError — 409
	g.Map("GET /data-error-err", s.ReturnsDataAndErrorErr)

	// Pattern 6: func() (Response, error), nil error — uses Response (201)
	g.Map("GET /resp-error-ok", s.ReturnsResponseAndErrorOk)

	// Pattern 6b: func() (Response, error), *HttpError — 422
	g.Map("GET /resp-error-err", s.ReturnsResponseAndErrorErr)

	// Context injection: *goserv.Context — access headers, query, etc.
	g.Map("GET /context", s.WithContext)

	// Context injection: context.Context — passes directly to downstream calls
	g.Map("GET /std-context/:id", s.WithStdContext)

	// Path parameter injection
	g.Map("GET /path/:n", s.WithPathParam)

	// Body decoding
	g.Map("POST /body", s.WithBody)

	// Chained response headers
	g.Map("GET /chained", s.ChainedHeaders)
}

// Pattern 1 — func()
// Framework responds 200 with no body.
func (*ShowcaseRoute) ReturnsNothing() {}

// Pattern 2 — func() Response
// Full escape hatch: choose any status, set headers, control body.
func (*ShowcaseRoute) ReturnsResponse() goserv.Response {
	return goserv.Ok(Item{ID: 1, Name: "escape-hatch"})
}

// Pattern 3 — func() T
// Return any Go value; the framework serializes it with a 200.
func (*ShowcaseRoute) ReturnsData() *Item {
	return &Item{ID: 2, Name: "typed-data"}
}

// Pattern 3b — func() T where pointer is nil
// Nil pointer → 204 No Content (nothing to serialize).
func (*ShowcaseRoute) ReturnsNilData() *Item {
	return nil
}

// Pattern 4 — func() error, returning nil
// Nil error → 200 no body.
func (*ShowcaseRoute) ReturnsErrorOk() error {
	return nil
}

// Pattern 4b — func() error, returning *HttpError
// *HttpError implements Response: the framework uses its status code (404) and body.
func (*ShowcaseRoute) ReturnsErrorErr() error {
	return goserv.ErrNotFound("item not found")
}

// Pattern 4c — func() error, returning a plain error
// The error handler receives it; default handler maps unknown errors to 500.
func (*ShowcaseRoute) ReturnsErrorPlain() error {
	return fmt.Errorf("something went wrong internally")
}

// Pattern 5 — func() (T, error), happy path
// Nil error → 200 with the typed value serialized as body.
func (*ShowcaseRoute) ReturnsDataAndErrorOk() (*Item, error) {
	return &Item{ID: 3, Name: "data-and-error"}, nil
}

// Pattern 5b — func() (T, error), nil T + nil error
// Both nil → 204 No Content.
func (*ShowcaseRoute) ReturnsDataAndErrorNil() (*Item, error) {
	return nil, nil
}

// Pattern 5c — func() (T, error), error path
// Non-nil error → error handler; *HttpError is used directly (409 Conflict).
func (*ShowcaseRoute) ReturnsDataAndErrorErr() (*Item, error) {
	return nil, goserv.ErrConflict("item already exists")
}

// Pattern 6 — func() (Response, error), happy path
// Nil error → Response used as-is. Useful when 200 is not the right status.
func (*ShowcaseRoute) ReturnsResponseAndErrorOk() (goserv.Response, error) {
	return goserv.Created(Item{ID: 4, Name: "created"}), nil
}

// Pattern 6b — func() (Response, error), error path
// Non-nil error → error handler wins over the Response value.
func (*ShowcaseRoute) ReturnsResponseAndErrorErr() (goserv.Response, error) {
	return nil, goserv.ErrUnprocessableEntity("validation failed").
		WithDetails(map[string]string{"name": "must not be empty"})
}

// Context injection — *goserv.Context
// Grants access to request headers, query params, and the raw ResponseWriter.
func (*ShowcaseRoute) WithContext(ctx *goserv.Context) (*Item, error) {
	name := ctx.Query("name")
	if name == "" {
		name = ctx.Header("X-Default-Name")
	}
	if name == "" {
		name = "default"
	}
	return &Item{ID: 5, Name: name}, nil
}

// Context injection — context.Context
// *goserv.Context satisfies context.Context, so it flows into any downstream call.
func (*ShowcaseRoute) WithStdContext(ctx context.Context, id int32) (*Item, error) {
	item, err := fetchWithContext(ctx, id)
	if err != nil {
		return nil, goserv.ErrNotFound("item not found").WithCause(err)
	}
	return item, nil
}

// Path parameter injection — typed, zero reflection at request time
func (*ShowcaseRoute) WithPathParam(n int32) (*Item, error) {
	if n <= 0 {
		return nil, goserv.ErrBadRequest("n must be positive")
	}
	return &Item{ID: int(n), Name: fmt.Sprintf("item-%d", n)}, nil
}

// Body decoding — struct params are decoded from the request body via the active codec
func (*ShowcaseRoute) WithBody(req CreateItemRequest) (*Item, error) {
	if req.Name == "" {
		return nil, goserv.ErrBadRequest("name is required")
	}
	return &Item{ID: 99, Name: req.Name}, nil
}

// Chained headers — *resultWriter supports method chaining for headers
func (*ShowcaseRoute) ChainedHeaders() goserv.Response {
	return goserv.Created(Item{ID: 6, Name: "chained"}).
		WithHeader("X-Trace-Id", "abc-123").
		WithHeader("X-Region", "us-east-1")
}

// ============================================================================
// Simulated downstream function (uses context for cancellation/deadline)
// ============================================================================

func fetchWithContext(ctx context.Context, id int32) (*Item, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
		if id == 404 {
			return nil, fmt.Errorf("not found in store")
		}
		return &Item{ID: int(id), Name: fmt.Sprintf("fetched-%d-%d", id, time.Now().UnixMilli())}, nil
	}
}
