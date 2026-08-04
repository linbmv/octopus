package routingstate

import (
	"testing"
	"time"
)

func TestNotifyClosesPreviousGenerationOnly(t *testing.T) {
	before := Current()
	revision := Notify()
	if revision != before.Revision+1 {
		t.Fatalf("revision = %d, want %d", revision, before.Revision+1)
	}
	select {
	case <-before.Changed:
	case <-time.After(time.Second):
		t.Fatal("previous routing generation was not notified")
	}

	after := Current()
	if after.Revision != revision {
		t.Fatalf("current revision = %d, want %d", after.Revision, revision)
	}
	select {
	case <-after.Changed:
		t.Fatal("current routing generation was already closed")
	default:
	}
}
