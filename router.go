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
	fieldNaming  FieldNamingConvention
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
	// Injection order (enforced here at registration):
	//   1. *Context / context.Context  — by type, must appear first
	//   2. Path params                 — positional, next len(params) non-context args
	//   3. Query[T] / body struct      — by type, in any order after path params
	setters := make([]argSetter, numIn)
	pathParamsGiven := 0

	for i := range numIn {
		inTyp := handlerType.In(i)

		switch {
		case inTyp == ourContextType || inTyp == stdContextType:
			// Context — always by type, never counts as a path param slot.
			setters[i] = func(args []reflect.Value, _ http.ResponseWriter, r *http.Request) (bool, Response) {
				ctx, _ := r.Context().Value(contextKey{}).(Context)
				args[i] = reflect.ValueOf(&ctx)
				return true, nil
			}

		case inTyp.Kind() == reflect.Struct && isRequestStruct(inTyp):
			// goserv-tagged request struct — reads its own path params by name,
			// so it must be detected before the positional path param case.
			setters[i] = buildRequestStructSetter(i, inTyp, path, g.s.pathParamDecoders, inputCodecs, g.fieldNaming)

		case pathParamsGiven < len(params):
			// Positional path param — consumes the next :param slot.
			pc := params[pathParamsGiven]
			pathParamsGiven++

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

		case inTyp.Implements(queryMarkerType):
			setters[i] = buildQuerySetter(i, inTyp, g.s.pathParamDecoders, g.fieldNaming)

		case inTyp.Kind() == reflect.Struct:
			// Plain struct with no goserv tags — decode from request body.
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

		default:
			panic(fmt.Sprintf("Map %s: argument %d has unsupported type %s after path params are exhausted", path, i, inTyp))
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

	// Probe parseRawValue early to surface unsupported-type errors at
	// registration time rather than at request time.
	// Only unsupportedTypeError means the type has no parser at all;
	// other errors (e.g. parse failure on "") are fine — the type is supported.
	if _, err := parseRawValue(typ, "", decoders); isDecoderMissError(err) {
		return nil, err
	}

	return func(args []reflect.Value, _ http.ResponseWriter, r *http.Request) (bool, Response) {
		rawVal := r.PathValue(name)
		if !validate(rawVal) {
			return false, errResponse(http.StatusBadRequest, validErrMsg)
		}
		val, err := parseRawValue(typ, rawVal, decoders)
		if err != nil {
			return false, errResponse(http.StatusBadRequest, "invalid path parameter '"+name+"': "+err.Error())
		}
		args[idx] = val
		return true, nil
	}, nil
}

// parseRawValue converts a raw string into a reflect.Value of the given type,
// using registered custom decoders first and falling back to built-in primitives.
// It is shared by path param and query param injection.
func parseRawValue(typ reflect.Type, raw string, decoders map[reflect.Type]pathparam.ParamDecoder) (reflect.Value, error) {
	if dec, ok := decoders[typ]; ok {
		val, err := dec.Decode(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(val), nil
	}

	switch typ.Kind() {
	case reflect.String:
		return reflect.ValueOf(raw), nil
	case reflect.Bool:
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v), nil
	case reflect.Int:
		v, err := strconv.ParseInt(raw, 10, 0)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(int(v)), nil
	case reflect.Int8:
		v, err := strconv.ParseInt(raw, 10, 8)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(int8(v)), nil
	case reflect.Int16:
		v, err := strconv.ParseInt(raw, 10, 16)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(int16(v)), nil
	case reflect.Int32:
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(int32(v)), nil
	case reflect.Int64:
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v), nil
	case reflect.Uint:
		v, err := strconv.ParseUint(raw, 10, 0)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(uint(v)), nil
	case reflect.Uint8:
		v, err := strconv.ParseUint(raw, 10, 8)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(uint8(v)), nil
	case reflect.Uint16:
		v, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(uint16(v)), nil
	case reflect.Uint32:
		v, err := strconv.ParseUint(raw, 10, 32)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(uint32(v)), nil
	case reflect.Uint64:
		v, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v), nil
	case reflect.Float32:
		v, err := strconv.ParseFloat(raw, 32)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(float32(v)), nil
	case reflect.Float64:
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return reflect.Value{}, err
		}
		return reflect.ValueOf(v), nil
	default:
		return reflect.Value{}, &unsupportedTypeError{typ: typ}
	}
}

// unsupportedTypeError is returned by parseRawValue for types with no registered decoder.
type unsupportedTypeError struct{ typ reflect.Type }

func (e *unsupportedTypeError) Error() string { return "unsupported kind: " + e.typ.Kind().String() }

// isDecoderMissError reports whether err is an unsupportedTypeError — used to
// distinguish "this type has no parser" (a configuration error) from a parse
// failure on an empty string during the probe in buildPathParamSetter.
func isDecoderMissError(err error) bool {
	_, ok := err.(*unsupportedTypeError)
	return ok
}

// ============================================================================
// Request struct injection
// ============================================================================

// isRequestStruct reports whether typ is a struct where at least one exported
// field carries a "goserv" struct tag. Such structs use per-field source tags
// instead of whole-struct body decoding.
func isRequestStruct(typ reflect.Type) bool {
	for i := range typ.NumField() {
		if typ.Field(i).Tag.Get("goserv") != "" {
			return true
		}
	}
	return false
}

// reqFieldSetter is a compiled setter for one field of a request struct.
type reqFieldSetter func(structVal reflect.Value, r *http.Request) (bool, Response)

// buildRequestStructSetter compiles an argSetter for a goserv-tagged struct at
// slot idx. It panics at registration time for invalid tag values or multiple
// fromBody fields.
func buildRequestStructSetter(
	idx int,
	typ reflect.Type,
	routePath string,
	decoders map[reflect.Type]pathparam.ParamDecoder,
	inputCodecs []codec.InputCodec,
	naming FieldNamingConvention,
) argSetter {
	type fieldMeta struct {
		index  int
		setter reqFieldSetter
	}

	var fieldSetters []fieldMeta
	bodyFieldIdx := -1

	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("goserv")
		if tag == "" {
			continue
		}

		source, name, _ := strings.Cut(tag, ",")
		if name == "" {
			name = naming(f.Name)
		}

		fidx := i
		ftyp := f.Type

		switch source {
		case "fromParam":
			paramName := name
			fieldSetters = append(fieldSetters, fieldMeta{index: fidx, setter: func(sv reflect.Value, r *http.Request) (bool, Response) {
				raw := r.PathValue(paramName)
				val, err := parseRawValue(ftyp, raw, decoders)
				if err != nil {
					return false, errResponse(http.StatusBadRequest, "invalid path parameter '"+paramName+"': "+err.Error())
				}
				sv.Field(fidx).Set(val)
				return true, nil
			}})

		case "fromQuery":
			if ftyp.Kind() == reflect.Struct {
				// Nested struct tagged fromQuery — flatten its fields as individual
				// query parameters. Useful for shared pagination / filter structs.
				fieldSetters = append(fieldSetters, fieldMeta{index: fidx,
					setter: buildNestedQuerySetter(fidx, ftyp, decoders, naming)})
			} else {
				queryKey := name
				fieldSetters = append(fieldSetters, fieldMeta{index: fidx, setter: func(sv reflect.Value, r *http.Request) (bool, Response) {
					raw := r.URL.Query().Get(queryKey)
					if raw == "" {
						return true, nil // missing → zero value
					}
					val, err := parseRawValue(ftyp, raw, decoders)
					if err != nil {
						return false, errResponse(http.StatusBadRequest, "invalid query parameter '"+queryKey+"': "+err.Error())
					}
					sv.Field(fidx).Set(val)
					return true, nil
				}})
			}

		case "fromHeader":
			headerName := name
			fieldSetters = append(fieldSetters, fieldMeta{index: fidx, setter: func(sv reflect.Value, r *http.Request) (bool, Response) {
				raw := r.Header.Get(headerName)
				if raw == "" {
					return true, nil // missing → zero value
				}
				val, err := parseRawValue(ftyp, raw, decoders)
				if err != nil {
					return false, errResponse(http.StatusBadRequest, "invalid header '"+headerName+"': "+err.Error())
				}
				sv.Field(fidx).Set(val)
				return true, nil
			}})

		case "fromBody":
			if bodyFieldIdx != -1 {
				panic(fmt.Sprintf("Map %s: request struct %s has multiple fromBody fields", routePath, typ.Name()))
			}
			bodyFieldIdx = fidx
			bodyType := ftyp
			fieldSetters = append(fieldSetters, fieldMeta{index: fidx, setter: func(sv reflect.Value, r *http.Request) (bool, Response) {
				ic := codec.SelectInputCodec(inputCodecs, r.Header.Get("Content-Type"))
				if ic == nil {
					return false, errResponse(http.StatusUnsupportedMediaType, "unsupported media type")
				}
				ptr := reflect.New(bodyType)
				if err := ic.Decode(r.Body, ptr.Interface()); err != nil {
					return false, errResponse(http.StatusBadRequest, "invalid request body")
				}
				sv.Field(fidx).Set(ptr.Elem())
				return true, nil
			}})

		default:
			panic(fmt.Sprintf("Map %s: unknown goserv tag source %q on field %s.%s", routePath, source, typ.Name(), f.Name))
		}
	}

	return func(args []reflect.Value, _ http.ResponseWriter, r *http.Request) (bool, Response) {
		ptr := reflect.New(typ)
		sv := ptr.Elem()
		for _, fs := range fieldSetters {
			if ok, errRes := fs.setter(sv, r); !ok {
				return false, errRes
			}
		}
		args[idx] = sv
		return true, nil
	}
}

// buildNestedQuerySetter returns a reqFieldSetter that populates the struct at
// field slot idx by reading each of its exported fields from URL query params.
// Fields may carry a bare goserv tag to override the query key name:
//
//	type PageQuery struct {
//	    Page     int    // ?page=
//	    PageSize int    // ?page_size=
//	    Cursor   string `goserv:"after"` // ?after=
//	}
//
// If a sub-field is itself a struct it is recursed into (fully flattened).
func buildNestedQuerySetter(
	idx int,
	typ reflect.Type,
	decoders map[reflect.Type]pathparam.ParamDecoder,
	naming FieldNamingConvention,
) reqFieldSetter {
	type subField struct {
		index    int
		queryKey string
		typ      reflect.Type
		nested   reqFieldSetter // non-nil when typ is a struct
	}

	var fields []subField
	for i := range typ.NumField() {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		// Key name: bare goserv tag overrides the naming convention.
		key := naming(f.Name)
		if tag := f.Tag.Get("goserv"); tag != "" {
			key = tag
		}

		sf := subField{index: i, queryKey: key, typ: f.Type}
		if f.Type.Kind() == reflect.Struct {
			sf.nested = buildNestedQuerySetter(i, f.Type, decoders, naming)
		}
		fields = append(fields, sf)
	}

	return func(sv reflect.Value, r *http.Request) (bool, Response) {
		nested := reflect.New(typ).Elem()
		for _, f := range fields {
			if f.nested != nil {
				if ok, errRes := f.nested(nested, r); !ok {
					return false, errRes
				}
				continue
			}
			raw := r.URL.Query().Get(f.queryKey)
			if raw == "" {
				continue // missing → zero value
			}
			val, err := parseRawValue(f.typ, raw, decoders)
			if err != nil {
				return false, errResponse(http.StatusBadRequest, "invalid query parameter '"+f.queryKey+"': "+err.Error())
			}
			nested.Field(f.index).Set(val)
		}
		sv.Field(idx).Set(nested)
		return true, nil
	}
}
