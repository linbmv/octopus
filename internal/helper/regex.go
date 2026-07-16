package helper

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dlclark/regexp2"
)

const (
	// ModelRegexMaxPatternBytes bounds parser work for administrator-supplied
	// expressions while remaining far above normal model filters.
	ModelRegexMaxPatternBytes = 1024
	// ModelRegexMaxInputBytes bounds regexp2's rune conversion and backtracking
	// state for model names received from an upstream service.
	ModelRegexMaxInputBytes = 4096
	ModelRegexMatchTimeout  = 100 * time.Millisecond
)

var (
	ErrModelRegexPatternTooLong = errors.New("model regex pattern is too long")
	ErrModelRegexInputTooLong   = errors.New("model regex input is too long")
	ErrModelRegexMatchTimeout   = errors.New("model regex match timed out")
)

// CompileModelRegex is the only supported regexp2 compiler for model matching.
// regexp2 is required for ECMAScript compatibility but is a backtracking engine,
// so every compiled expression must carry a finite timeout.
func CompileModelRegex(pattern string) (*regexp2.Regexp, error) {
	if len(pattern) > ModelRegexMaxPatternBytes {
		return nil, fmt.Errorf("%w: maximum is %d bytes", ErrModelRegexPatternTooLong, ModelRegexMaxPatternBytes)
	}
	re, err := regexp2.Compile(pattern, regexp2.ECMAScript)
	if err != nil {
		return nil, fmt.Errorf("compile model regex: %w", err)
	}
	re.MatchTimeout = ModelRegexMatchTimeout
	return re, nil
}

// MatchModelRegex applies the same input bound at every runtime match site.
func MatchModelRegex(re *regexp2.Regexp, input string) (bool, error) {
	if re == nil {
		return false, errors.New("model regex is nil")
	}
	if len(input) > ModelRegexMaxInputBytes {
		return false, fmt.Errorf("%w: maximum is %d bytes", ErrModelRegexInputTooLong, ModelRegexMaxInputBytes)
	}
	matched, err := re.MatchString(input)
	if err != nil {
		// regexp2 includes the complete input in timeout errors. Return a bounded,
		// stable error instead so upstream-controlled model names do not flood logs.
		if strings.Contains(err.Error(), "match timeout") {
			return false, fmt.Errorf("%w after %s", ErrModelRegexMatchTimeout, re.MatchTimeout)
		}
		return false, fmt.Errorf("match model regex: %w", err)
	}
	return matched, nil
}
