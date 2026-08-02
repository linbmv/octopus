package helper

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
)

func TestCodexModelsForPlanFiltersUnsupportedModels(t *testing.T) {
	free := codexModelsForPlan("free")
	if !containsModel(free, "gpt-5.5") || !containsModel(free, "gpt-5.4-mini") {
		t.Fatalf("free models = %v", free)
	}
	if containsModel(free, "gpt-5.4") || containsModel(free, "gpt-5.6-sol") {
		t.Fatalf("free models contain known unsupported models: %v", free)
	}

	goPlan := codexModelsForPlan("go")
	if !containsModel(goPlan, "gpt-5.4") || !containsModel(goPlan, "gpt-5.6-terra") {
		t.Fatalf("go models = %v", goPlan)
	}
	if containsModel(goPlan, "gpt-5.6-sol") {
		t.Fatalf("go models contain Plus-only model: %v", goPlan)
	}

	plusPlan := codexModelsForPlan("plus")
	if !containsModel(plusPlan, "gpt-5.6-sol") {
		t.Fatalf("plus models omit supported model: %v", plusPlan)
	}
	if !containsModel(codexModelsForPlan("unknown"), "gpt-5.3-codex") {
		t.Fatal("unknown plan should preserve the static catalog")
	}
}

func TestFetchModelsUsesHighestEnabledCodexPlan(t *testing.T) {
	freeToken := codexTestToken(t, "free")
	goToken := codexTestToken(t, "go")
	models, err := FetchModels(context.Background(), model.Channel{
		Type: llm.APIFormat("openai/codex"),
		Keys: []model.ChannelKey{
			{Enabled: true, ChannelKey: freeToken},
			{Enabled: true, ChannelKey: goToken},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !containsModel(models, "gpt-5.4") || containsModel(models, "gpt-5.6-sol") {
		t.Fatalf("models = %v", models)
	}
}

func containsModel(models []string, target string) bool {
	for _, model := range models {
		if model == target {
			return true
		}
	}
	return false
}

func codexTestToken(t *testing.T, plan string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"https://api.openai.com/auth": map[string]string{"chatgpt_plan_type": plan},
	})
	if err != nil {
		t.Fatal(err)
	}
	return `{"type":"codex","access_token":"` +
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) +
		`.signature","refresh_token":"refresh"}`
}
