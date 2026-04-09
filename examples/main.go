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
		goserv.WithFieldNamingConvention(goserv.SnakeCaseNaming),
		goserv.WithErrorHandler(func(err error, r *goserv.Context) goserv.Response {
			fmt.Printf("error handling %s %s: %v\n", r.Request().Method, r.Request().URL.Path, err)
			return goserv.DefaultErrorHandler(err, r)
		}),
	)

	server.RegisterRouteGroup("/patterns", &ShowcaseRoute{})
	server.RegisterRouteGroup("/injection", &InjectionRoute{})

	fmt.Println("")
	fmt.Println("── Handler return patterns ──────────────────────────────────")
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
	fmt.Println("── Parameter injection ──────────────────────────────────────")
	fmt.Println("  GET  /injection/positional/:org/:repo  — positional path params")
	fmt.Println("  GET  /injection/query                  — Query[T] injection  ?page=&search=")
	fmt.Println("  GET  /injection/request-struct/:id     — request struct (goserv tags)")
	fmt.Println("  POST /injection/all/:orgId             — all sources in one request struct")
	fmt.Println("")

	if err := server.Listen(ctx); err != nil {
		log.Fatal(err)
	}
}

// ============================================================================
// Shared types
// ============================================================================

type Item struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type CreateItemRequest struct {
	Name string `json:"name"`
}

// ============================================================================
// Handler return patterns
// ============================================================================

type ShowcaseRoute struct{}

func (s *ShowcaseRoute) Routes(g *goserv.RouteGroup) {
	g.Map("GET /nothing", s.ReturnsNothing)
	g.Map("GET /response", s.ReturnsResponse)
	g.Map("GET /data", s.ReturnsData)
	g.Map("GET /data-nil", s.ReturnsNilData)
	g.Map("GET /error-ok", s.ReturnsErrorOk)
	g.Map("GET /error-err", s.ReturnsErrorErr)
	g.Map("GET /error-plain", s.ReturnsErrorPlain)
	g.Map("GET /data-error-ok", s.ReturnsDataAndErrorOk)
	g.Map("GET /data-error-nil", s.ReturnsDataAndErrorNil)
	g.Map("GET /data-error-err", s.ReturnsDataAndErrorErr)
	g.Map("GET /resp-error-ok", s.ReturnsResponseAndErrorOk)
	g.Map("GET /resp-error-err", s.ReturnsResponseAndErrorErr)
	g.Map("GET /context", s.WithContext)
	g.Map("GET /std-context/:id", s.WithStdContext)
	g.Map("GET /path/:n", s.WithPathParam)
	g.Map("POST /body", s.WithBody)
	g.Map("GET /chained", s.ChainedHeaders)
}

func (*ShowcaseRoute) ReturnsNothing() {}

func (*ShowcaseRoute) ReturnsResponse() goserv.Response {
	return goserv.Ok(Item{ID: 1, Name: "escape-hatch"})
}

func (*ShowcaseRoute) ReturnsData() *Item {
	return &Item{ID: 2, Name: "typed-data"}
}

func (*ShowcaseRoute) ReturnsNilData() *Item { return nil }

func (*ShowcaseRoute) ReturnsErrorOk() error { return nil }

func (*ShowcaseRoute) ReturnsErrorErr() error {
	return goserv.ErrNotFound("item not found")
}

func (*ShowcaseRoute) ReturnsErrorPlain() error {
	return fmt.Errorf("something went wrong internally")
}

func (*ShowcaseRoute) ReturnsDataAndErrorOk() (*Item, error) {
	return &Item{ID: 3, Name: "data-and-error"}, nil
}

func (*ShowcaseRoute) ReturnsDataAndErrorNil() (*Item, error) { return nil, nil }

func (*ShowcaseRoute) ReturnsDataAndErrorErr() (*Item, error) {
	return nil, goserv.ErrConflict("item already exists")
}

func (*ShowcaseRoute) ReturnsResponseAndErrorOk() (goserv.Response, error) {
	return goserv.Created(Item{ID: 4, Name: "created"}), nil
}

func (*ShowcaseRoute) ReturnsResponseAndErrorErr() (goserv.Response, error) {
	return nil, goserv.ErrUnprocessableEntity("validation failed").
		WithDetails(map[string]string{"name": "must not be empty"})
}

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

func (*ShowcaseRoute) WithStdContext(ctx context.Context, id int32) (*Item, error) {
	item, err := fetchWithContext(ctx, id)
	if err != nil {
		return nil, goserv.ErrNotFound("item not found").WithCause(err)
	}
	return item, nil
}

func (*ShowcaseRoute) WithPathParam(n int32) (*Item, error) {
	if n <= 0 {
		return nil, goserv.ErrBadRequest("n must be positive")
	}
	return &Item{ID: int(n), Name: fmt.Sprintf("item-%d", n)}, nil
}

func (*ShowcaseRoute) WithBody(req CreateItemRequest) (*Item, error) {
	if req.Name == "" {
		return nil, goserv.ErrBadRequest("name is required")
	}
	return &Item{ID: 99, Name: req.Name}, nil
}

func (*ShowcaseRoute) ChainedHeaders() goserv.Response {
	return goserv.Created(Item{ID: 6, Name: "chained"}).
		WithHeader("X-Trace-Id", "abc-123").
		WithHeader("X-Region", "us-east-1")
}

// ============================================================================
// Parameter injection showcase
// ============================================================================

type InjectionRoute struct{}

func (r *InjectionRoute) Routes(g *goserv.RouteGroup) {
	// Positional path params — each primitive arg maps to the next :param in order.
	g.Map("GET /positional/:org/:repo", r.Positional)

	// Query[T] — typed query string parameters via a struct wrapper.
	g.Map("GET /query", r.QueryParams)

	// Request struct — a single struct whose fields declare their own sources.
	g.Map("GET /request-struct/:id", r.RequestStruct)

	// All sources combined in one request struct.
	g.Map("POST /all/:orgId", r.AllSources)
}

// Positional path params.
// Try: GET /injection/positional/acme/goserv
func (*InjectionRoute) Positional(org, repo string) map[string]string {
	return map[string]string{"org": org, "repo": repo}
}

// ListQuery is the typed query struct for the /query endpoint.
// Field names are converted using the server's SnakeCaseNaming convention,
// so Page → ?page= and PageSize → ?page_size=.
type ListQuery struct {
	Page     int    // ?page=      (snake_case default: "page")
	PageSize int    // ?page_size= (snake_case default: "page_size")
	Search   string // ?search=
}

// Query[T] injection.
// Try: GET /injection/query?page=2&page_size=10&search=hello
func (*InjectionRoute) QueryParams(q goserv.Query[ListQuery]) map[string]any {
	return map[string]any{
		"page":      q.Value.Page,
		"page_size": q.Value.PageSize,
		"search":    q.Value.Search,
	}
}

// ItemRequest uses goserv tags to pull individual fields from different sources.
type ItemRequest struct {
	// fromParam — path parameter :id
	ID int64 `goserv:"fromParam,id"`
	// fromQuery — ?include_deleted= (no explicit name: snake_case of IncludeDeleted)
	IncludeDeleted bool `goserv:"fromQuery"`
	// fromHeader — X-Request-Id header
	TraceID string `goserv:"fromHeader,X-Request-Id"`
}

// Request struct injection.
// Try: GET /injection/request-struct/42?include_deleted=true  (add X-Request-Id header)
func (*InjectionRoute) RequestStruct(req ItemRequest) map[string]any {
	return map[string]any{
		"id":              req.ID,
		"include_deleted": req.IncludeDeleted,
		"trace_id":        req.TraceID,
	}
}

// CreateOrgUserRequest combines all four sources in one struct.
type CreateOrgUserRequest struct {
	// path param :orgId
	OrgID int64 `goserv:"fromParam,orgId"`
	// ?dry_run=
	DryRun bool `goserv:"fromQuery"`
	// Authorization header
	Token string `goserv:"fromHeader,Authorization"`
	// decoded request body
	Body CreateItemRequest `goserv:"fromBody"`
}

// All sources in one request struct.
// Try: POST /injection/all/7?dry_run=true
//
//	Authorization: Bearer mytoken
//	Content-Type: application/json
//	{"name": "widget"}
func (*InjectionRoute) AllSources(ctx *goserv.Context, req CreateOrgUserRequest) (*Item, error) {
	if req.Body.Name == "" {
		return nil, goserv.ErrBadRequest("name is required")
	}
	name := fmt.Sprintf("[org:%d] %s", req.OrgID, req.Body.Name)
	if req.DryRun {
		name = "[dry-run] " + name
	}
	_ = req.Token // would be used for auth in a real handler
	_ = ctx       // context available alongside the request struct
	return &Item{ID: 1, Name: name}, nil
}

// ============================================================================
// Simulated downstream function
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
