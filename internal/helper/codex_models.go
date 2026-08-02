package helper

import (
	"strings"

	"github.com/looplj/axonhub/llm/transformer/openai/codex"
)

// codexModelsForPlan narrows the provider's static catalog to models that the
// current Codex account can use. The upstream has no stable /models endpoint,
// so an unknown plan deliberately keeps the full catalog and lets the request
// response remain authoritative.
func codexModelsForPlan(plan string) []string {
	models := codex.DefaultModels()
	plan = strings.ToLower(strings.TrimSpace(plan))
	if plan == "" {
		return models
	}

	freeModels := map[string]struct{}{
		"gpt-5.4-mini":  {},
		"gpt-5.5":       {},
		"gpt-5.6-terra": {},
		"gpt-5.6-luna":  {},
	}
	paidModels := map[string]struct{}{
		"gpt-5.4":       {},
		"gpt-5.4-mini":  {},
		"gpt-5.5":       {},
		"gpt-5.6-terra": {},
		"gpt-5.6-luna":  {},
	}
	plusModels := map[string]struct{}{
		"gpt-5.4":       {},
		"gpt-5.4-mini":  {},
		"gpt-5.5":       {},
		"gpt-5.6-sol":   {},
		"gpt-5.6-terra": {},
		"gpt-5.6-luna":  {},
	}
	var allowed map[string]struct{}
	switch plan {
	case "free", "free_workspace", "k12":
		allowed = freeModels
	case "plus":
		// Verified against the official Codex endpoint with a Plus OAuth
		// credential. Go and Free credentials are rejected for this model.
		allowed = plusModels
	case "pro", "team", "business", "enterprise", "enterprise_cbp_usage_based", "edu", "education", "go", "prolite", "hc", "quorum", "finserv", "self_serve_business_usage_based":
		allowed = paidModels
	default:
		return models
	}

	filtered := make([]string, 0, len(models))
	for _, model := range models {
		if _, ok := allowed[model]; ok {
			filtered = append(filtered, model)
		}
	}
	return filtered
}
