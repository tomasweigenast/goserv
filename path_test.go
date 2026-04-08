package goserv

import (
	"testing"
)

// ============================================================================
// buildPattern
// ============================================================================

func TestBuildPattern_MethodAndPath(t *testing.T) {
	got := buildPattern("/api", "GET /users")
	if got != "GET /api/users" {
		t.Errorf("got %q, want %q", got, "GET /api/users")
	}
}

func TestBuildPattern_NoMethod(t *testing.T) {
	got := buildPattern("/api", "/users")
	if got != "/api/users" {
		t.Errorf("got %q, want %q", got, "/api/users")
	}
}

func TestBuildPattern_CollapseDoubleSlash(t *testing.T) {
	got := buildPattern("/api/", "/users")
	if got != "/api/users" {
		t.Errorf("got %q, want %q", got, "/api/users")
	}
}

func TestBuildPattern_EmptyPrefix(t *testing.T) {
	got := buildPattern("", "GET /users")
	if got != "GET /users" {
		t.Errorf("got %q, want %q", got, "GET /users")
	}
}

// ============================================================================
// adaptGo122Pattern
// ============================================================================

func TestAdaptGo122Pattern_PlainParam(t *testing.T) {
	pattern, params := adaptGo122Pattern("GET /users/:id")
	if pattern != "GET /users/{id}" {
		t.Errorf("pattern = %q, want %q", pattern, "GET /users/{id}")
	}
	if len(params) != 1 || params[0].name != "id" || params[0].constraintName != "" {
		t.Errorf("params = %+v", params)
	}
}

func TestAdaptGo122Pattern_ConstraintNoArg(t *testing.T) {
	pattern, params := adaptGo122Pattern("GET /users/:id[uuid]")
	if pattern != "GET /users/{id}" {
		t.Errorf("pattern = %q, want %q", pattern, "GET /users/{id}")
	}
	if params[0].constraintName != "uuid" || params[0].constraintArg != "" {
		t.Errorf("params = %+v", params)
	}
}

func TestAdaptGo122Pattern_ConstraintWithArg(t *testing.T) {
	_, params := adaptGo122Pattern("GET /items/:id[regex:^\\d+$]")
	if params[0].constraintName != "regex" || params[0].constraintArg != `^\d+$` {
		t.Errorf("params = %+v", params)
	}
}

func TestAdaptGo122Pattern_MultipleParams(t *testing.T) {
	pattern, params := adaptGo122Pattern("GET /a/:x/b/:y")
	if pattern != "GET /a/{x}/b/{y}" {
		t.Errorf("pattern = %q", pattern)
	}
	if len(params) != 2 || params[0].name != "x" || params[1].name != "y" {
		t.Errorf("params = %+v", params)
	}
}

func TestAdaptGo122Pattern_NoParams(t *testing.T) {
	pattern, params := adaptGo122Pattern("GET /users")
	if pattern != "GET /users" {
		t.Errorf("pattern = %q", pattern)
	}
	if len(params) != 0 {
		t.Errorf("expected no params, got %+v", params)
	}
}

// ============================================================================
// parseParamSegment
// ============================================================================

func TestParseParamSegment_Plain(t *testing.T) {
	pc := parseParamSegment("id")
	if pc.name != "id" || pc.constraintName != "" || pc.constraintArg != "" {
		t.Errorf("got %+v", pc)
	}
}

func TestParseParamSegment_ConstraintNoArg(t *testing.T) {
	pc := parseParamSegment("id[uuid]")
	if pc.name != "id" || pc.constraintName != "uuid" || pc.constraintArg != "" {
		t.Errorf("got %+v", pc)
	}
}

func TestParseParamSegment_ConstraintWithArg(t *testing.T) {
	pc := parseParamSegment(`id[regex:^\d+$]`)
	if pc.name != "id" || pc.constraintName != "regex" || pc.constraintArg != `^\d+$` {
		t.Errorf("got %+v", pc)
	}
}

// ============================================================================
// UUIDPattern
// ============================================================================

func TestUUIDPattern_Valid(t *testing.T) {
	p := UUIDPattern{}
	valid := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"550E8400-E29B-41D4-A716-446655440000", // uppercase
	}
	for _, v := range valid {
		if !p.Validate(v) {
			t.Errorf("expected valid UUID: %q", v)
		}
	}
}

func TestUUIDPattern_Invalid(t *testing.T) {
	p := UUIDPattern{}
	invalid := []string{
		"not-a-uuid",
		"550e8400-e29b-41d4-a716",
		"",
		"550e8400e29b41d4a716446655440000", // no dashes
	}
	for _, v := range invalid {
		if p.Validate(v) {
			t.Errorf("expected invalid UUID: %q", v)
		}
	}
}

// ============================================================================
// RegexPattern
// ============================================================================

func TestRegexPattern_Match(t *testing.T) {
	p, err := RegexPatternFactory(`^\d+$`)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Validate("123") {
		t.Error("expected 123 to match")
	}
	if p.Validate("abc") {
		t.Error("expected abc not to match")
	}
}

func TestRegexPatternFactory_EmptyArgError(t *testing.T) {
	_, err := RegexPatternFactory("")
	if err == nil {
		t.Error("expected error for empty arg")
	}
}

func TestRegexPatternFactory_InvalidRegexError(t *testing.T) {
	_, err := RegexPatternFactory(`[invalid`)
	if err == nil {
		t.Error("expected error for invalid regex")
	}
}
