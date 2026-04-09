package goserv

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/tomasweigenast/goserv/pathparam"
)

// Query[T] injects URL query string parameters into a handler argument.
// T must be a struct; each exported field is read from the query string.
//
// Fields are matched by the `query` struct tag, falling back to the
// lowercased field name. A tag value of "-" skips the field entirely.
//
//	type ListUsersQuery struct {
//	    Page   int    `query:"page"`
//	    Search string `query:"search"`
//	}
//
//	func (r *Routes) List(q goserv.Query[ListUsersQuery]) ([]*User, error) {
//	    q.Value.Page   // from ?page=
//	    q.Value.Search // from ?search=
//	}
//
// Missing query params are left at their zero value. A malformed value
// (e.g. "abc" for an int field) returns 400 Bad Request.
type Query[T any] struct {
	Value T
}

// queryMarker is an unexported interface used to detect Query[T] arguments
// at route-registration time without inspecting the type name.
type queryMarker interface{ queryParam() }

func (Query[T]) queryParam() {} // implements queryMarker

var queryMarkerType = reflect.TypeFor[queryMarker]()

// queryFieldInfo is pre-computed at registration for each exported field of T.
type queryFieldInfo struct {
	index int
	key   string
	typ   reflect.Type
}

// buildQuerySetter compiles an argSetter for a Query[T] argument at slot idx.
// It panics at registration time if T is not a struct.
func buildQuerySetter(idx int, inTyp reflect.Type, decoders map[reflect.Type]pathparam.ParamDecoder, naming FieldNamingConvention) argSetter {
	// inTyp is Query[T]; Value is the first (and only) field.
	innerTyp := inTyp.Field(0).Type
	if innerTyp.Kind() != reflect.Struct {
		panic(fmt.Sprintf("goserv.Query[T]: T must be a struct, got %s", innerTyp))
	}

	fields := make([]queryFieldInfo, 0, innerTyp.NumField())
	for i := range innerTyp.NumField() {
		f := innerTyp.Field(i)
		if !f.IsExported() {
			continue
		}
		key := naming(f.Name)
		if tag := f.Tag.Get("query"); tag != "" {
			if tag == "-" {
				continue
			}
			key = strings.SplitN(tag, ",", 2)[0]
		}
		fields = append(fields, queryFieldInfo{index: i, key: key, typ: f.Type})
	}

	return func(args []reflect.Value, _ http.ResponseWriter, r *http.Request) (bool, Response) {
		ptr := reflect.New(inTyp)
		inner := ptr.Elem().Field(0) // the Value T field

		for _, f := range fields {
			raw := r.URL.Query().Get(f.key)
			if raw == "" {
				continue // missing → zero value, not an error
			}
			val, err := parseRawValue(f.typ, raw, decoders)
			if err != nil {
				return false, errResponse(http.StatusBadRequest,
					"invalid query parameter '"+f.key+"': "+err.Error())
			}
			inner.Field(f.index).Set(val)
		}

		args[idx] = ptr.Elem()
		return true, nil
	}
}
