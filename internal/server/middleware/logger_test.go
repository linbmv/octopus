package middleware

import "testing"

func TestRedactSensitiveQuery(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"gemini key", "/v1beta/models/gemini:generateContent?key=sk-octopus-secret", "/v1beta/models/gemini:generateContent?key=REDACTED"},
		{"stream token", "/api/v1/log/stream?token=abcdef123456", "/api/v1/log/stream?token=REDACTED"},
		{"mixed with normal params", "/v1beta/models/x:generateContent?alt=sse&key=sk-1", "/v1beta/models/x:generateContent?alt=sse&key=REDACTED"},
		{"case insensitive", "/p?KEY=sk-1", "/p?KEY=REDACTED"},
		{"no query untouched", "/api/v1/group/list", "/api/v1/group/list"},
		{"other params untouched", "/api/v1/log/list?page=2&size=10", "/api/v1/log/list?page=2&size=10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactSensitiveQuery(tc.in); got != tc.want {
				t.Fatalf("redactSensitiveQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
