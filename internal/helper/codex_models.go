package helper

import (
	_ "embed"
	"encoding/json"
)

//go:embed codex_client_models.json
var codexClientModelsJSON []byte

// CodexModelsForPlan returns slugs whose available_in_plans contains planType AND visibility=="list".
// If planType=="" returns all visibility=="list" slugs (conservative full set).
func CodexModelsForPlan(planType string) []string {
	var catalog struct {
		Models []struct {
			Slug             string   `json:"slug"`
			Visibility       string   `json:"visibility"`
			AvailableInPlans []string `json:"available_in_plans"`
		} `json:"models"`
	}
	if err := json.Unmarshal(codexClientModelsJSON, &catalog); err != nil {
		return nil
	}

	models := make([]string, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if model.Slug == "" || model.Visibility != "list" {
			continue
		}
		if planType == "" || containsString(model.AvailableInPlans, planType) {
			models = append(models, model.Slug)
		}
	}
	return models
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
