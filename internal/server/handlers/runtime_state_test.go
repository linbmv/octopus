package handlers

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/client"
	"github.com/bestruirui/octopus/internal/model"
	"github.com/bestruirui/octopus/internal/relay/balancer"
)

func TestInvalidateChannelRuntimeStateClearsStickyAndProxyClient(t *testing.T) {
	client.InvalidateAllCustomProxyClients()
	defer client.InvalidateAllCustomProxyClients()

	const (
		channelID = 707
		apiKeyID  = 909
		modelName = "runtime-state-test"
	)
	proxyURL := "http://127.0.0.1:19090"
	firstClient, err := client.GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("GetHTTPClientCustomProxy() error = %v", err)
	}
	balancer.SetSticky(apiKeyID, modelName, channelID, 1, modelName)

	invalidateChannelRuntimeState(channelID, &model.Channel{
		ID:           channelID,
		ChannelProxy: &proxyURL,
	})

	if sticky := balancer.GetSticky(apiKeyID, modelName, time.Minute); sticky != nil {
		t.Fatalf("sticky state remained after channel invalidation: %#v", sticky)
	}
	secondClient, err := client.GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("GetHTTPClientCustomProxy() after invalidation error = %v", err)
	}
	if firstClient == secondClient {
		t.Fatal("channel invalidation reused the stale custom proxy client")
	}
}

func TestInvalidateChannelRuntimeStateWithoutSnapshotClearsProxyCache(t *testing.T) {
	client.InvalidateAllCustomProxyClients()
	defer client.InvalidateAllCustomProxyClients()

	proxyURL := "http://127.0.0.1:19091"
	firstClient, err := client.GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("GetHTTPClientCustomProxy() error = %v", err)
	}

	invalidateChannelRuntimeState(708, nil)

	secondClient, err := client.GetHTTPClientCustomProxy(proxyURL)
	if err != nil {
		t.Fatalf("GetHTTPClientCustomProxy() after fallback invalidation error = %v", err)
	}
	if firstClient == secondClient {
		t.Fatal("missing channel snapshot did not clear the bounded proxy cache")
	}
}
