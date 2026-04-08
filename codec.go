package goserv

import (
	"io"
	"net/http"
	"strconv"
)

// InputCodec decodes a request body into a Go value.
// The active codec is selected by matching the request Content-Type header.
type InputCodec interface {
	// CanDecode reports whether this codec handles the given Content-Type value.
	// The full header value is passed, including any parameters (e.g. "application/json; charset=utf-8").
	CanDecode(contentType string) bool
	// Decode reads from r and populates dst.
	Decode(r io.Reader, dst any) error
}

// OutputCodec encodes a Go value into a response body.
// The active codec is selected by matching the request Accept header.
type OutputCodec interface {
	// CanEncode reports whether this codec can satisfy the given Accept value.
	// The full header value is passed, including q-factors and parameters.
	CanEncode(accept string) bool
	// ContentType returns the MIME type this codec produces.
	ContentType() string
	// Encode writes the serialized form of data to w. Called after response headers
	// are committed. Use for streaming large or dynamically generated payloads.
	Encode(w io.Writer, data any) error
}

// BufferedOutputCodec is an optional extension of OutputCodec.
// When a codec implements this interface the framework calls Marshal instead
// of Encode, which lets it fully materialize the body before writing any headers.
// This enables a correct Content-Length header and avoids chunked transfer encoding.
// Codecs that do NOT implement this interface will produce chunked responses.
type BufferedOutputCodec interface {
	OutputCodec
	// Marshal serializes data and returns the encoded bytes.
	// Called before any response headers are written, so errors can still
	// produce a proper error response with the correct status code.
	Marshal(data any) ([]byte, error)
}

// selectInputCodec returns the first InputCodec that can handle contentType,
// or the first registered codec when contentType is empty (no Content-Type header).
// Returns nil if no codec matches, which should produce a 415 response.
func selectInputCodec(codecs []InputCodec, contentType string) InputCodec {
	if contentType == "" && len(codecs) > 0 {
		return codecs[0]
	}
	for _, c := range codecs {
		if c.CanDecode(contentType) {
			return c
		}
	}
	return nil
}

// selectOutputCodec returns the first OutputCodec that can satisfy accept,
// or the first registered codec when accept is empty (no Accept header).
// Returns nil if no codec matches, which should produce a 406 response.
func selectOutputCodec(codecs []OutputCodec, accept string) OutputCodec {
	if accept == "" && len(codecs) > 0 {
		return codecs[0]
	}
	for _, c := range codecs {
		if c.CanEncode(accept) {
			return c
		}
	}
	return nil
}

func writeResponse(w http.ResponseWriter, r *http.Request, res Response, codecs []OutputCodec) {
	for k, v := range res.Headers() {
		w.Header().Set(k, v)
	}

	data := res.Data()
	if data == nil {
		w.WriteHeader(res.StatusCode())
		return
	}

	codec := selectOutputCodec(codecs, r.Header.Get("Accept"))
	if codec == nil {
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}

	h := w.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", codec.ContentType())
	}

	if buf, ok := codec.(BufferedOutputCodec); ok {
		// Buffered: marshal first, set Content-Length, then write.
		// Headers are not yet committed — encoding errors still produce a clean response.
		b, err := buf.Marshal(data)
		if err != nil {
			h.Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":"internal server error encoding response"}`)
			return
		}
		h.Set("Content-Length", strconv.Itoa(len(b)))
		w.WriteHeader(res.StatusCode())
		_, _ = w.Write(b)
	} else {
		// Streaming: write directly to the response; Go's http package will use
		// chunked transfer encoding automatically.
		w.WriteHeader(res.StatusCode())
		if err := codec.Encode(w, data); err != nil {
			// Headers already committed — cannot change the status code.
			_ = err
		}
	}
}
