package goserv_test

import (
	"errors"
	"testing"

	httpx "github.com/tomasweigenast/goserv"
)

func TestHttpError_Error(t *testing.T) {
	err := httpx.ErrNotFound("item not found")
	if err.Error() != "item not found" {
		t.Errorf("got %q, want %q", err.Error(), "item not found")
	}
}

func TestHttpError_Error_WithCause(t *testing.T) {
	cause := errors.New("db timeout")
	err := httpx.ErrNotFound("item not found").WithCause(cause)
	if err.Error() != "item not found: db timeout" {
		t.Errorf("got %q", err.Error())
	}
	if !errors.Is(err, cause) {
		t.Error("errors.Is should find cause")
	}
}

func TestHttpError_DefaultData_IsProblemDetails(t *testing.T) {
	err := httpx.ErrNotFound("item not found")
	pd, ok := err.Data().(*httpx.ProblemDetails)
	if !ok {
		t.Fatalf("Data() should return *ProblemDetails, got %T", err.Data())
	}
	if pd.Status != 404 {
		t.Errorf("Status = %d, want 404", pd.Status)
	}
	if pd.Detail != "item not found" {
		t.Errorf("Detail = %q, want %q", pd.Detail, "item not found")
	}
	if pd.Type != "about:blank" {
		t.Errorf("Type = %q, want %q", pd.Type, "about:blank")
	}
	if pd.Title == "" {
		t.Error("Title should not be empty")
	}
}

func TestHttpError_WithDetails_OverridesProblemDetails(t *testing.T) {
	custom := map[string]string{"field": "required"}
	err := httpx.ErrBadRequest("validation failed").WithDetails(custom)
	if _, ok := err.Data().(map[string]string); !ok {
		t.Errorf("Data() should be the custom details value, got %T", err.Data())
	}
}

func TestHttpError_Headers_ContainsContentType(t *testing.T) {
	err := httpx.ErrBadRequest("bad")
	h := err.Headers()
	if h["Content-Type"] != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", h["Content-Type"])
	}
}

func TestHttpError_WithHeader_MergesWithContentType(t *testing.T) {
	err := httpx.ErrBadRequest("bad").WithHeader("X-Retry-After", "30")
	h := err.Headers()
	if h["Content-Type"] != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", h["Content-Type"])
	}
	if h["X-Retry-After"] != "30" {
		t.Errorf("X-Retry-After = %q, want 30", h["X-Retry-After"])
	}
}

func TestHttpError_StatusCodes(t *testing.T) {
	cases := []struct {
		fn   func(string) *httpx.HttpError
		want int
	}{
		{httpx.ErrBadRequest, 400},
		{httpx.ErrUnauthorized, 401},
		{httpx.ErrForbidden, 403},
		{httpx.ErrNotFound, 404},
		{httpx.ErrConflict, 409},
		{httpx.ErrUnprocessableEntity, 422},
		{httpx.ErrInternalServer, 500},
	}
	for _, c := range cases {
		err := c.fn("msg")
		if err.StatusCode() != c.want {
			t.Errorf("%T: StatusCode() = %d, want %d", err, err.StatusCode(), c.want)
		}
	}
}

func TestDefaultErrorHandler_HttpError(t *testing.T) {
	httpErr := httpx.ErrNotFound("not found")
	res := httpx.DefaultErrorHandler(httpErr, nil)
	if res.StatusCode() != 404 {
		t.Errorf("status = %d, want 404", res.StatusCode())
	}
}

func TestDefaultErrorHandler_PlainError_Returns500ProblemDetails(t *testing.T) {
	res := httpx.DefaultErrorHandler(errors.New("something broke"), nil)
	if res.StatusCode() != 500 {
		t.Errorf("status = %d, want 500", res.StatusCode())
	}
	pd, ok := res.Data().(*httpx.ProblemDetails)
	if !ok {
		t.Fatalf("Data() should be *ProblemDetails, got %T", res.Data())
	}
	if pd.Status != 500 {
		t.Errorf("ProblemDetails.Status = %d, want 500", pd.Status)
	}
}

func TestProblemDetails_Response(t *testing.T) {
	pd := &httpx.ProblemDetails{Type: "about:blank", Title: "Not Found", Status: 404, Detail: "gone"}
	if pd.StatusCode() != 404 {
		t.Errorf("StatusCode() = %d, want 404", pd.StatusCode())
	}
	if pd.Headers()["Content-Type"] != "application/problem+json" {
		t.Error("Headers() should contain application/problem+json")
	}
	if pd.Data() != pd {
		t.Error("Data() should return itself")
	}
}
