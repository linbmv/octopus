package helper

import (
	"github.com/bestruirui/octopus/internal/codexauth"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm/oauth"
)

type CodexAuthData = codexauth.CodexAuthData

// ParseCodexTokenJSON parses the FLAT CPA/Claude-Code style token json:
// {"id_token","access_token","refresh_token","account_id","last_refresh","email","expired","plan_type",...}
func ParseCodexTokenJSON(raw string) (*CodexAuthData, error) {
	return codexauth.ParseCodexTokenJSON(raw)
}

func ApplyCodexOAuthImport(key *model.ChannelKey) error {
	return codexauth.ApplyCodexOAuthImport(key)
}

// CodexCredentialsFromChannelKey builds oauth.OAuthCredentials from a stored ChannelKey's Codex* fields.
func CodexCredentialsFromChannelKey(key model.ChannelKey) *oauth.OAuthCredentials {
	return codexauth.CodexCredentialsFromChannelKey(key)
}

// CodexAuthDataFromCredentials re-derives CodexAuthData from refreshed creds.
func CodexAuthDataFromCredentials(creds *oauth.OAuthCredentials) *CodexAuthData {
	return codexauth.CodexAuthDataFromCredentials(creds)
}
