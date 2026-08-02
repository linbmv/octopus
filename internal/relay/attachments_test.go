package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	dbmodel "github.com/bestruirui/octopus/internal/model"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

func TestCollectRelayAttachmentTurnsSupportsOpenAIChatFileParts(t *testing.T) {
	turns := collectRelayAttachmentTurns(llm.APIFormatOpenAIChatCompletion, []byte(`{
		"messages":[
			{"role":"system","content":"read"},
			{"role":"user","content":[
				{"type":"text","text":"inspect"},
				{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}
			]}
		]
	}`))
	if len(turns) != 1 || turns[0].Role != "user" || turns[0].RoleOrdinal != 0 || len(turns[0].Files) != 1 {
		t.Fatalf("unexpected attachment turns: %#v", turns)
	}
	file := turns[0].Files[0]
	if file.Filename != "report.pdf" || file.MIMEType != "" || file.Data != "data:application/pdf;base64,JVBERi0=" {
		t.Fatalf("attachment was not preserved: %#v", file)
	}
}

func TestCollectRelayAttachmentTurnsSupportsResponsesInputFile(t *testing.T) {
	turns := collectRelayAttachmentTurns(llm.APIFormatOpenAIResponse, []byte(`{
		"input":[{"type":"message","role":"user","content":[
			{"type":"input_text","text":"inspect"},
			{"type":"input_file","filename":"notes.txt","file_id":"file_123"}
		]}]
	}`))
	if len(turns) != 1 || len(turns[0].Files) != 1 {
		t.Fatalf("unexpected response attachment turns: %#v", turns)
	}
	file := turns[0].Files[0]
	if file.Filename != "notes.txt" || file.ID != "file_123" {
		t.Fatalf("responses file metadata was not preserved: %#v", file)
	}
}

func TestPatchRelayAttachmentsChatToResponses(t *testing.T) {
	turns := collectRelayAttachmentTurns(llm.APIFormatOpenAIChatCompletion, []byte(`{
		"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}]}]
	}`))
	body := []byte(`{"model":"gpt-test","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]}]}`)
	patched, changed, err := patchRelayAttachmentBody(body, llm.APIFormatOpenAIResponse, turns)
	if err != nil || !changed {
		t.Fatalf("patch failed: changed=%v err=%v", changed, err)
	}
	var got map[string]any
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatal(err)
	}
	content := got["input"].([]any)[0].(map[string]any)["content"].([]any)
	file := content[1].(map[string]any)
	if file["type"] != "input_file" || file["filename"] != "report.pdf" || file["file_data"] != "data:application/pdf;base64,JVBERi0=" {
		t.Fatalf("unexpected Responses attachment: %#v", file)
	}
}

func TestPatchRelayAttachmentsResponsesToChatPreservesFileID(t *testing.T) {
	turns := collectRelayAttachmentTurns(llm.APIFormatOpenAIResponse, []byte(`{
		"input":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"notes.txt","file_id":"file_123"}]}]
	}`))
	body := []byte(`{"model":"gpt-test","messages":[{"role":"user","content":"inspect"}]}`)
	patched, changed, err := patchRelayAttachmentBody(body, llm.APIFormatOpenAIChatCompletion, turns)
	if err != nil || !changed {
		t.Fatalf("patch failed: changed=%v err=%v", changed, err)
	}
	var got map[string]any
	if err := json.Unmarshal(patched, &got); err != nil {
		t.Fatal(err)
	}
	content := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	file := content[1].(map[string]any)["file"].(map[string]any)
	if file["filename"] != "notes.txt" || file["file_id"] != "file_123" {
		t.Fatalf("unexpected Chat attachment: %#v", file)
	}
}

func TestPatchRelayAttachmentsConvertsToAnthropicAndGemini(t *testing.T) {
	source := []byte(`{
		"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}]}]
	}`)
	turns := collectRelayAttachmentTurns(llm.APIFormatOpenAIChatCompletion, source)

	for _, test := range []struct {
		name   string
		target llm.APIFormat
		body   string
		check  func(t *testing.T, root map[string]any)
	}{
		{
			name:   "anthropic",
			target: llm.APIFormatAnthropicMessage,
			body:   `{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"inspect"}]}]}`,
			check: func(t *testing.T, root map[string]any) {
				block := root["messages"].([]any)[0].(map[string]any)["content"].([]any)[1].(map[string]any)
				source := block["source"].(map[string]any)
				if block["type"] != "document" || source["type"] != "base64" || source["media_type"] != "application/pdf" || source["data"] != "JVBERi0=" {
					t.Fatalf("unexpected Anthropic document: %#v", block)
				}
			},
		},
		{
			name:   "gemini",
			target: llm.APIFormatGeminiContents,
			body:   `{"contents":[{"role":"user","parts":[{"text":"inspect"}]}]}`,
			check: func(t *testing.T, root map[string]any) {
				part := root["contents"].([]any)[0].(map[string]any)["parts"].([]any)[1].(map[string]any)
				inline := part["inlineData"].(map[string]any)
				if inline["mimeType"] != "application/pdf" || inline["data"] != "JVBERi0=" {
					t.Fatalf("unexpected Gemini document: %#v", part)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			patched, changed, err := patchRelayAttachmentBody([]byte(test.body), test.target, turns)
			if err != nil || !changed {
				t.Fatalf("patch failed: changed=%v err=%v", changed, err)
			}
			var root map[string]any
			if err := json.Unmarshal(patched, &root); err != nil {
				t.Fatal(err)
			}
			test.check(t, root)
		})
	}
}

func TestPatchRelayAttachmentsDoesNotDuplicateExistingPart(t *testing.T) {
	turns := collectRelayAttachmentTurns(llm.APIFormatOpenAIChatCompletion, []byte(`{
		"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}]}]
	}`))
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}]}]}`)
	patched, changed, err := patchRelayAttachmentBody(body, llm.APIFormatOpenAIChatCompletion, turns)
	if err != nil || changed {
		t.Fatalf("existing attachment should be left alone: changed=%v err=%v body=%s", changed, err, patched)
	}
}

func TestMiddlewareAppliesRelayAttachmentCompatibilityAtOutboundBoundary(t *testing.T) {
	source := []byte(`{"messages":[{"role":"user","content":[{"type":"file","file":{"filename":"report.pdf","file_data":"data:application/pdf;base64,JVBERi0="}}]}]}`)
	ra := newTestAttempt(&dbmodel.Channel{Type: llm.APIFormatOpenAIResponse})
	ra.relayRun.internalRequest = &llm.Request{
		APIFormat: llm.APIFormatOpenAIChatCompletion,
		RawRequest: &httpclient.Request{
			Body: source,
		},
	}
	m := &relayPipelineMiddleware{attempt: ra}
	outbound := &httpclient.Request{
		Headers:     http.Header{"Content-Type": {"application/json"}},
		ContentType: "application/json",
		Body:        []byte(`{"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"inspect"}]}]}`),
	}
	if _, err := m.OnOutboundRawRequest(context.Background(), outbound); err != nil {
		t.Fatalf("middleware returned error: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(outbound.Body, &root); err != nil {
		t.Fatal(err)
	}
	content := root["input"].([]any)[0].(map[string]any)["content"].([]any)
	if len(content) != 2 || content[1].(map[string]any)["type"] != "input_file" {
		t.Fatalf("middleware did not preserve attachment: %s", outbound.Body)
	}
}

func TestFilterRequestForLogRedactsDocumentContent(t *testing.T) {
	request := &llm.Request{Messages: []llm.Message{{Content: llm.MessageContent{MultipleContent: []llm.MessageContentPart{{
		Type: "document", Document: &llm.DocumentURL{URL: "data:application/pdf;base64,secret", MIMEType: "application/pdf"},
	}}}}}}
	filtered := filterRequestForLog(request)
	if filtered.Messages[0].Content.MultipleContent[0].Document.URL == "data:application/pdf;base64,secret" {
		t.Fatal("document data URL was retained in the log copy")
	}
	if request.Messages[0].Content.MultipleContent[0].Document.URL != "data:application/pdf;base64,secret" {
		t.Fatal("filter mutated the original request")
	}
}
