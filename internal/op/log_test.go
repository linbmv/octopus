package op

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/model"
)

func TestRelayLogServiceTokens(t *testing.T) {
	service := NewRelayLogService()

	token, err := service.StreamTokenCreate()
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	if token == "" {
		t.Fatal("expected token")
	}
	if !service.StreamTokenVerify(token) {
		t.Fatal("expected token to verify")
	}

	service.StreamTokenRevoke(token)
	if service.StreamTokenVerify(token) {
		t.Fatal("expected revoked token to fail verification")
	}
}

func TestRelayLogServiceSubscription(t *testing.T) {
	service := NewRelayLogService()
	ch := service.Subscribe()

	service.notifySubscribers(model.RelayLog{ID: 1})

	select {
	case got := <-ch:
		if got.ID != 1 {
			t.Fatalf("unexpected log id: %d", got.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for relay log notification")
	}

	service.Unsubscribe(ch)
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected unsubscribe to close channel")
		}
	default:
		t.Fatal("expected closed channel")
	}
}
