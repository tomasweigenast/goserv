package goserv

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"

	"github.com/tomasweigenast/goserv/codec"
	"github.com/tomasweigenast/goserv/pathparam"
)

// RequestHandler is a typed handler convenience alias.
type RequestHandler func(r *Context) Response

// RouteDefiner ensures groups passed to RegisterRouteGroup have the Routes method.
type RouteDefiner interface {
	Routes(g *RouteGroup)
}

// routeConfig holds codec and middleware state shared by Server and RouteGroup.
type routeConfig struct {
	inputCodecs  []codec.InputCodec
	outputCodecs []codec.OutputCodec
	middlewares  []Middleware
	logger       *slog.Logger
}

// RouteGroup is a set of routes sharing a common prefix, codec list, and middleware chain.
type RouteGroup struct {
	routeConfig
	s      *Server
	prefix string
}

// Map registers handler under the given path pattern within this group.
//
// Supported handler signatures:
//
//	func()
//	func() Response
//	func() T
//	func() error
//	func() (Response, error)
//	func() (T, error)
//
// Parameters are injected by type:
//   - *Context or context.Context — the request context
//   - any struct type — decoded from the request body
//   - primitives (string, int*, uint*, float*, bool) — extracted from path parameters
func (g *RouteGroup) Map(path string, handler any) {
	handlerVal := reflect.ValueOf(handler)
	handlerType := handlerVal.Type()

	if handlerType.Kind() != reflect.Func {
		panic(fmt.Sprintf("handler must be a function, got %T", handler))
	}

	fullPattern := buildPattern(g.prefix, path)
	muxPattern, params := adaptGo122Pattern(fullPattern)

	numIn := handlerType.NumIn()

	// Capture codec lists once at registration — zero field-lookup overhead per request.
	inputCodecs := g.inputCodecs
	outputCodecs := g.outputCodecs

	// Compile one setter closure per parameter — zero branching at request time.
	setters := make([]argSetter, numIn)
	paramIdx := 0

	for i := range numIn {
		inTyp := handlerType.In(i)

		switch {
		case inTyp == ourContextType || inTyp == stdContextType:
			setters[i] = func(args []reflect.Value, _ http.ResponseWriter, r *http.Request) (bool, Response) {
				ctx, _ := r.Context().Value(contextKey{}).(Context)
				args[i] = reflect.ValueOf(&ctx)
				return true, nil
			}

		case inTyp.Kind() == reflect.Struct:
			bodyType := inTyp
			setters[i] = func(args []reflect.Value, _ http.ResponseWriter, r *http.Request) (bool, Response) {
				ic := codec.SelectInputCodec(inputCodecs, r.Header.Get("Content-Type"))
				if ic == nil {
					return false, errResponse(http.StatusUnsupportedMediaType, "unsupported media type")
				}
				ptr := reflect.New(bodyType)
				if err := ic.Decode(r.Body, ptr.Interface()); err != nil {
					return false, errResponse(http.StatusBadRequest, "invalid request body")
				}
				args[i] = ptr.Elem()
				return true, nil
			}

		case paramIdx < len(params):
			pc := params[paramIdx]
			paramIdx++

			var pattern pathparam.Pattern
			if pc.ConstraintName != "" {
				factory, ok := g.s.pathPatterns[pc.ConstraintName]
				if !ok {
					panic(fmt.Sprintf("Map %s: unknown path pattern %q for parameter %q", path, pc.ConstraintName, pc.Name))
				}
				var err error
				pattern, err = factory(pc.ConstraintArg)
				if err != nil {
					panic(fmt.Sprintf("Map %s: invalid path pattern %q for parameter %q: %v", path, pc.ConstraintName, pc.Name, err))
				}
			}

			setter, err := buildPathParamSetter(i, inTyp, pc.Name, pattern, g.s.pathParamDecoders)
			if err != nil {
				panic(fmt.Sprintf("Map %s: unsupported path parameter type for %q: %v", path, pc.Name, err))
			}
			setters[i] = setter
		}
	}

	// Pre-compute zero values once — copied into the pooled slice to reset it each request.
	baseArgs := make([]reflect.Value, numIn)
	for i := range numIn {
		baseArgs[i] = reflect.Zero(handlerType.In(i))
	}

	// Pool []reflect.Value to avoid a heap allocation per request.
	argsPool := &sync.Pool{
		New: func() any {
			s := make([]reflect.Value, numIn)
			copy(s, baseArgs)
			return &s
		},
	}

	retPattern := resolveReturnPattern(path, handlerType)
	errHandler := g.s.errorHandler

	httpHandler := func(w http.ResponseWriter, r *http.Request) {
		argsPtr := argsPool.Get().(*[]reflect.Value)
		args := *argsPtr
		copy(args, baseArgs)

		for _, set := range setters {
			if set == nil {
				continue
			}
			if ok, errRes := set(args, w, r); !ok {
				writeResponse(w, r, errRes, outputCodecs)
				argsPool.Put(argsPtr)
				return
			}
		}

		results := handlerVal.Call(args)
		argsPool.Put(argsPtr)

		switch retPattern {
		case returnsResponse:
			if res, ok := results[0].Interface().(Response); ok && res != nil {
				writeResponse(w, r, res, outputCodecs)
				return
			}
		case returnsData:
			if !isNilValue(results[0]) {
				writeResponse(w, r, &resultWriter{statusCode: http.StatusOK, data: results[0].Interface()}, outputCodecs)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		case returnsError:
			if err, ok := results[0].Interface().(error); ok && err != nil {
				writeResponse(w, r, errHandler(err, &Context{ctx: r.Context(), r: r, w: w}), outputCodecs)
				return
			}
		case returnsResponseAndError:
			if err, ok := results[1].Interface().(error); ok && err != nil {
				writeResponse(w, r, errHandler(err, &Context{ctx: r.Context(), r: r, w: w}), outputCodecs)
				return
			}
			if res, ok := results[0].Interface().(Response); ok && res != nil {
				writeResponse(w, r, res, outputCodecs)
				return
			}
		case returnsDataAndError:
			if err, ok := results[1].Interface().(error); ok && err != nil {
				writeResponse(w, r, errHandler(err, &Context{ctx: r.Context(), r: r, w: w}), outputCodecs)
				return
			}
			if !isNilValue(results[0]) {
				writeResponse(w, r, &resultWriter{statusCode: http.StatusOK, data: results[0].Interface()}, outputCodecs)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		w.WriteHeader(http.StatusOK)
	}

	// Stash the final Context (after all middleware) into r so the argSetter
	// can retrieve it directly without re-creating one from scratch.
	routeNext := Next(func(ctx Context) {
		r := ctx.r.WithContext(context.WithValue(ctx.r.Context(), contextKey{}, ctx))
		httpHandler(ctx.w, r)
	})

	// Build pipeline: routeNext ← group middleware (inner) ← server middleware (outer).
	chain := routeNext
	for i := len(g.middlewares) - 1; i >= 0; i-- {
		mw := g.middlewares[i]
		nextInChain := chain
		chain = func(ctx Context) { mw(ctx, nextInChain) }
	}
	for i := len(g.s.middlewares) - 1; i >= 0; i-- {
		mw := g.s.middlewares[i]
		nextInChain := chain
		chain = func(ctx Context) { mw(ctx, nextInChain) }
	}

	// Single Context creation per request — after the mux has set path values on r.
	muxHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chain(Context{ctx: r.Context(), r: r, w: w})
	})

	g.s.mux.Handle(muxPattern, muxHandler)

	// Register the slash-stripped variant to avoid ServeMux redirect from /foo → /foo/.
	if trimmed, ok := strings.CutSuffix(muxPattern, "/"); ok && strings.Contains(trimmed, "/") {
		g.s.mux.Handle(trimmed, muxHandler)
	}
}

// ============================================================================
// Handler reflection
// ============================================================================

// Package-level type descriptors — evaluated once at startup.
var (
	ourContextType = reflect.TypeFor[*Context]()
	stdContextType = reflect.TypeFor[context.Context]()
	responseType   = reflect.TypeFor[Response]()
	errorType      = reflect.TypeFor[error]()
)

// returnPattern describes what a handler function returns.
// Resolved once at registration time; never inspected at request time.
type returnPattern int

const (
	returnsNothing          returnPattern = iota
	returnsResponse                       // func(...) Response
	returnsData                           // func(...) T  (not Response, not error)
	returnsError                          // func(...) error
	returnsResponseAndError               // func(...) (Response, error)
	returnsDataAndError                   // func(...) (T, error)
)

// argSetter is compiled once per parameter slot at route-registration time.
// At request time it injects the correct value into args[i] with no branching.
// Returns (false, errResponse) if injection fails (e.g. malformed path parameter).
type argSetter func(args []reflect.Value, w http.ResponseWriter, r *http.Request) (ok bool, errResponse Response)

// resolveReturnPattern inspects a handler's return types at registration time.
// Panics on unsupported signatures so misconfigurations are caught at startup.
func resolveReturnPattern(path string, t reflect.Type) returnPattern {
	switch t.NumOut() {
	case 0:
		return returnsNothing
	case 1:
		out := t.Out(0)
		switch {
		case out.Implements(responseType):
			return returnsResponse
		case out.Implements(errorType):
			return returnsError
		default:
			return returnsData
		}
	case 2:
		out1 := t.Out(1)
		if !out1.Implements(errorType) {
			panic(fmt.Sprintf("Map %s: second return value must implement error, got %s", path, out1))
		}
		if t.Out(0).Implements(responseType) {
			return returnsResponseAndError
		}
		return returnsDataAndError
	default:
		panic(fmt.Sprintf("Map %s: handler must return at most 2 values", path))
	}
}

func isNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return v.IsNil()
	}
	return false
}

// ============================================================================
// Pattern parsing
// ============================================================================

// paramConstraint holds the parsed name and optional constraint for a path parameter segment.
type paramConstraint struct {
	Name           string
	ConstraintName string
	ConstraintArg  string
}

// buildPattern joins the group prefix and route path, normalising double slashes.
// If the route path starts with an HTTP method (e.g. "GET /foo"), the method is preserved.
func buildPattern(prefix, routePath string) string {
	parts := strings.SplitN(routePath, " ", 2)
	method, path := "", routePath
	if len(parts) == 2 {
		method = parts[0] + " "
		path = parts[1]
	}
	fullPath := strings.ReplaceAll(prefix+path, "//", "/")
	return method + fullPath
}

// adaptGo122Pattern converts colon-style path parameters (":name", ":id[uuid]")
// into the Go 1.22 ServeMux wildcard syntax ("{name}") and returns the extracted
// constraint metadata in declaration order.
func adaptGo122Pattern(pattern string) (string, []paramConstraint) {
	var params []paramConstraint
	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		if !strings.HasPrefix(seg, ":") {
			continue
		}
		pc := parseParamSegment(seg[1:])
		params = append(params, pc)
		segments[i] = "{" + pc.Name + "}"
	}
	return strings.Join(segments, "/"), params
}

func parseParamSegment(seg string) paramConstraint {
	name, rest, hasConstraint := strings.Cut(seg, "[")
	if !hasConstraint {
		return paramConstraint{Name: name}
	}
	spec := strings.TrimSuffix(rest, "]")
	constraintName, constraintArg, _ := strings.Cut(spec, ":")
	return paramConstraint{Name: name, ConstraintName: constraintName, ConstraintArg: constraintArg}
}

// ============================================================================
// Path parameter injection
// ============================================================================

// buildPathParamSetter returns an argSetter that extracts and type-converts the
// named path parameter for injection into handler argument slot idx.
// An optional Pattern is applied for format validation before type conversion.
func buildPathParamSetter(idx int, typ reflect.Type, name string, pattern pathparam.Pattern, decoders map[reflect.Type]pathparam.ParamDecoder) (argSetter, error) {
	validate := func(string) bool { return true }
	if pattern != nil {
		validate = pattern.Validate
	}
	validErrMsg := "invalid path parameter '" + name + "': does not match expected format"
	typeErrMsg := func(t string) string { return "invalid path parameter '" + name + "': expected " + t }

	if dec, ok := decoders[typ]; ok {
		return func(args []reflect.Value, _ http.ResponseWriter, r *http.Request) (bool, Response) {
			rawVal := r.PathValue(name)
			if !validate(rawVal) {
				return false, errResponse(http.StatusBadRequest, validErrMsg)
			}
			val, err := dec.Decode(rawVal)
			if err != nil {
				return false, errResponse(http.StatusBadRequest, "invalid path parameter '"+name+"': "+err.Error())
			}
			args[idx] = reflect.ValueOf(val)
			return true, nil
		}, nil
	}

	switch typ.Kind() {
	case reflect.String:
		return makeParamSetter(idx, name, validate, validErrMsg, "", func(s string) (string, error) { return s, nil }), nil
	case reflect.Bool:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("bool"), strconv.ParseBool), nil
	case reflect.Int:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("int"), func(s string) (int, error) {
			v, err := strconv.ParseInt(s, 10, 0)
			return int(v), err
		}), nil
	case reflect.Int8:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("int8"), func(s string) (int8, error) {
			v, err := strconv.ParseInt(s, 10, 8)
			return int8(v), err
		}), nil
	case reflect.Int16:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("int16"), func(s string) (int16, error) {
			v, err := strconv.ParseInt(s, 10, 16)
			return int16(v), err
		}), nil
	case reflect.Int32:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("int32"), func(s string) (int32, error) {
			v, err := strconv.ParseInt(s, 10, 32)
			return int32(v), err
		}), nil
	case reflect.Int64:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("int64"), func(s string) (int64, error) {
			return strconv.ParseInt(s, 10, 64)
		}), nil
	case reflect.Uint:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("uint"), func(s string) (uint, error) {
			v, err := strconv.ParseUint(s, 10, 0)
			return uint(v), err
		}), nil
	case reflect.Uint8:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("uint8"), func(s string) (uint8, error) {
			v, err := strconv.ParseUint(s, 10, 8)
			return uint8(v), err
		}), nil
	case reflect.Uint16:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("uint16"), func(s string) (uint16, error) {
			v, err := strconv.ParseUint(s, 10, 16)
			return uint16(v), err
		}), nil
	case reflect.Uint32:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("uint32"), func(s string) (uint32, error) {
			v, err := strconv.ParseUint(s, 10, 32)
			return uint32(v), err
		}), nil
	case reflect.Uint64:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("uint64"), func(s string) (uint64, error) {
			return strconv.ParseUint(s, 10, 64)
		}), nil
	case reflect.Float32:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("float32"), func(s string) (float32, error) {
			v, err := strconv.ParseFloat(s, 32)
			return float32(v), err
		}), nil
	case reflect.Float64:
		return makeParamSetter(idx, name, validate, validErrMsg, typeErrMsg("float64"), func(s string) (float64, error) {
			return strconv.ParseFloat(s, 64)
		}), nil
	default:
		return nil, fmt.Errorf("unsupported kind: %s", typ.Kind())
	}
}

func makeParamSetter[T any](idx int, name string, validate func(string) bool, validErrMsg, typeErrMsg string, parse func(string) (T, error)) argSetter {
	return func(args []reflect.Value, _ http.ResponseWriter, r *http.Request) (bool, Response) {
		rawVal := r.PathValue(name)
		if !validate(rawVal) {
			return false, errResponse(http.StatusBadRequest, validErrMsg)
		}
		parsed, err := parse(rawVal)
		if err != nil {
			return false, errResponse(http.StatusBadRequest, typeErrMsg)
		}
		args[idx] = reflect.ValueOf(parsed)
		return true, nil
	}
}
