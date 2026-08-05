package model

import (
	"math"
	"strings"
	"testing"

	"github.com/looplj/axonhub/llm"
)

func validChannelForValidation() Channel {
	return Channel{
		Name:     " primary ",
		Type:     llm.APIFormatOpenAIChatCompletion,
		BaseUrls: []BaseUrl{{URL: " https://example.com/v1 ", Delay: 0}},
		Keys:     []ChannelKey{{Enabled: true, ChannelKey: " secret "}},
	}
}

func TestValidateChannelNormalizesAndAcceptsValidInput(t *testing.T) {
	channel := validChannelForValidation()
	if err := ValidateChannel(&channel); err != nil {
		t.Fatalf("ValidateChannel() error = %v", err)
	}
	if channel.Name != "primary" || channel.BaseUrls[0].URL != "https://example.com/v1" || channel.Keys[0].ChannelKey != "secret" {
		t.Fatalf("channel was not normalized: %#v", channel)
	}
	if channel.PolicyProfile != ChannelPolicyStandard {
		t.Fatalf("default policy profile = %q, want standard", channel.PolicyProfile)
	}
}

func TestValidateChannelFirstTokenTimeoutExceptionRequiresExplicitOptIn(t *testing.T) {
	channel := validChannelForValidation()
	channel.FirstTokenTimeoutExceptionEnabled = true
	channel.FirstTokenTimeoutExceptionSeconds = 200
	if err := ValidateChannel(&channel); err != nil {
		t.Fatalf("ValidateChannel() rejected channel exception: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*Channel)
	}{
		{name: "enabled at hard ceiling", mutate: func(c *Channel) {
			c.FirstTokenTimeoutExceptionEnabled = true
			c.FirstTokenTimeoutExceptionSeconds = HardMaxInitialResponseTimeoutSeconds
		}},
		{name: "enabled without seconds", mutate: func(c *Channel) { c.FirstTokenTimeoutExceptionEnabled = true }},
		{name: "above maximum", mutate: func(c *Channel) {
			c.FirstTokenTimeoutExceptionSeconds = MaxChannelFirstTokenTimeoutExceptionSeconds + 1
		}},
		{name: "negative seconds", mutate: func(c *Channel) { c.FirstTokenTimeoutExceptionSeconds = -1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := validChannelForValidation()
			test.mutate(&invalid)
			if err := ValidateChannel(&invalid); err == nil {
				t.Fatal("ValidateChannel() accepted invalid channel exception")
			}
		})
	}
}

func TestValidateChannelUpdateFirstTokenTimeoutException(t *testing.T) {
	enabled := true
	seconds := 200
	if err := ValidateChannelUpdate(&ChannelUpdateRequest{
		ID:                                1,
		FirstTokenTimeoutExceptionEnabled: &enabled,
		FirstTokenTimeoutExceptionSeconds: &seconds,
	}); err != nil {
		t.Fatalf("ValidateChannelUpdate() rejected valid channel exception: %v", err)
	}
	if err := ValidateChannelUpdate(&ChannelUpdateRequest{ID: 1, FirstTokenTimeoutExceptionEnabled: &enabled}); err == nil {
		t.Fatal("ValidateChannelUpdate() accepted enabled exception without seconds")
	}
	tooShort := HardMaxInitialResponseTimeoutSeconds
	if err := ValidateChannelUpdate(&ChannelUpdateRequest{
		ID:                                1,
		FirstTokenTimeoutExceptionEnabled: &enabled,
		FirstTokenTimeoutExceptionSeconds: &tooShort,
	}); err == nil {
		t.Fatal("ValidateChannelUpdate() accepted exception at the hard ceiling")
	}
}

func TestValidateChannelAcceptsOfficialCodexOAuthCredential(t *testing.T) {
	channel := Channel{
		Name:     "codex-oauth",
		Type:     ChannelTypeOpenAICodex,
		BaseUrls: []BaseUrl{{URL: " https://chatgpt.com/backend-api/codex/ "}},
		Keys: []ChannelKey{{Enabled: true, ChannelKey: `{
			"type":"codex","access_token":"header.payload.signature","refresh_token":"refresh",
			"account_id":"account","expired":"2099-01-01T00:00:00Z","custom_metadata":{"source":"import"}
		}`}},
	}
	if err := ValidateChannel(&channel); err != nil {
		t.Fatalf("ValidateChannel() error = %v", err)
	}
	if channel.BaseUrls[0].URL != "https://chatgpt.com/backend-api/codex/" {
		t.Fatalf("validated base URL = %q", channel.BaseUrls[0].URL)
	}
}

func TestValidateChannelRejectsUnsafeCodexOAuthConfiguration(t *testing.T) {
	validCredential := `{"type":"codex","access_token":"header.payload.signature","refresh_token":"refresh","expired":"2099-01-01T00:00:00Z"}`
	tests := []struct {
		name       string
		baseURL    string
		credential string
	}{
		{name: "arbitrary host", baseURL: "https://attacker.example/backend-api/codex", credential: validCredential},
		{name: "insecure scheme", baseURL: "http://chatgpt.com/backend-api/codex", credential: validCredential},
		{name: "query parameters", baseURL: "https://chatgpt.com/backend-api/codex?target=other", credential: validCredential},
		{name: "custom port", baseURL: "https://chatgpt.com:444/backend-api/codex", credential: validCredential},
		{name: "fragment", baseURL: "https://chatgpt.com/backend-api/codex#other", credential: validCredential},
		{name: "plain API key", baseURL: "https://chatgpt.com/backend-api/codex", credential: "sk-not-an-oauth-document"},
		{name: "wrong credential type", baseURL: "https://chatgpt.com/backend-api/codex", credential: `{"type":"openai","access_token":"token"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := Channel{
				Name: "codex-oauth", Type: ChannelTypeOpenAICodex,
				BaseUrls: []BaseUrl{{URL: test.baseURL}},
				Keys:     []ChannelKey{{Enabled: true, ChannelKey: test.credential}},
			}
			if err := ValidateChannel(&channel); err == nil {
				t.Fatal("ValidateChannel() accepted unsafe Codex OAuth configuration")
			}
		})
	}
}

func TestValidateChannelRejectsUnsafeShapes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Channel)
	}{
		{name: "blank name", mutate: func(c *Channel) { c.Name = " " }},
		{name: "unknown type", mutate: func(c *Channel) { c.Type = "unknown" }},
		{name: "unknown policy profile", mutate: func(c *Channel) { c.PolicyProfile = "reckless" }},
		{name: "missing URL", mutate: func(c *Channel) { c.BaseUrls = nil }},
		{name: "URL credentials", mutate: func(c *Channel) { c.BaseUrls[0].URL = "https://user:pass@example.com" }},
		{name: "duplicate URL", mutate: func(c *Channel) { c.BaseUrls = append(c.BaseUrls, BaseUrl{URL: "https://EXAMPLE.com/v1"}) }},
		{name: "negative delay", mutate: func(c *Channel) { c.BaseUrls[0].Delay = -1 }},
		{name: "missing key", mutate: func(c *Channel) { c.Keys = nil }},
		{name: "blank key", mutate: func(c *Channel) { c.Keys[0].ChannelKey = " " }},
		{name: "negative limit", mutate: func(c *Channel) { c.RPMLimit = -1 }},
		{name: "unknown proxy scheme", mutate: func(c *Channel) { value := "file:///tmp/socket"; c.ChannelProxy = &value }},
		{name: "non object override", mutate: func(c *Channel) { value := `[]`; c.ParamOverride = &value }},
		{name: "header injection", mutate: func(c *Channel) { c.CustomHeader = []CustomHeader{{HeaderKey: "X-Test", HeaderValue: "ok\r\nbad"}} }},
		{name: "legacy authentication header", mutate: func(c *Channel) { c.CustomHeader = []CustomHeader{{HeaderKey: "Authorization", HeaderValue: "secret"}} }},
		{name: "protected advanced header", mutate: func(c *Channel) { c.HeaderRules = []HeaderRule{{Action: "remove", HeaderKey: "x-api-key"}} }},
		{name: "unknown header action", mutate: func(c *Channel) { c.HeaderRules = []HeaderRule{{Action: "merge", HeaderKey: "X-Test"}} }},
		{name: "header append injection", mutate: func(c *Channel) {
			c.HeaderRules = []HeaderRule{{Action: "append", HeaderKey: "X-Test", HeaderValue: "ok\nbad"}}
		}},
		{name: "invalid JSON pointer", mutate: func(c *Channel) {
			value := `1`
			c.JSONRewriteRules = []JSONRewriteRule{{Action: "override", Path: "temperature", Value: &value}}
		}},
		{name: "missing JSON value", mutate: func(c *Channel) { c.JSONRewriteRules = []JSONRewriteRule{{Action: "override", Path: "/temperature"}} }},
		{name: "invalid JSON value", mutate: func(c *Channel) {
			value := `{`
			c.JSONRewriteRules = []JSONRewriteRule{{Action: "override", Path: "/temperature", Value: &value}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			channel := validChannelForValidation()
			test.mutate(&channel)
			if err := ValidateChannel(&channel); err == nil {
				t.Fatal("ValidateChannel() error = nil")
			}
		})
	}
}

func TestValidateChannelNormalizesAdvancedRewriteRules(t *testing.T) {
	value := ` {"enabled": true} `
	ignored := `"remove ignores this"`
	channel := validChannelForValidation()
	channel.HeaderRules = []HeaderRule{
		{Action: " APPEND ", HeaderKey: " X-Trace ", HeaderValue: "audit"},
		{Action: "REMOVE", HeaderKey: "X-Old", HeaderValue: "ignored"},
	}
	channel.JSONRewriteRules = []JSONRewriteRule{
		{Action: " OVERRIDE ", Path: " /options ", Value: &value},
		{Action: "REMOVE", Path: "/messages/0/internal", Value: &ignored},
	}

	if err := ValidateChannel(&channel); err != nil {
		t.Fatalf("ValidateChannel() error = %v", err)
	}
	if channel.HeaderRules[0].Action != "append" || channel.HeaderRules[0].HeaderKey != "X-Trace" {
		t.Fatalf("header rules were not normalized: %#v", channel.HeaderRules)
	}
	if channel.HeaderRules[1].HeaderValue != "" {
		t.Fatalf("remove header retained value: %#v", channel.HeaderRules[1])
	}
	if channel.JSONRewriteRules[0].Action != "override" || *channel.JSONRewriteRules[0].Value != `{"enabled": true}` {
		t.Fatalf("JSON rules were not normalized: %#v", channel.JSONRewriteRules)
	}
	if channel.JSONRewriteRules[1].Value != nil {
		t.Fatalf("remove JSON rule retained value: %#v", channel.JSONRewriteRules[1])
	}
}

func TestValidateChannelRejectsRewriteCapacityLimits(t *testing.T) {
	channel := validChannelForValidation()
	channel.HeaderRules = make([]HeaderRule, MaxHeaderRules+1)
	if err := ValidateChannel(&channel); err == nil {
		t.Fatal("too many header rules accepted")
	}

	channel = validChannelForValidation()
	channel.JSONRewriteRules = make([]JSONRewriteRule, MaxJSONRewriteRules+1)
	if err := ValidateChannel(&channel); err == nil {
		t.Fatal("too many JSON rewrite rules accepted")
	}
}

func TestValidateChannelUpdateRejectsInvalidKeyDiff(t *testing.T) {
	key := "replacement"
	req := ChannelUpdateRequest{
		ID:             1,
		KeysToUpdate:   []ChannelKeyUpdateRequest{{ID: 7, ChannelKey: &key}},
		KeysToDelete:   []int{7},
		RPMLimit:       intValidationPointer(0),
		MaxConcurrency: intValidationPointer(0),
	}
	if err := ValidateChannelUpdate(&req); err == nil {
		t.Fatal("ValidateChannelUpdate() accepted overlapping key operations")
	}
}

func TestValidateGroupNormalizesDefaultWeight(t *testing.T) {
	group := Group{
		Name: " group ",
		Mode: GroupModeWeighted,
		Items: []GroupItem{{
			Type:      GroupItemTypeChannel,
			ChannelID: 4,
			ModelName: " model ",
			Priority:  1,
		}},
	}
	if err := ValidateGroup(&group); err != nil {
		t.Fatalf("ValidateGroup() error = %v", err)
	}
	if group.Name != "group" || group.Items[0].ModelName != "model" || group.Items[0].Weight != 1 {
		t.Fatalf("group was not normalized: %#v", group)
	}
}

func TestValidateGroupRejectsInvalidRangesAndItems(t *testing.T) {
	tests := []Group{
		{Name: "", Mode: GroupModeRoundRobin},
		{Name: "group", Mode: 0},
		{Name: "group", Mode: GroupModeRoundRobin, FirstTokenTimeOut: -1},
		{Name: "group", Mode: GroupModeRoundRobin, SessionKeepTime: MaxSessionKeepSeconds + 1},
		{Name: "group", Mode: GroupModeWeighted, Items: []GroupItem{{Type: GroupItemTypeChannel, ChannelID: 1, ModelName: "m", Weight: -1}}},
		{Name: "group", Mode: GroupModeRoundRobin, Items: []GroupItem{{Type: GroupItemTypeGroup, TargetGroupID: 0, Weight: 1}}},
	}
	for i := range tests {
		if err := ValidateGroup(&tests[i]); err == nil {
			t.Fatalf("ValidateGroup(case %d) error = nil", i)
		}
	}
}

func TestValidateGroupUpdatePreservesOmittedFields(t *testing.T) {
	disabled := true
	req := GroupUpdateRequest{ID: 1, ItemsToUpdate: []GroupItemUpdateRequest{{ID: 9, Disabled: &disabled}}}
	if err := ValidateGroupUpdate(&req); err != nil {
		t.Fatalf("ValidateGroupUpdate() error = %v", err)
	}
	if req.ItemsToUpdate[0].Priority != nil || req.ItemsToUpdate[0].Weight != nil {
		t.Fatal("omitted update fields were populated")
	}
}

func TestValidateAPIKeyNormalizesModelsAndRejectsInvalidNumbers(t *testing.T) {
	key := APIKey{Name: " key ", ExpireAt: MaxUnixTimestamp, MaxCost: 10, SupportedModels: " gpt-5,claude-opus "}
	if err := ValidateAPIKey(&key); err != nil {
		t.Fatalf("ValidateAPIKey() error = %v", err)
	}
	if key.Name != "key" || key.SupportedModels != "gpt-5,claude-opus" {
		t.Fatalf("API key was not normalized: %#v", key)
	}
	for name, invalid := range map[string]APIKey{
		"blank name":       {Name: " "},
		"negative expiry":  {Name: "key", ExpireAt: -1},
		"NaN cost":         {Name: "key", MaxCost: math.NaN()},
		"negative cost":    {Name: "key", MaxCost: -1},
		"duplicate models": {Name: "key", SupportedModels: "GPT-5,gpt-5"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAPIKey(&invalid); err == nil {
				t.Fatal("ValidateAPIKey() error = nil")
			}
		})
	}
}

func TestValidateLLMInfoNormalizesAndRejectsInvalidPrice(t *testing.T) {
	info := LLMInfo{Name: " GPT-5 ", LLMPrice: LLMPrice{Input: 1}}
	if err := ValidateLLMInfo(&info); err != nil {
		t.Fatalf("ValidateLLMInfo() error = %v", err)
	}
	if info.Name != "gpt-5" {
		t.Fatalf("name = %q, want gpt-5", info.Name)
	}
	for _, invalid := range []LLMInfo{
		{Name: strings.Repeat("x", MaxModelNameBytes+1)},
		{Name: "bad,name"},
		{Name: "model", LLMPrice: LLMPrice{Output: -1}},
		{Name: "model", LLMPrice: LLMPrice{CacheRead: math.Inf(1)}},
	} {
		if err := ValidateLLMInfo(&invalid); err == nil {
			t.Fatalf("ValidateLLMInfo(%#v) error = nil", invalid)
		}
	}
}

func intValidationPointer(value int) *int {
	return &value
}
