package goserv_test

import (
	"testing"

	httpx "github.com/tomasweigenast/goserv"
)

func TestContext_Set_PropagatesValue(t *testing.T) {
	type key struct{}
	s, ts := newTestServer(t)
	var got string

	mw := func(ctx httpx.Context, next httpx.Next) {
		next(ctx.Set(key{}, "hello"))
	}
	s.RegisterRouteGroup("",
		routeDefiner(func(g *httpx.RouteGroup) {
			g.Map("GET /set", func(ctx *httpx.Context) string {
				if v, ok := ctx.Value(key{}).(string); ok {
					got = v
				}
				return got
			})
		}),
		httpx.WithMiddleware(mw),
	)

	resp := mustGET(t, ts.URL+"/set")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got != "hello" {
		t.Errorf("Value from middleware = %q, want hello", got)
	}
}

func TestContext_Set_OriginalUnchanged(t *testing.T) {
	type key struct{}
	var original, enriched httpx.Context

	mw := func(ctx httpx.Context, next httpx.Next) {
		original = ctx
		next(ctx.Set(key{}, "value"))
	}

	s, ts := newTestServer(t)
	s.RegisterRouteGroup("",
		routeDefiner(func(g *httpx.RouteGroup) {
			g.Map("GET /orig", func(ctx *httpx.Context) {
				enriched = *ctx
			})
		}),
		httpx.WithMiddleware(mw),
	)

	mustGET(t, ts.URL+"/orig")

	if original.Value(key{}) != nil {
		t.Error("original context should not have the key set by Set()")
	}
	if enriched.Value(key{}) != "value" {
		t.Errorf("enriched context should have the key, got %v", enriched.Value(key{}))
	}
}
