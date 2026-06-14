package task

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/bestruirui/octopus/internal/codexprovider"
	"github.com/bestruirui/octopus/internal/helper"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/op"
	"github.com/bestruirui/octopus/internal/utils/log"
	"github.com/looplj/axonhub/llm/httpclient"
	"github.com/looplj/axonhub/llm/oauth"
	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

func RefreshCodexOAuthTokensTask() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	channels, err := op.ChannelList(ctx)
	if err != nil {
		log.Warnf("failed to list channels for codex oauth refresh: %v", err)
		return
	}

	now := time.Now()
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for _, channel := range channels {
		if channel.Type != model.ChannelTypeCodexOAuth {
			continue
		}
		for _, key := range channel.Keys {
			if !shouldRefreshCodexOAuthKey(channel, key, now) {
				continue
			}
			select {
			case <-ctx.Done():
				wg.Wait()
				return
			case sem <- struct{}{}:
			}

			wg.Add(1)
			go func(key model.ChannelKey) {
				defer wg.Done()
				defer func() { <-sem }()
				if err := refreshCodexOAuthKey(ctx, key); err != nil {
					log.Warnf("failed to refresh codex oauth token for channel key %d: %v", key.ID, err)
				}
			}(key)
		}
	}
	wg.Wait()
}

func shouldRefreshCodexOAuthKey(channel model.Channel, key model.ChannelKey, now time.Time) bool {
	return channel.Type == model.ChannelTypeCodexOAuth &&
		key.Enabled &&
		key.CodexAccessToken != "" &&
		key.CodexRefreshToken != "" &&
		key.CodexTokenExpiry < now.Add(24*time.Hour).Unix()
}

func refreshCodexOAuthKey(ctx context.Context, key model.ChannelKey) error {
	creds := helper.CodexCredentialsFromChannelKey(key)
	if creds == nil || creds.AccessToken == "" {
		return fmt.Errorf("codex oauth key not configured")
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

	// Type assert to access EnsureFresh method
	if tp, ok := provider.(interface {
		EnsureFresh(context.Context, time.Duration) (string, error)
	}); ok {
		_, err := tp.EnsureFresh(ctx, 24*time.Hour)
		return err
	}
	return fmt.Errorf("provider does not support EnsureFresh")
}
