package channelstate

import (
	"testing"
	"time"

	"github.com/bestruirui/octopus/internal/relay/balancer"
	"github.com/bestruirui/octopus/internal/routingstate"
)

func TestInvalidateAllClearsBalancerStateAndPublishesOnce(t *testing.T) {
	InvalidateAll()
	const (
		apiKeyID  = 501
		channelID = 601
		keyID     = 701
		modelName = "restore-model"
	)
	balancer.SetSticky(apiKeyID, modelName, channelID, keyID, modelName)
	balancer.RecordFailure(channelID, keyID, modelName)
	balancer.RecordFailure(channelID, keyID, modelName)
	if tripped, _ := balancer.IsTripped(channelID, keyID, modelName); !tripped {
		t.Fatal("circuit precondition was not established")
	}

	before := routingstate.Current()
	InvalidateAll()
	if sticky := balancer.GetSticky(apiKeyID, modelName, time.Minute); sticky != nil {
		t.Fatalf("bulk invalidation retained sticky state: %#v", sticky)
	}
	if tripped, _ := balancer.IsTripped(channelID, keyID, modelName); tripped {
		t.Fatal("bulk invalidation retained circuit state")
	}
	select {
	case <-before.Changed:
	default:
		t.Fatal("bulk invalidation did not publish routing change")
	}
	after := routingstate.Current()
	if after.Revision != before.Revision+1 {
		t.Fatalf("routing revision advanced %d times, want exactly once", after.Revision-before.Revision)
	}
}
