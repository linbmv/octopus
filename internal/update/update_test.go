package update

import "testing"

func TestIsSelfUpdateEnabled(t *testing.T) {
	t.Setenv("OCTOPUS_SELF_UPDATE_ENABLED", "")
	if IsSelfUpdateEnabled() {
		t.Fatal("expected self update disabled by default")
	}

	for _, value := range []string{"1", "true", "TRUE", "yes", "on"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("OCTOPUS_SELF_UPDATE_ENABLED", value)
			if !IsSelfUpdateEnabled() {
				t.Fatalf("expected self update enabled for %q", value)
			}
		})
	}

	t.Setenv("OCTOPUS_SELF_UPDATE_ENABLED", "false")
	if IsSelfUpdateEnabled() {
		t.Fatal("expected self update disabled for false")
	}
}
