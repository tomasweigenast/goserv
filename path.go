package goserv

import (
	"fmt"
	"net"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/rs/xid"
)

// ============================================================================
// PathPattern — validates a raw path parameter value
// ============================================================================

// PathPattern validates a raw path parameter value extracted from the URL.
type PathPattern interface {
	Validate(rawVal string) bool
}

// PathPatternFactory builds a PathPattern from an optional argument string.
// Registered by name on the Server (e.g. "uuid", "regex").
// For argument-less patterns, arg is always "".
type PathPatternFactory func(arg string) (PathPattern, error)

// UUIDPattern validates that a path parameter is a well-formed UUID/GUID.
type UUIDPattern struct{}

var uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-([1-8])[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func (UUIDPattern) Validate(rawVal string) bool { return uuidRe.MatchString(rawVal) }

// UUIDPatternFactory is the built-in factory for the "uuid" constraint.
var UUIDPatternFactory PathPatternFactory = func(_ string) (PathPattern, error) {
	return UUIDPattern{}, nil
}

// RegexPattern validates a path parameter against a compiled regular expression.
type RegexPattern struct{ re *regexp.Regexp }

func (p *RegexPattern) Validate(rawVal string) bool { return p.re.MatchString(rawVal) }

// RegexPatternFactory is the built-in factory for the "regex" constraint.
// The argument must be a valid Go regular expression, e.g. :id[regex:^\d+$].
var RegexPatternFactory PathPatternFactory = func(arg string) (PathPattern, error) {
	if arg == "" {
		return nil, fmt.Errorf(`regex constraint requires a pattern argument, e.g. :id[regex:^\d+$]`)
	}
	re, err := regexp.Compile(arg)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", arg, err)
	}
	return &RegexPattern{re: re}, nil
}

// ============================================================================
// PathParamDecoder — typed path parameter decoding
// ============================================================================

// PathParamDecoder decodes a raw string path parameter into a typed value.
// The framework calls Zero() once at registration time to determine which
// parameter type this decoder handles.
//
// Implement on a struct:
//
//	type MyDecoder struct{}
//	func (MyDecoder) Zero() any                      { return MyType{} }
//	func (MyDecoder) Decode(raw string) (any, error) { return parseMyType(raw) }
//
// Or use NewPathParamDecoder to wrap a plain parse function.
type PathParamDecoder interface {
	Zero() any
	Decode(raw string) (any, error)
}

// NewPathParamDecoder wraps a parse function as a PathParamDecoder.
//
//	WithPathParamDecoder(NewPathParamDecoder(time.ParseDuration))
func NewPathParamDecoder[T any](fn func(string) (T, error)) PathParamDecoder {
	return &typedDecoder[T]{fn: fn}
}

type typedDecoder[T any] struct{ fn func(string) (T, error) }

func (*typedDecoder[T]) Zero() any                        { var zero T; return zero }
func (d *typedDecoder[T]) Decode(raw string) (any, error) { return d.fn(raw) }

// ============================================================================
// Built-in decoders
// ============================================================================

// DurationDecoder parses time.Duration path parameters (e.g. "1h30m", "500ms").
type DurationDecoder struct{}

func (DurationDecoder) Zero() any                      { return time.Duration(0) }
func (DurationDecoder) Decode(raw string) (any, error) { return time.ParseDuration(raw) }

// TimeDecoder parses time.Time path parameters in RFC3339 format.
type TimeDecoder struct{}

func (TimeDecoder) Zero() any { return time.Time{} }
func (TimeDecoder) Decode(raw string) (any, error) {
	return time.Parse(time.RFC3339, raw)
}

// IPDecoder parses net.IP path parameters (IPv4 or IPv6).
type IPDecoder struct{}

func (IPDecoder) Zero() any { return net.IP{} }
func (IPDecoder) Decode(raw string) (any, error) {
	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, fmt.Errorf("not a valid IP address")
	}
	return ip, nil
}

// XIDDecoder parses xid.ID path parameters.
type XIDDecoder struct{}

func (XIDDecoder) Zero() any                      { return xid.ID{} }
func (XIDDecoder) Decode(raw string) (any, error) { return xid.FromString(raw) }

// ============================================================================
// Pattern building — :param[constraint] → Go 1.22 ServeMux {param}
// ============================================================================

// paramConstraint holds the parsed name and optional constraint for a path parameter.
type paramConstraint struct {
	name           string // e.g. "id"
	constraintName string // e.g. "uuid", "regex" — empty if none
	constraintArg  string // e.g. "^\d+$" for regex — empty if none
}

// buildPattern combines a group prefix and route path into a full mux pattern.
func buildPattern(prefix, path string) string {
	parts := strings.SplitN(path, " ", 2)
	method, routePath := "", path
	if len(parts) == 2 {
		method = parts[0] + " "
		routePath = parts[1]
	}
	fullPath := strings.ReplaceAll(prefix+routePath, "//", "/")
	return method + fullPath
}

// adaptGo122Pattern converts a route pattern using :param[constraint] syntax into
// a Go 1.22 ServeMux pattern ({param}) and returns the parsed parameter constraints.
//
// Syntax:
//
//	:name                  — plain parameter, no constraint
//	:name[uuid]            — named constraint without argument
//	:name[regex:^pattern$] — named constraint with argument
func adaptGo122Pattern(pattern string) (string, []paramConstraint) {
	var params []paramConstraint
	segments := strings.Split(pattern, "/")
	for i, seg := range segments {
		if !strings.HasPrefix(seg, ":") {
			continue
		}
		pc := parseParamSegment(seg[1:])
		params = append(params, pc)
		segments[i] = "{" + pc.name + "}"
	}
	return strings.Join(segments, "/"), params
}

// parseParamSegment parses a path segment (without the leading ":") into a paramConstraint.
//
//	"id"              → {name:"id"}
//	"id[uuid]"        → {name:"id", constraintName:"uuid"}
//	"id[regex:^\d+$]" → {name:"id", constraintName:"regex", constraintArg:"^\d+$"}
func parseParamSegment(seg string) paramConstraint {
	name, rest, hasConstraint := strings.Cut(seg, "[")
	if !hasConstraint {
		return paramConstraint{name: name}
	}
	spec := strings.TrimSuffix(rest, "]")
	constraintName, constraintArg, _ := strings.Cut(spec, ":")
	return paramConstraint{name: name, constraintName: constraintName, constraintArg: constraintArg}
}

// ============================================================================
// Path parameter setters — compiled once at registration, zero-branch at runtime
// ============================================================================

// makeParamSetter encodes the validate→parse→set scaffold shared by every built-in kind.
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

// buildPathParamSetter returns a pre-compiled setter for a single path parameter.
// Custom decoders are checked first; the built-in kind switch handles primitives.
func buildPathParamSetter(idx int, typ reflect.Type, name string, pattern PathPattern, decoders map[reflect.Type]PathParamDecoder) (argSetter, error) {
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
