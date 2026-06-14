package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/bestruirui/octopus/internal/codexprovider"
	"github.com/bestruirui/octopus/internal/helper"
	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

func codexTokenProviderForKey(key dbmodel.ChannelKey) (oauth.TokenGetter, error) {
	creds := helper.CodexCredentialsFromChannelKey(key)
	if creds == nil || creds.AccessToken == "" {
		return nil, fmt.Errorf("codex oauth key not configured")
	}
	oldAccessToken := key.CodexAccessToken

	provider := codexprovider.GetOrCreate(key.ID, key, func() oauth.TokenGetter {
		return codex.NewTokenProvider(codex.TokenProviderParams{
			Credentials: creds,
			HTTPClient:  httpclient.NewHttpClient(),
			OnRefreshed: func(ctx context.Context, refreshed *oauth.OAuthCredentials) error {
				data := helper.CodexAuthDataFromCredentials(refreshed)
				if data == nil {
					return fmt.Errorf("codex oauth refreshed credentials are nil")
				}
				err := op.ChannelKeyCodexCredentialsUpdate(
					key.ID,
					key.ChannelID,
					oldAccessToken,
					data.AccessToken,
					data.RefreshToken,
					data.IDToken,
					data.AccountID,
					data.PlanType,
					data.Email,
					data.ExpiresAt,
					ctx,
				)
				if err == nil {
					codexprovider.InvalidateByKey(key.ID, oldAccessToken)
				}
				return err
			},
		})
	})
	return provider, nil
}

func codexChannelKeyByID(channel *dbmodel.Channel, keyID int) dbmodel.ChannelKey {
	if channel == nil || keyID == 0 || len(channel.Keys) == 0 {
		return dbmodel.ChannelKey{}
	}
	nowSec := time.Now().Unix()
	for _, key := range channel.Keys {
		if key.ID == keyID && codexChannelKeyAvailable(key, nowSec) {
			return key
		}
	}
	return dbmodel.ChannelKey{}
}

func codexChannelKey(channel *dbmodel.Channel) dbmodel.ChannelKey {
	if channel == nil || len(channel.Keys) == 0 {
		return dbmodel.ChannelKey{}
	}
	nowSec := time.Now().Unix()

	best := dbmodel.ChannelKey{}
	bestExpiry := int64(0)
	bestSet := false
	for _, key := range channel.Keys {
		if !codexChannelKeyAvailable(key, nowSec) {
			continue
		}
		// Prefer keys with longer remaining token validity (expiry furthest in future)
		// to avoid selecting soon-to-expire tokens that trigger immediate refresh
		if !bestSet || key.CodexTokenExpiry > bestExpiry {
			best = key
			bestExpiry = key.CodexTokenExpiry
			bestSet = true
		}
	}
	if !bestSet {
		return dbmodel.ChannelKey{}
	}
	return best
}

func codexChannelKeyAvailable(key dbmodel.ChannelKey, nowSec int64) bool {
	if !key.Enabled || key.CodexAccessToken == "" {
		return false
	}
	if key.StatusCode == 429 && key.LastUseTimeStamp > 0 {
		return nowSec-key.LastUseTimeStamp >= int64(5*time.Minute/time.Second)
	}
	return true
}
