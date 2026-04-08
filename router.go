package goserv

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync"
)

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
				codec := selectInputCodec(inputCodecs, r.Header.Get("Content-Type"))
				if codec == nil {
					return false, errResponse(http.StatusUnsupportedMediaType, "unsupported media type")
				}
				ptr := reflect.New(bodyType)
				if err := codec.Decode(r.Body, ptr.Interface()); err != nil {
					return false, errResponse(http.StatusBadRequest, "invalid request body")
				}
				args[i] = ptr.Elem()
				return true, nil
			}

		case paramIdx < len(params):
			pc := params[paramIdx]
			paramIdx++

			var pattern PathPattern
			if pc.constraintName != "" {
				factory, ok := g.s.pathPatterns[pc.constraintName]
				if !ok {
					panic(fmt.Sprintf("Map %s: unknown path pattern %q for parameter %q", path, pc.constraintName, pc.name))
				}
				var err error
				pattern, err = factory(pc.constraintArg)
				if err != nil {
					panic(fmt.Sprintf("Map %s: invalid path pattern %q for parameter %q: %v", path, pc.constraintName, pc.name, err))
				}
			}

			setter, err := buildPathParamSetter(i, inTyp, pc.name, pattern, g.s.pathParamDecoders)
			if err != nil {
				panic(fmt.Sprintf("Map %s: unsupported path parameter type for %q: %v", path, pc.name, err))
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

// isNilValue reports whether v is a nil pointer, interface, slice, map, chan, or func.
func isNilValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Slice, reflect.Map, reflect.Chan, reflect.Func:
		return v.IsNil()
	}
	return false
}
