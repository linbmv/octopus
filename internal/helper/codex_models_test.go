package helper

import "testing"

func TestCodexModelsForPlan(t *testing.T) {
	teamModels := CodexModelsForPlan("team")
	if !hasModel(teamModels, "gpt-5.5") {
		t.Fatal("CodexModelsForPlan(team) missing gpt-5.5")
	}
	if !hasModel(teamModels, "gpt-5.4") {
		t.Fatal("CodexModelsForPlan(team) missing gpt-5.4")
	}
	if hasModel(teamModels, "codex-auto-review") {
		t.Fatal("CodexModelsForPlan(team) returned hidden model codex-auto-review")
	}

	freeModels := CodexModelsForPlan("free")
	if !hasModel(freeModels, "gpt-5.5") {
		t.Fatal("CodexModelsForPlan(free) missing gpt-5.5")
	}
	if hasModel(freeModels, "gpt-5.4") {
		t.Fatal("CodexModelsForPlan(free) returned gpt-5.4")
	}
	if hasModel(freeModels, "codex-auto-review") {
		t.Fatal("CodexModelsForPlan(free) returned hidden model codex-auto-review")
	}
}

func hasModel(models []string, slug string) bool {
	for _, model := range models {
		if model == slug {
			return true
		}
	}
	return false
}
