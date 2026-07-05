package op

import "testing"

func TestDefaultServices(t *testing.T) {
	services := DefaultServices()
	if services.Settings == nil {
		t.Fatal("settings service is nil")
	}
	if services.Users == nil {
		t.Fatal("users service is nil")
	}
	if services.Channels == nil {
		t.Fatal("channels service is nil")
	}
	if services.Groups == nil {
		t.Fatal("groups service is nil")
	}
	if services.Stats == nil {
		t.Fatal("stats service is nil")
	}
	if services.RelayLogs == nil {
		t.Fatal("relay logs service is nil")
	}
}
