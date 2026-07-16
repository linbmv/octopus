package price

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/bestruirui/octopus/internal/utils/bodylimit"
)

func TestGetLLMPriceUsesPresetCaseInsensitively(t *testing.T) {
	price := GetLLMPrice("GPT-4O")
	if price == nil || price.Input != 2.5 || price.Output != 10 {
		t.Fatalf("GetLLMPrice() = %#v", price)
	}
	if GetLLMPrice("definitely-unknown-model") != nil {
		t.Fatal("unknown model unexpectedly had a price")
	}
}

func TestUpdateLLMPrice(t *testing.T) {
	oldURL := llmPriceURL
	t.Cleanup(func() { llmPriceURL = oldURL })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openai":{"models":{"custom":{"id":"CUSTOM-MODEL","cost":{"input":3,"output":7}}}}}`))
	}))
	defer server.Close()
	llmPriceURL = server.URL

	if err := UpdateLLMPrice(context.Background()); err != nil {
		t.Fatalf("UpdateLLMPrice() error = %v", err)
	}
	price := GetLLMPrice("custom-model")
	if price == nil || price.Input != 3 || price.Output != 7 {
		t.Fatalf("updated price = %#v", price)
	}
	if GetLastUpdateTime().IsZero() {
		t.Fatal("last update time was not recorded")
	}
}

func TestUpdateLLMPriceRejectsHTTPAndJSONErrors(t *testing.T) {
	oldURL := llmPriceURL
	t.Cleanup(func() { llmPriceURL = oldURL })
	for name, handler := range map[string]http.HandlerFunc{
		"status": func(w http.ResponseWriter, r *http.Request) { http.Error(w, "failed", http.StatusBadGateway) },
		"JSON":   func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"broken":`)) },
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			llmPriceURL = server.URL
			if err := UpdateLLMPrice(context.Background()); err == nil {
				t.Fatal("UpdateLLMPrice() expected an error")
			}
		})
	}
}

func TestUpdateLLMPriceRejectsOversizedResponseBeforeReading(t *testing.T) {
	oldURL := llmPriceURL
	t.Cleanup(func() { llmPriceURL = oldURL })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(bodylimit.DefaultMetadataResponseBytes+1, 10))
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	llmPriceURL = server.URL

	err := UpdateLLMPrice(context.Background())
	if err == nil || !strings.Contains(err.Error(), bodylimit.ErrTooLarge.Error()) {
		t.Fatalf("UpdateLLMPrice() error = %v, want response size limit", err)
	}
}
