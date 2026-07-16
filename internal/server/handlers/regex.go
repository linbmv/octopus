package handlers

import (
	"fmt"

	"github.com/bestruirui/octopus/internal/helper"
)

func validateModelMatchRegex(pattern string) error {
	if pattern == "" {
		return nil
	}
	if _, err := helper.CompileModelRegex(pattern); err != nil {
		return fmt.Errorf("match_regex is invalid: %w", err)
	}
	return nil
}

func validateOptionalModelMatchRegex(pattern *string) error {
	if pattern == nil {
		return nil
	}
	return validateModelMatchRegex(*pattern)
}
