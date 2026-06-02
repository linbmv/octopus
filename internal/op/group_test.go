package op

import "testing"

func TestStripModelSuffix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "remove -openai-compact",
			input:    "gpt-5.5-openai-compact",
			expected: "gpt-5.5",
		},
		{
			name:     "remove -openai",
			input:    "gpt-4-openai",
			expected: "gpt-4",
		},
		{
			name:     "remove -compact",
			input:    "claude-opus-compact",
			expected: "claude-opus",
		},
		{
			name:     "remove -anthropic",
			input:    "claude-3-5-sonnet-anthropic",
			expected: "claude-3-5-sonnet",
		},
		{
			name:     "remove -gemini",
			input:    "gemini-pro-gemini",
			expected: "gemini-pro",
		},
		{
			name:     "no suffix to remove",
			input:    "gpt-4-turbo",
			expected: "gpt-4-turbo",
		},
		{
			name:     "suffix in middle not removed",
			input:    "gpt-openai-4",
			expected: "gpt-openai-4",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripModelSuffix(tt.input)
			if result != tt.expected {
				t.Errorf("stripModelSuffix(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
