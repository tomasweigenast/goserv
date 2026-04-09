package goserv

import (
	"io"
	"net/http"
	"strconv"

	"github.com/tomasweigenast/goserv/codec"
)

// Response represents the result of a request handler.
// Use as an escape hatch when you need a non-200 status or custom headers —
// for the happy path, return your domain type directly and the framework
// wraps it in a 200.
type Response interface {
	StatusCode() int
	Headers() map[string]string
	Data() any
}

type resultWriter struct {
	statusCode int
	headers    map[string]string
	data       any
}

func (w *resultWriter) StatusCode() int            { return w.statusCode }
func (w *resultWriter) Headers() map[string]string { return w.headers }
func (w *resultWriter) Data() any                  { return w.data }

// WithHeader adds or replaces a response header. Safe to chain:
//
//	return http.Created(user).WithHeader("X-Request-Id", id)
func (w *resultWriter) WithHeader(key, value string) *resultWriter {
	if w.headers == nil {
		w.headers = make(map[string]string, 1)
	}
	w.headers[key] = value
	return w
}

// ============================================================================
// 2xx Success
// ============================================================================

// Ok returns a 200 response with an optional body.
func Ok(data ...any) *resultWriter {
	if len(data) == 0 {
		return &resultWriter{statusCode: http.StatusOK}
	}
	return &resultWriter{statusCode: http.StatusOK, data: data[0]}
}

// Created returns a 201 response with an optional body.
func Created(data ...any) *resultWriter {
	if len(data) == 0 {
		return &resultWriter{statusCode: http.StatusCreated}
	}
	return &resultWriter{statusCode: http.StatusCreated, data: data[0]}
}

// Accepted returns a 202 response with an optional body.
func Accepted(data ...any) *resultWriter {
	if len(data) == 0 {
		return &resultWriter{statusCode: http.StatusAccepted}
	}
	return &resultWriter{statusCode: http.StatusAccepted, data: data[0]}
}

// NoContent returns a 204 response with no body.
func NoContent() *resultWriter {
	return &resultWriter{statusCode: http.StatusNoContent}
}

// ============================================================================
// 3xx Redirection
// ============================================================================

func MovedPermanently(location string) *resultWriter {
	return &resultWriter{statusCode: http.StatusMovedPermanently, headers: map[string]string{"Location": location}}
}

func Found(location string) *resultWriter {
	return &resultWriter{statusCode: http.StatusFound, headers: map[string]string{"Location": location}}
}

func TemporaryRedirect(location string) *resultWriter {
	return &resultWriter{statusCode: http.StatusTemporaryRedirect, headers: map[string]string{"Location": location}}
}

// ============================================================================
// Internal helpers
// ============================================================================

func errResponse(status int, msg string) Response {
	return problemDetails(status, msg)
}

func selectOutputCodec(codecs []codec.OutputCodec, accept string) codec.OutputCodec {
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

func writeResponse(w http.ResponseWriter, r *http.Request, res Response, codecs []codec.OutputCodec) {
	for k, v := range res.Headers() {
		w.Header().Set(k, v)
	}

	data := res.Data()
	if data == nil {
		w.WriteHeader(res.StatusCode())
		return
	}

	c := selectOutputCodec(codecs, r.Header.Get("Accept"))
	if c == nil {
		w.WriteHeader(http.StatusNotAcceptable)
		return
	}

	h := w.Header()
	if h.Get("Content-Type") == "" {
		h.Set("Content-Type", c.ContentType())
	}

	if buf, ok := c.(codec.BufferedOutputCodec); ok {
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
		w.WriteHeader(res.StatusCode())
		if err := c.Encode(w, data); err != nil {
			_ = err
		}
	}
}
