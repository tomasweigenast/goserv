package goserv_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	httpx "github.com/tomasweigenast/goserv"
)

// ============================================================================
// Helpers
// ============================================================================

func newTestServer(t *testing.T, opts ...httpx.ServerOption) (*httpx.Server, *httptest.Server) {
	t.Helper()
	s := httpx.NewServer(opts...)
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return s, ts
}

// routeDefiner adapts a plain func into a RouteDefiner.
type routeDefiner func(*httpx.RouteGroup)

func (f routeDefiner) Routes(g *httpx.RouteGroup) { f(g) }

func mustGET(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	return resp
}

func mustPOST(t *testing.T, url, contentType, body string) *http.Response {
	t.Helper()
	resp, err := http.Post(url, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	return resp
}

func readBody(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return strings.TrimSpace(string(b))
}

func decodeJSON(t *testing.T, r *http.Response, dst any) {
	t.Helper()
	defer r.Body.Close()
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
}

// ============================================================================
// Handler return patterns
// ============================================================================

func TestMap_ReturnsNothing(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /nothing", func() {})
	}))

	resp := mustGET(t, ts.URL+"/nothing")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMap_ReturnsResponse(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /r", func() httpx.Response {
			return httpx.Created(map[string]string{"ok": "yes"})
		})
	}))

	resp := mustGET(t, ts.URL+"/r")
	if resp.StatusCode != 201 {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
}

func TestMap_ReturnsData(t *testing.T) {
	type item struct{ Name string }
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /data", func() *item { return &item{Name: "test"} })
	}))

	resp := mustGET(t, ts.URL+"/data")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	var got item
	decodeJSON(t, resp, &got)
	if got.Name != "test" {
		t.Errorf("Name = %q, want %q", got.Name, "test")
	}
}

func TestMap_ReturnsData_Nil_Is204(t *testing.T) {
	type item struct{ Name string }
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /nil", func() *item { return nil })
	}))

	resp := mustGET(t, ts.URL+"/nil")
	if resp.StatusCode != 204 {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestMap_ReturnsError_Nil_Is200(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /err-ok", func() error { return nil })
	}))

	resp := mustGET(t, ts.URL+"/err-ok")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMap_ReturnsError_HttpError(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /err", func() error { return httpx.ErrNotFound("gone") })
	}))

	resp := mustGET(t, ts.URL+"/err")
	if resp.StatusCode != 404 {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
}

func TestMap_ReturnsDataAndError_HappyPath(t *testing.T) {
	type item struct{ ID int }
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /de", func() (*item, error) { return &item{ID: 1}, nil })
	}))

	resp := mustGET(t, ts.URL+"/de")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestMap_ReturnsDataAndError_ErrorWins(t *testing.T) {
	type item struct{ ID int }
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /de-err", func() (*item, error) {
			return &item{ID: 1}, httpx.ErrConflict("already exists")
		})
	}))

	resp := mustGET(t, ts.URL+"/de-err")
	if resp.StatusCode != 409 {
		t.Errorf("status = %d, want 409", resp.StatusCode)
	}
}

func TestMap_ReturnsDataAndError_NilNil_Is204(t *testing.T) {
	type item struct{ ID int }
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /nilnil", func() (*item, error) { return nil, nil })
	}))

	resp := mustGET(t, ts.URL+"/nilnil")
	if resp.StatusCode != 204 {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
}

func TestMap_ReturnsResponseAndError_ErrorWins(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /re-err", func() (httpx.Response, error) {
			return httpx.Ok(), httpx.ErrUnauthorized("no auth")
		})
	}))

	resp := mustGET(t, ts.URL+"/re-err")
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

// ============================================================================
// Parameter injection
// ============================================================================

func TestMap_ContextInjection(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /ctx", func(ctx *httpx.Context) string {
			return ctx.Query("name")
		})
	}))

	resp, err := http.Get(ts.URL + "/ctx?name=alice")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "alice") {
		t.Errorf("body %q does not contain alice", body)
	}
}

func TestMap_StdContextInjection(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /stdctx", func(ctx context.Context) string {
			// *Context satisfies context.Context
			if ctx == nil {
				return "nil"
			}
			return "ok"
		})
	}))

	resp := mustGET(t, ts.URL+"/stdctx")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	body := readBody(t, resp)
	if !strings.Contains(body, "ok") {
		t.Errorf("body = %q, want ok", body)
	}
}

func TestMap_PathParamString(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items/:name", func(name string) string { return name })
	}))

	resp := mustGET(t, ts.URL+"/items/widget")
	body := readBody(t, resp)
	if !strings.Contains(body, "widget") {
		t.Errorf("body = %q, want widget", body)
	}
}

func TestMap_PathParamInt32(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items/:id", func(id int32) int32 { return id })
	}))

	resp := mustGET(t, ts.URL+"/items/42")
	body := readBody(t, resp)
	if !strings.Contains(body, "42") {
		t.Errorf("body = %q, want 42", body)
	}
}

func TestMap_PathParamInvalid_Returns400(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items/:id", func(id int32) int32 { return id })
	}))

	resp := mustGET(t, ts.URL+"/items/notanumber")
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestMap_BodyDecoding(t *testing.T) {
	type input struct{ Name string }
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("POST /create", func(req input) string { return req.Name })
	}))

	resp := mustPOST(t, ts.URL+"/create", "application/json", `{"Name":"foo"}`)
	body := readBody(t, resp)
	if !strings.Contains(body, "foo") {
		t.Errorf("body = %q, want foo", body)
	}
}

func TestMap_BodyDecoding_InvalidJSON_Returns400(t *testing.T) {
	type input struct{ Name string }
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("POST /create", func(req input) string { return req.Name })
	}))

	resp := mustPOST(t, ts.URL+"/create", "application/json", `not json`)
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// ============================================================================
// Middleware ordering
// ============================================================================

func TestMap_ServerMiddleware_RunsOutermost(t *testing.T) {
	var order []string

	mw1 := func(ctx httpx.Context, next httpx.Next) {
		order = append(order, "server-before")
		next(ctx)
		order = append(order, "server-after")
	}
	mw2 := func(ctx httpx.Context, next httpx.Next) {
		order = append(order, "group-before")
		next(ctx)
		order = append(order, "group-after")
	}

	s, ts := newTestServer(t, httpx.WithMiddleware(mw1))
	s.RegisterRouteGroup("",
		routeDefiner(func(g *httpx.RouteGroup) {
			g.Map("GET /mw", func() {})
		}),
		httpx.WithMiddleware(mw2),
	)

	mustGET(t, ts.URL+"/mw")

	want := []string{"server-before", "group-before", "group-after", "server-after"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i, v := range want {
		if order[i] != v {
			t.Errorf("order[%d] = %q, want %q", i, order[i], v)
		}
	}
}

func TestMap_MultipleServerMiddlewares_OrderPreserved(t *testing.T) {
	var order []string
	mw := func(label string) httpx.Middleware {
		return func(ctx httpx.Context, next httpx.Next) {
			order = append(order, label+"-in")
			next(ctx)
			order = append(order, label+"-out")
		}
	}

	s, ts := newTestServer(t, httpx.WithMiddleware(mw("A"), mw("B")))
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /multi", func() {})
	}))

	mustGET(t, ts.URL+"/multi")

	want := []string{"A-in", "B-in", "B-out", "A-out"}
	for i, v := range want {
		if i >= len(order) || order[i] != v {
			t.Errorf("order = %v, want %v", order, want)
			break
		}
	}
}

// ============================================================================
// Error handler
// ============================================================================

func TestMap_CustomErrorHandler(t *testing.T) {
	called := false
	s, ts := newTestServer(t, httpx.WithErrorHandler(func(err error, ctx *httpx.Context) httpx.Response {
		called = true
		return httpx.DefaultErrorHandler(err, ctx)
	}))
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /fail", func() error { return httpx.ErrBadRequest("oops") })
	}))

	mustGET(t, ts.URL+"/fail")
	if !called {
		t.Error("custom error handler was not called")
	}
}

func TestMap_DefaultErrorHandler_PlainError_ReturnsProblemDetails(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /boom", func() error { return &plainError{"something broke"} })
	}))

	resp := mustGET(t, ts.URL+"/boom")
	if resp.StatusCode != 500 {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", ct)
	}
	var pd httpx.ProblemDetails
	decodeJSON(t, resp, &pd)
	if pd.Status != 500 {
		t.Errorf("ProblemDetails.Status = %d, want 500", pd.Status)
	}
}

// ============================================================================
// Group prefix
// ============================================================================

func TestMap_GroupPrefix(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("/api/v1", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /ping", func() string { return "pong" })
	}))

	resp := mustGET(t, ts.URL+"/api/v1/ping")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// ============================================================================
// Response headers
// ============================================================================

func TestMap_ResponseHeaders(t *testing.T) {
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /headers", func() httpx.Response {
			return httpx.Ok().WithHeader("X-Custom", "value")
		})
	}))

	resp := mustGET(t, ts.URL+"/headers")
	if resp.Header.Get("X-Custom") != "value" {
		t.Errorf("X-Custom = %q, want value", resp.Header.Get("X-Custom"))
	}
}

// ============================================================================
// Query[T] injection
// ============================================================================

func TestMap_QueryParam_PrimitiveFields(t *testing.T) {
	type q struct {
		Page   int    `query:"page"`
		Search string `query:"search"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(params httpx.Query[q]) string {
			return fmt.Sprintf("%d|%s", params.Value.Page, params.Value.Search)
		})
	}))

	resp := mustGET(t, ts.URL+"/items?page=3&search=hello")
	body := readBody(t, resp)
	if body != `"3|hello"` {
		t.Errorf("body = %q, want %q", body, `"3|hello"`)
	}
}

func TestMap_QueryParam_MissingFieldIsZero(t *testing.T) {
	type q struct {
		Page int `query:"page"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(params httpx.Query[q]) int {
			return params.Value.Page
		})
	}))

	resp := mustGET(t, ts.URL+"/items") // no ?page=
	body := readBody(t, resp)
	if body != "0" {
		t.Errorf("body = %q, want 0", body)
	}
}

func TestMap_QueryParam_InvalidValue_Returns400(t *testing.T) {
	type q struct {
		Page int `query:"page"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(params httpx.Query[q]) int {
			return params.Value.Page
		})
	}))

	resp := mustGET(t, ts.URL+"/items?page=notanumber")
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestMap_QueryParam_WithPathParamAndBody(t *testing.T) {
	type body struct{ Name string }
	type q struct {
		Active bool `query:"active"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("POST /orgs/:id/users", func(id int32, params httpx.Query[q], req body) string {
			return fmt.Sprintf("%d|%v|%s", id, params.Value.Active, req.Name)
		})
	}))

	resp := mustPOST(t, ts.URL+"/orgs/7/users?active=true", "application/json", `{"Name":"alice"}`)
	body2 := readBody(t, resp)
	if body2 != `"7|true|alice"` {
		t.Errorf("body = %q, want %q", body2, `"7|true|alice"`)
	}
}

func TestMap_QueryParam_FallbackToLowercaseFieldName(t *testing.T) {
	type q struct {
		Limit int // no tag — matches ?limit=
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(params httpx.Query[q]) int {
			return params.Value.Limit
		})
	}))

	resp := mustGET(t, ts.URL+"/items?limit=20")
	body := readBody(t, resp)
	if body != "20" {
		t.Errorf("body = %q, want 20", body)
	}
}

func TestMap_StructBeforePathParams_Panics(t *testing.T) {
	type body struct{ Name string }
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when struct appears before path params are exhausted")
		}
	}()

	s := httpx.NewServer()
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		// :id is a path param but the handler puts a struct first (wrong order)
		g.Map("GET /items/:id", func(req body, id int32) string { return "" })
	}))
}

// ============================================================================
// Request struct injection
// ============================================================================

func TestMap_RequestStruct_FromParam(t *testing.T) {
	type req struct {
		ID string `goserv:"fromParam,id"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items/:id", func(r req) string { return r.ID })
	}))

	resp := mustGET(t, ts.URL+"/items/abc")
	body := readBody(t, resp)
	if body != `"abc"` {
		t.Errorf("body = %q, want %q", body, `"abc"`)
	}
}

func TestMap_RequestStruct_FromQuery(t *testing.T) {
	type req struct {
		Page int `goserv:"fromQuery,page"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(r req) int { return r.Page })
	}))

	resp := mustGET(t, ts.URL+"/items?page=5")
	body := readBody(t, resp)
	if body != "5" {
		t.Errorf("body = %q, want 5", body)
	}
}

func TestMap_RequestStruct_FromQuery_Missing_IsZero(t *testing.T) {
	type req struct {
		Page int `goserv:"fromQuery,page"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(r req) int { return r.Page })
	}))

	resp := mustGET(t, ts.URL+"/items")
	body := readBody(t, resp)
	if body != "0" {
		t.Errorf("body = %q, want 0", body)
	}
}

func TestMap_RequestStruct_FromHeader(t *testing.T) {
	type req struct {
		Token string `goserv:"fromHeader,Authorization"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /secure", func(r req) string { return r.Token })
	}))

	httpReq, _ := http.NewRequest("GET", ts.URL+"/secure", nil)
	httpReq.Header.Set("Authorization", "Bearer token123")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if body != `"Bearer token123"` {
		t.Errorf("body = %q, want %q", body, `"Bearer token123"`)
	}
}

func TestMap_RequestStruct_FromBody(t *testing.T) {
	type payload struct{ Name string }
	type req struct {
		Body payload `goserv:"fromBody"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("POST /items", func(r req) string { return r.Body.Name })
	}))

	resp := mustPOST(t, ts.URL+"/items", "application/json", `{"Name":"hello"}`)
	body := readBody(t, resp)
	if body != `"hello"` {
		t.Errorf("body = %q, want %q", body, `"hello"`)
	}
}

func TestMap_RequestStruct_AllSources(t *testing.T) {
	type payload struct{ Value string }
	type req struct {
		ID    int     `goserv:"fromParam,id"`
		Page  int     `goserv:"fromQuery,page"`
		Token string  `goserv:"fromHeader,X-Token"`
		Body  payload `goserv:"fromBody"`
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("POST /items/:id", func(r req) string {
			return fmt.Sprintf("%d|%d|%s|%s", r.ID, r.Page, r.Token, r.Body.Value)
		})
	}))

	httpReq, _ := http.NewRequest("POST", ts.URL+"/items/42?page=3", strings.NewReader(`{"Value":"hi"}`))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Token", "secret")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(t, resp)
	if body != `"42|3|secret|hi"` {
		t.Errorf("body = %q, want %q", body, `"42|3|secret|hi"`)
	}
}

func TestMap_RequestStruct_UntaggedFieldIsZero(t *testing.T) {
	type req struct {
		Tagged   string `goserv:"fromQuery,name"`
		Untagged string // no goserv tag — stays zero
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(r req) string {
			return r.Tagged + "|" + r.Untagged
		})
	}))

	resp := mustGET(t, ts.URL+"/items?name=foo")
	body := readBody(t, resp)
	if body != `"foo|"` {
		t.Errorf("body = %q, want %q", body, `"foo|"`)
	}
}

func TestMap_RequestStruct_DefaultNaming_Lowercase(t *testing.T) {
	type req struct {
		PageSize int `goserv:"fromQuery"` // no explicit name → "pagesize"
	}
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(r req) int { return r.PageSize })
	}))

	resp := mustGET(t, ts.URL+"/items?pagesize=10")
	body := readBody(t, resp)
	if body != "10" {
		t.Errorf("body = %q, want 10", body)
	}
}

func TestMap_RequestStruct_SnakeCaseNaming(t *testing.T) {
	type req struct {
		PageSize int `goserv:"fromQuery"` // no explicit name → "page_size"
	}
	s, ts := newTestServer(t, httpx.WithFieldNamingConvention(httpx.SnakeCaseNaming))
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("GET /items", func(r req) int { return r.PageSize })
	}))

	resp := mustGET(t, ts.URL+"/items?page_size=7")
	body := readBody(t, resp)
	if body != "7" {
		t.Errorf("body = %q, want 7", body)
	}
}

func TestMap_RequestStruct_MultipleFromBody_Panics(t *testing.T) {
	type body struct{ X string }
	type req struct {
		A body `goserv:"fromBody"`
		B body `goserv:"fromBody"`
	}
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic with multiple fromBody fields")
		}
	}()

	s := httpx.NewServer()
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("POST /items", func(r req) string { return "" })
	}))
}

func TestMap_PlainStruct_NoGoservTag_StillBody(t *testing.T) {
	type body struct{ Name string }
	s, ts := newTestServer(t)
	s.RegisterRouteGroup("", routeDefiner(func(g *httpx.RouteGroup) {
		g.Map("POST /items", func(b body) string { return b.Name })
	}))

	resp := mustPOST(t, ts.URL+"/items", "application/json", `{"Name":"backward-compat"}`)
	got := readBody(t, resp)
	if got != `"backward-compat"` {
		t.Errorf("body = %q, want %q", got, `"backward-compat"`)
	}
}

// ============================================================================
// Internal helpers
// ============================================================================

type plainError struct{ msg string }

func (e *plainError) Error() string { return e.msg }
