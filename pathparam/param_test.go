package pathparam

import "testing"

func TestUUIDPattern_Valid(t *testing.T) {
	p := uuidPattern{}
	valid := []string{
		"550e8400-e29b-41d4-a716-446655440000",
		"6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		"550E8400-E29B-41D4-A716-446655440000",
	}
	for _, v := range valid {
		if !p.Validate(v) {
			t.Errorf("expected valid UUID: %q", v)
		}
	}
}

func TestUUIDPattern_Invalid(t *testing.T) {
	p := uuidPattern{}
	invalid := []string{
		"not-a-uuid",
		"550e8400-e29b-41d4-a716",
		"",
		"550e8400e29b41d4a716446655440000",
	}
	for _, v := range invalid {
		if p.Validate(v) {
			t.Errorf("expected invalid UUID: %q", v)
		}
	}
}

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
