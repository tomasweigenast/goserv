package goserv

import (
	"context"
	"net/http"
	"time"
)

// contextKey is the key used to stash a Context into r.Context() at the
// middleware→handler boundary so the argSetter can retrieve it directly.
type contextKey struct{}

// Context is the request context injected into handlers. It implements context.Context
// so it can be passed directly to any function that accepts one (database calls,
// service layers, etc.).
//
// Handlers may declare either context.Context or *Context as a parameter — both
// receive the same *Context value at request time.
//
// Middleware can attach values via Set, which are then readable downstream through
// both the *Context and the standard context.Value chain.
type Context struct {
	ctx context.Context
	r   *http.Request
	w   http.ResponseWriter
}

// — context.Context implementation —

func (c *Context) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c *Context) Done() <-chan struct{}       { return c.ctx.Done() }
func (c *Context) Err() error                  { return c.ctx.Err() }
func (c *Context) Value(key any) any           { return c.ctx.Value(key) }

// Set returns a copy of the context with the given key/value pair attached.
// The original context is not modified. The value is visible to downstream
// middleware and handlers via context.Value.
//
//	next(ctx.Set("userID", id))
func (c Context) Set(key, val any) Context {
	c.ctx = context.WithValue(c.ctx, key, val)
	c.r = c.r.WithContext(c.ctx)
	return c
}

// Header returns the value of the named request header.
func (c *Context) Header(name string) string { return c.r.Header.Get(name) }

// Headers returns all request headers.
func (c *Context) Headers() map[string][]string { return c.r.Header }

// Query returns the value of the named URL query parameter.
func (c *Context) Query(name string) string { return c.r.URL.Query().Get(name) }

// PathValue returns the raw string value of the named path parameter.
// Prefer declaring typed path parameters directly in the handler signature.
func (c *Context) PathValue(name string) string { return c.r.PathValue(name) }

// Request returns the underlying *http.Request.
func (c *Context) Request() *http.Request { return c.r }

// ResponseWriter exposes the underlying http.ResponseWriter for low-level control.
func (c *Context) ResponseWriter() http.ResponseWriter { return c.w }
