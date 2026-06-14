package task

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
)

func TestShouldRefreshCodexOAuthKeySkipsNonRefreshableKeys(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		channel model.Channel
		key     model.ChannelKey
		want    bool
	}{
		{
			name:    "non codex channel",
			channel: model.Channel{Type: llm.APIFormatOpenAIChatCompletion},
			key:     model.ChannelKey{Enabled: true, CodexAccessToken: "token", CodexRefreshToken: "refresh", CodexTokenExpiry: now.Add(time.Hour).Unix()},
		},
		{
			name:    "disabled key",
			channel: model.Channel{Type: model.ChannelTypeCodexOAuth},
			key:     model.ChannelKey{Enabled: false, CodexAccessToken: "token", CodexRefreshToken: "refresh", CodexTokenExpiry: now.Add(time.Hour).Unix()},
		},
		{
			name:    "missing access token",
			channel: model.Channel{Type: model.ChannelTypeCodexOAuth},
			key:     model.ChannelKey{Enabled: true, CodexRefreshToken: "refresh", CodexTokenExpiry: now.Add(time.Hour).Unix()},
		},
		{
			name:    "missing refresh token",
			channel: model.Channel{Type: model.ChannelTypeCodexOAuth},
			key:     model.ChannelKey{Enabled: true, CodexAccessToken: "token", CodexTokenExpiry: now.Add(time.Hour).Unix()},
		},
		{
			name:    "far future expiry",
			channel: model.Channel{Type: model.ChannelTypeCodexOAuth},
			key:     model.ChannelKey{Enabled: true, CodexAccessToken: "token", CodexRefreshToken: "refresh", CodexTokenExpiry: now.Add(25 * time.Hour).Unix()},
		},
		{
			name:    "expires within refresh window",
			channel: model.Channel{Type: model.ChannelTypeCodexOAuth},
			key:     model.ChannelKey{Enabled: true, CodexAccessToken: "token", CodexRefreshToken: "refresh", CodexTokenExpiry: now.Add(time.Hour).Unix()},
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldRefreshCodexOAuthKey(tt.channel, tt.key, now)
			if got != tt.want {
				t.Fatalf("shouldRefreshCodexOAuthKey() = %v, want %v", got, tt.want)
			}
		})
	}
}
