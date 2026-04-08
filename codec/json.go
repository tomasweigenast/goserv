package codec

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// JSONCodec handles JSON encoding and decoding using streaming IO.
// It produces chunked transfer encoding (no Content-Length header).
// Each func field can be swapped independently for any compatible library
// (e.g. sonic, go-json, jsoniter).
//
// Implements: InputCodec, OutputCodec.
type JSONCodec struct {
	// DecodeFn deserializes JSON from r into dst.
	DecodeFn func(r io.Reader, dst any) error
	// EncodeFn serializes data into w.
	EncodeFn func(w io.Writer, data any) error
}

// NewJSONCodec returns a streaming JSONCodec backed by encoding/json.
// Swap func fields to use a different library:
//
//	codec := NewJSONCodec()
//	codec.DecodeFn = func(r io.Reader, dst any) error { return sonic.NewDecoder(r).Decode(dst) }
//	codec.EncodeFn = func(w io.Writer, v any) error   { return sonic.NewEncoder(w).Encode(v) }
func NewJSONCodec() JSONCodec {
	return JSONCodec{
		DecodeFn: func(r io.Reader, dst any) error {
			err := json.NewDecoder(r).Decode(dst)
			if errors.Is(err, io.EOF) {
				return nil // empty body is not an error
			}
			return err
		},
		EncodeFn: func(w io.Writer, data any) error { return json.NewEncoder(w).Encode(data) },
	}
}

func (JSONCodec) CanDecode(contentType string) bool {
	mt, _, _ := strings.Cut(contentType, ";")
	return strings.TrimSpace(mt) == "application/json"
}

func (c JSONCodec) Decode(r io.Reader, dst any) error { return c.DecodeFn(r, dst) }

func (JSONCodec) CanEncode(accept string) bool {
	return accept == "" ||
		strings.Contains(accept, "application/json") ||
		strings.Contains(accept, "*/*")
}

func (JSONCodec) ContentType() string { return "application/json" }

func (c JSONCodec) Encode(w io.Writer, data any) error { return c.EncodeFn(w, data) }

// BufferedJSONCodec extends JSONCodec by materializing the full response body
// before writing any headers, enabling a Content-Length header and avoiding
// chunked transfer encoding.
//
// Implements: InputCodec, BufferedOutputCodec.
type BufferedJSONCodec struct {
	JSONCodec
	MarshalFn func(data any) ([]byte, error)
}

// NewBufferedJSONCodec returns a BufferedJSONCodec backed by encoding/json.
// Swap func fields to use a different library:
//
//	codec := NewBufferedJSONCodec()
//	codec.DecodeFn  = func(r io.Reader, dst any) error { return sonic.NewDecoder(r).Decode(dst) }
//	codec.MarshalFn = sonic.Marshal
func NewBufferedJSONCodec() BufferedJSONCodec {
	return BufferedJSONCodec{
		JSONCodec: NewJSONCodec(),
		MarshalFn: json.Marshal,
	}
}

func (c BufferedJSONCodec) Marshal(data any) ([]byte, error) { return c.MarshalFn(data) }
