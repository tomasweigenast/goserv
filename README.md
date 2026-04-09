# goserv

A minimal, reflection-based HTTP framework for Go 1.22+.

Routes are registered with typed handler functions. The framework injects parameters by type at request time — no manual binding, no `ctx.Param()` calls.

```go
server := goserv.NewServer(goserv.WithPort(8080))

server.RegisterRouteGroup("/api", &UserRoutes{})

server.Listen(ctx)
```

```go
type UserRoutes struct{}

func (r *UserRoutes) Routes(g *goserv.RouteGroup) {
    g.Map("GET  /users/:id", r.Get)
    g.Map("POST /users",     r.Create)
}

func (r *UserRoutes) Get(id int64) (*User, error) { ... }
func (r *UserRoutes) Create(req CreateUserRequest) (*User, error) { ... }
```

---

## Table of contents

- [Server setup](#server-setup)
- [Registering routes](#registering-routes)
- [Handler signatures](#handler-signatures)
- [Parameter injection](#parameter-injection)
- [Path parameters](#path-parameters)
- [Request struct](#request-struct)
- [Field naming convention](#field-naming-convention)
- [Query parameters](#query-parameters)
- [Response helpers](#response-helpers)
- [Error handling](#error-handling)
- [Middleware](#middleware)
- [Codecs](#codecs)
- [Path parameter decoders](#path-parameter-decoders)
- [Path parameter patterns](#path-parameter-patterns)

---

## Server setup

```go
server := goserv.NewServer(
    goserv.WithPort(8080),                   // default: 8080
    goserv.WithAddress("127.0.0.1:9000"),    // overrides WithPort
    goserv.WithShutdownTimeout(10 * time.Second), // default: 5s
    goserv.WithLogger(slog.Default()),
    goserv.WithErrorHandler(myErrorHandler),
    goserv.WithMiddleware(authMiddleware, loggingMiddleware),
)
```

Start the server with a context. Cancel the context to trigger graceful shutdown:

```go
ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
defer stop()

if err := server.Listen(ctx); err != nil {
    log.Fatal(err)
}
```

`ServeHTTP` is also implemented, so the server can be used directly with `httptest.NewServer`.

---

## Registering routes

Routes are registered in groups. A group has a prefix, a codec list, and a middleware chain inherited from (and extending) the server's own.

```go
// route type must implement RouteDefiner:
//   type RouteDefiner interface { Routes(g *RouteGroup) }

server.RegisterRouteGroup("/api/v1", &UserRoutes{})

// With group-scoped options:
server.RegisterRouteGroup("/internal", &AdminRoutes{},
    goserv.WithMiddleware(requireAdmin),
)
```

Inside `Routes`, call `g.Map(pattern, handler)`:

```go
func (r *UserRoutes) Routes(g *goserv.RouteGroup) {
    g.Map("GET    /users",     r.List)
    g.Map("POST   /users",     r.Create)
    g.Map("GET    /users/:id", r.Get)
    g.Map("PUT    /users/:id", r.Update)
    g.Map("DELETE /users/:id", r.Delete)
}
```

The pattern format is `"METHOD /path"` — any method string accepted by Go's `net/http.ServeMux`.

---

## Handler signatures

Handlers are plain Go functions. The framework inspects the signature once at registration and compiles an optimised dispatch path.

Supported return patterns:

| Signature | Success | Error |
|---|---|---|
| `func()` | 200 no body | — |
| `func() Response` | status from Response | — |
| `func() T` | 200 + encoded body | — |
| `func() error` | 200 no body | error handler → response |
| `func() (Response, error)` | status from Response | error wins |
| `func() (T, error)` | 200 + encoded body | error wins |

**Nil rules:**
- `func() T` returning `nil` → **204 No Content**
- `func() (T, error)` returning `(nil, nil)` → **204 No Content**
- When the error is non-nil, the data/Response value is ignored

---

## Parameter injection

Parameters are injected into handler arguments following a strict ordering rule.

```go
// Route: POST /orgs/:orgId/users
func (r *Routes) Create(
    ctx    *goserv.Context,              // 1. context
    orgId  int64,                        // 2. path param :orgId (positional)
    filter goserv.Query[UserFilter],     // 3. query string
    req    CreateUserRequest,            // 3. request body
) (*User, error)
```

### Injection order (enforced at registration)

| # | Argument type | Source | Notes |
|---|---|---|---|
| 1 | `*goserv.Context` or `context.Context` | Request context | Must come first; 0 or 1 allowed |
| 2 | goserv-tagged struct (see [Request struct](#request-struct)) | Mixed — each field declares its own source | Checked before positional path params; reads path params by name |
| 2 | Next N non-context, non-tagged args | Path parameters | N = number of `:param` tokens in the route pattern; **positional** |
| 3 | `goserv.Query[T]` | URL query string | T must be a struct; fields read by name |
| 3 | Any other `struct` | Request body | Decoded via the active codec |

Step 3 arguments (query and body) may appear in any order relative to each other. The framework panics at registration if this order is violated, so misconfigurations are caught at startup.

---

## Path parameters

Declare path parameters with a `:name` prefix in the route pattern. The matching handler argument receives the parsed value by **position** — the first path-param argument maps to the first `:param`, and so on, regardless of argument type:

```go
g.Map("GET /items/:id",          func(id int64) (*Item, error) { ... })
g.Map("GET /files/:owner/:repo", func(owner, repo string) (*File, error) { ... })

// Custom decoder — positional matching means the struct goes to :xid, not to body
g.Map("GET /items/:xid", func(id xid.ID) (*Item, error) { ... })
```

### Built-in types

`string`, `bool`, `int`, `int8`, `int16`, `int32`, `int64`, `uint`, `uint8`, `uint16`, `uint32`, `uint64`, `float32`, `float64`

### Constraints

Append `[constraintName]` or `[constraintName:arg]` to a parameter to validate its format before the handler runs:

```go
g.Map("GET /users/:id[uuid]",           handler)  // must be a valid UUID
g.Map("GET /posts/:slug[regex:^[a-z-]+$]", handler) // must match regex
```

The built-in constraints are `uuid` and `regex`. Register custom ones with [`WithPathPattern`](#path-parameter-patterns).

If validation fails, the framework returns **400 Bad Request** automatically — the handler is never called.

---

## Request struct

A request struct aggregates all request inputs into a single handler argument. Each exported field carries a `goserv` tag that declares where its value comes from.

```go
type CreateUserReq struct {
    OrgID  string      `goserv:"fromParam,orgId"`       // r.PathValue("orgId")
    Page   int         `goserv:"fromQuery,page"`         // ?page=
    Token  string      `goserv:"fromHeader,Authorization"` // Authorization header
    Body   UserPayload `goserv:"fromBody"`               // decoded request body
}

// Route: POST /orgs/:orgId/users
func (r *Routes) Create(ctx *goserv.Context, req CreateUserReq) (*User, error) {
    req.OrgID  // from :orgId
    req.Page   // from ?page=
    req.Token  // from Authorization header
    req.Body   // decoded body
}
```

### Tag format

```
goserv:"<source>[,<name>]"
```

| Source | Reads from | On missing value |
|---|---|---|
| `fromParam` | `r.PathValue(name)` | 400 Bad Request |
| `fromQuery` | `r.URL.Query().Get(name)` | zero value (no error) |
| `fromHeader` | `r.Header.Get(name)` | zero value (no error) |
| `fromBody` | decoded request body | 400 Bad Request |

If no explicit `name` is given, the field naming convention is applied to the field name (see [Field naming convention](#field-naming-convention)).

Fields with no `goserv` tag are left at their zero value. Multiple `fromBody` fields on the same struct panic at registration.

### Supported field types

`fromParam`, `fromQuery`, and `fromHeader` support the same types as path parameters — all built-in primitives and any type with a registered [`WithPathParamDecoder`](#path-parameter-decoders).

`fromBody` accepts any type that the active codec can decode into.

---

## Field naming convention

Controls how Go struct field names are converted to their default key when no explicit name is given in a `goserv` or `query` tag. Applies to request struct fields and `Query[T]` fields.

```go
goserv.LowercaseNaming  // "PageSize" → "pagesize"  (default)
goserv.SnakeCaseNaming  // "PageSize" → "page_size"
goserv.CamelCaseNaming  // "PageSize" → "pageSize"
```

Set it server-wide or per-group:

```go
goserv.NewServer(
    goserv.WithFieldNamingConvention(goserv.SnakeCaseNaming),
)

server.RegisterRouteGroup("/api", &Routes{},
    goserv.WithFieldNamingConvention(goserv.CamelCaseNaming),
)
```

You can also pass any `func(string) string` as a custom convention.

---

## Query parameters

Use `goserv.Query[T]` to inject typed query string parameters. `T` must be a struct; each exported field maps to a query parameter.

```go
type ListUsersQuery struct {
    Page   int    `query:"page"`
    Search string `query:"search"`
    Active bool   `query:"active"`
}

func (r *Routes) List(q goserv.Query[ListUsersQuery]) ([]*User, error) {
    q.Value.Page   // from ?page=
    q.Value.Search // from ?search=
    q.Value.Active // from ?active=
    ...
}
```

### Field name resolution

| Priority | Rule |
|---|---|
| 1 | `query` struct tag: `` `query:"page"` `` |
| 2 | Lowercased field name: `Page` → `page` |

A tag of `"-"` skips the field entirely.

### Missing and invalid values

- **Missing** query param → field stays at its zero value (no error)
- **Malformed** value (e.g. `?page=abc` for an `int` field) → **400 Bad Request**

### Supported field types

The same types as path parameters: `string`, `bool`, all `int*`, `uint*`, `float*`, and any type with a registered [`WithPathParamDecoder`](#path-parameter-decoders).

```go
// Register a decoder once on the server:
goserv.WithPathParamDecoder(pathparam.DurationDecoder{})

// Then use time.Duration in both path params and Query[T] fields:
type SearchQuery struct {
    MaxAge time.Duration `query:"max_age"`
}
func (r *Routes) Search(q goserv.Query[SearchQuery]) ([]*Item, error) { ... }
```

---

## Response helpers

Return a `Response` for explicit control over status code and headers. For the happy path, returning your domain type directly is enough.

```go
// 2xx
goserv.Ok(data)          // 200
goserv.Created(data)     // 201
goserv.Accepted(data)    // 202
goserv.NoContent()       // 204

// 3xx
goserv.MovedPermanently(location)  // 301
goserv.Found(location)             // 302
goserv.TemporaryRedirect(location) // 307

// Custom headers (chainable)
goserv.Created(user).
    WithHeader("X-Trace-Id", traceID).
    WithHeader("Location", "/users/"+user.ID)
```

All helpers return `*resultWriter`, which implements `Response`.

---

## Error handling

Return an `*HttpError` as the error value to control the HTTP response exactly:

```go
return nil, goserv.ErrNotFound("user not found")
return nil, goserv.ErrConflict("email already taken").WithCause(dbErr)
return nil, goserv.ErrUnprocessableEntity("invalid input").
    WithDetails(map[string]string{"email": "must be valid"})
```

Available constructors:

```go
goserv.ErrBadRequest(msg)           // 400
goserv.ErrUnauthorized(msg)         // 401
goserv.ErrForbidden(msg)            // 403
goserv.ErrNotFound(msg)             // 404
goserv.ErrConflict(msg)             // 409
goserv.ErrUnprocessableEntity(msg)  // 422
goserv.ErrInternalServer(msg)       // 500
```

`*HttpError` methods:

| Method | Description |
|---|---|
| `.WithCause(err)` | Attaches an underlying error (visible via `errors.Is/As`) |
| `.WithDetails(any)` | Overrides the response body |
| `.WithHeader(key, value)` | Adds a response header |

### Default error response

For `*HttpError`, the response body is an [RFC 7807](https://www.rfc-editor.org/rfc/rfc7807) `ProblemDetails` object:

```json
{
  "type": "about:blank",
  "title": "Not Found",
  "status": 404,
  "detail": "user not found"
}
```

For any other error, the default handler returns `500` with a generic `ProblemDetails`.

### Custom error handler

```go
goserv.WithErrorHandler(func(err error, ctx *goserv.Context) goserv.Response {
    slog.Error("request failed", "err", err, "path", ctx.Request().URL.Path)
    return goserv.DefaultErrorHandler(err, ctx)
})
```

---

## Middleware

Middleware has the signature `func(ctx Context, next Next)`. Call `next(ctx)` to continue the chain, or return early to short-circuit.

```go
func Auth(ctx goserv.Context, next goserv.Next) {
    token := ctx.Header("Authorization")
    if token == "" {
        // write response directly and bail
        ctx.ResponseWriter().WriteHeader(http.StatusUnauthorized)
        return
    }
    next(ctx.Set("userID", parseToken(token)))
}
```

`ctx.Set(key, val)` returns a new `Context` with the value attached — it does not mutate the original.

Register middleware on the server (applies to all routes) or on a group (applies to that group only):

```go
// Server-wide
goserv.WithMiddleware(loggingMiddleware, authMiddleware)

// Group-scoped
server.RegisterRouteGroup("/admin", &AdminRoutes{},
    goserv.WithMiddleware(requireAdmin),
)
```

Execution order: server middleware runs outermost, group middleware runs innermost.

```
server-mw-1 → server-mw-2 → group-mw-1 → handler
```

---

## Codecs

Codecs control how request bodies are decoded and response bodies are encoded.

The JSON codec is registered by default. Add more with `WithInputCodec`, `WithOutputCodec`, or `WithCodec` (for types that implement both):

```go
goserv.WithCodec(myMsgpackCodec)
goserv.WithInputCodec(myXMLInputCodec)
goserv.WithOutputCodec(myXMLOutputCodec)
```

**Input codec** selection is based on the request `Content-Type` header.  
**Output codec** selection is based on the request `Accept` header. If `Accept` is empty, the first registered output codec is used.

---

## Path parameter decoders

To use a custom Go type as a path parameter, register a `ParamDecoder` for it. The decoder is called at request time to convert the raw string into your type.

```go
server := goserv.NewServer(
    goserv.WithPathParamDecoder(pathparam.NewParamDecoder(time.ParseDuration)),
)

// Now time.Duration can be used directly in handler signatures:
g.Map("GET /sleep/:d", func(d time.Duration) { time.Sleep(d) })
```

Use `pathparam.NewParamDecoder` to wrap any `func(string) (T, error)` parse function.

### Built-in decoders (`pathparam` package)

| Decoder | Go type | Format |
|---|---|---|
| `pathparam.DurationDecoder{}` | `time.Duration` | Go duration string (`1h30m`, `500ms`) |
| `pathparam.TimeDecoder{}` | `time.Time` | RFC 3339 (`2006-01-02T15:04:05Z`) |
| `pathparam.IPDecoder{}` | `net.IP` | IPv4 or IPv6 address |
| `pathparam.XIDDecoder{}` | `xid.ID` | [XID](https://github.com/rs/xid) string |

Register them the same way:

```go
goserv.WithPathParamDecoder(pathparam.DurationDecoder{})
goserv.WithPathParamDecoder(pathparam.TimeDecoder{})
goserv.WithPathParamDecoder(pathparam.IPDecoder{})
goserv.WithPathParamDecoder(pathparam.XIDDecoder{})
```

---

## Path parameter patterns

Patterns validate the raw string value of a path parameter **before** type conversion. If validation fails, the request is rejected with 400 — the handler is never invoked.

A pattern is defined by a `PatternFactory`: a function that receives the constraint argument from the route definition and returns a compiled `Pattern`. The factory runs **once at startup**; the `Pattern.Validate` method runs on every request.

```go
type Pattern interface {
    Validate(rawVal string) bool
}

type PatternFactory func(arg string) (Pattern, error)
```

Register a custom pattern with `WithPathPattern`:

```go
server := goserv.NewServer(
    goserv.WithPathPattern("slug", func(arg string) (pathparam.Pattern, error) {
        re := regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
        return &slugPattern{re}, nil
    }),
)

// Reference by name in the route:
g.Map("GET /posts/:name[slug]", handler)
```

### Built-in patterns

| Name | Usage | Validates |
|---|---|---|
| `uuid` | `:id[uuid]` | UUID v1–v8 |
| `regex` | `:id[regex:pattern]` | Custom regular expression |

### Patterns vs. decoders

| | Pattern (`WithPathPattern`) | Decoder (`WithPathParamDecoder`) |
|---|---|---|
| Purpose | Validate format | Parse into a custom Go type |
| Declared in | Route: `:id[uuid]` | Handler signature: `func(d time.Duration)` |
| Handler type | Unchanged (`string`, `int`, …) | Your custom type |
| Runs | Before type conversion | Instead of built-in conversion |

They compose: a decoder handles the type, a pattern validates the format first.
