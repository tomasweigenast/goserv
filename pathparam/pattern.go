package pathparam

import (
	"fmt"
	"regexp"
)

type Pattern interface {
	Validate(rawVal string) bool
}

type PatternFactory func(arg string) (Pattern, error)

var uuidRe = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-([1-8])[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

type uuidPattern struct{}

func (uuidPattern) Validate(rawVal string) bool { return uuidRe.MatchString(rawVal) }

var UUIDPatternFactory PatternFactory = func(_ string) (Pattern, error) {
	return uuidPattern{}, nil
}

type regexPattern struct{ re *regexp.Regexp }

func (p *regexPattern) Validate(rawVal string) bool { return p.re.MatchString(rawVal) }

var RegexPatternFactory PatternFactory = func(arg string) (Pattern, error) {
	if arg == "" {
		return nil, fmt.Errorf(`regex constraint requires a pattern argument, e.g. :id[regex:^\d+$]`)
	}
	re, err := regexp.Compile(arg)
	if err != nil {
		return nil, fmt.Errorf("invalid regex %q: %w", arg, err)
	}
	return &regexPattern{re: re}, nil
}
