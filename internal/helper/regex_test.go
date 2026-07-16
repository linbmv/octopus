package helper

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestCompileModelRegexAppliesSafetyPolicy(t *testing.T) {
	re, err := CompileModelRegex(`^gpt-[[:alnum:]._-]+$`)
	if err != nil {
		t.Fatalf("CompileModelRegex returned error: %v", err)
	}
	if re.MatchTimeout != ModelRegexMatchTimeout {
		t.Fatalf("MatchTimeout = %v, want %v", re.MatchTimeout, ModelRegexMatchTimeout)
	}

	_, err = CompileModelRegex(strings.Repeat("a", ModelRegexMaxPatternBytes+1))
	if !errors.Is(err, ErrModelRegexPatternTooLong) {
		t.Fatalf("overlong pattern error = %v, want ErrModelRegexPatternTooLong", err)
	}
}

func TestMatchModelRegexRejectsOverlongInput(t *testing.T) {
	re, err := CompileModelRegex(`.*`)
	if err != nil {
		t.Fatalf("CompileModelRegex returned error: %v", err)
	}
	_, err = MatchModelRegex(re, strings.Repeat("x", ModelRegexMaxInputBytes+1))
	if !errors.Is(err, ErrModelRegexInputTooLong) {
		t.Fatalf("overlong input error = %v, want ErrModelRegexInputTooLong", err)
	}
}

func TestFilterModelsStopsCatastrophicBacktracking(t *testing.T) {
	input := strings.Repeat("a", 2048) + "!"
	started := time.Now()
	_, err := filterModels([]string{input}, `^(a+)+$`)
	if !errors.Is(err, ErrModelRegexMatchTimeout) {
		t.Fatalf("filterModels error = %v, want timeout", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("catastrophic match took %v, want <= 1s", elapsed)
	}
}

func TestAutoGroupRegexUsesBoundedMatcher(t *testing.T) {
	input := strings.Repeat("a", 2048) + "!"
	started := time.Now()
	matched := matchRegex(1, model.Group{ID: 2, MatchRegex: `^(a+)+$`}, []string{input})
	if len(matched) != 0 {
		t.Fatalf("matched models = %v, want none", matched)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("catastrophic auto-group match took %v, want <= 1s", elapsed)
	}
}
