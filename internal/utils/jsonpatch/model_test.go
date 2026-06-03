package jsonpatch

import (
	"strings"
	"testing"
)

func TestPatchModel(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		model       string
		want        string
		wantPatched bool
	}{
		{
			name:        "替换顶层 model 并保留字段顺序",
			raw:         `{"temperature":0.7,"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`,
			model:       "gpt-5",
			want:        `{"temperature":0.7,"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`,
			wantPatched: true,
		},
		{
			name: "保留空白和 cache sensitive 片段",
			raw: `{ "model" : "gpt-4o" , "messages" : [` +
				`{"role":"user","content":[{"type":"text","text":"hello",` +
				`"cache_control":{"type":"ephemeral"}}]}], "stream" : true }`,
			model: "gpt-5",
			want: `{ "model" : "gpt-5" , "messages" : [` +
				`{"role":"user","content":[{"type":"text","text":"hello",` +
				`"cache_control":{"type":"ephemeral"}}]}], "stream" : true }`,
			wantPatched: true,
		},
		{
			name:        "目标相同返回原样",
			raw:         `{"model":"gpt-4o","messages":[]}`,
			model:       "gpt-4o",
			want:        `{"model":"gpt-4o","messages":[]}`,
			wantPatched: false,
		},
		{
			name:        "空目标 model 返回原样",
			raw:         `{"model":"gpt-4o","messages":[]}`,
			model:       "",
			want:        `{"model":"gpt-4o","messages":[]}`,
			wantPatched: false,
		},
		{
			name:        "缺失顶层 model 不插入",
			raw:         `{"messages":[{"role":"user","content":"hello"}]}`,
			model:       "gpt-5",
			want:        `{"messages":[{"role":"user","content":"hello"}]}`,
			wantPatched: false,
		},
		{
			name:        "顶层 model 非字符串不 patch",
			raw:         `{"model":123,"messages":[]}`,
			model:       "gpt-5",
			want:        `{"model":123,"messages":[]}`,
			wantPatched: false,
		},
		{
			name:        "非对象返回原样",
			raw:         `[{"model":"gpt-4o"}]`,
			model:       "gpt-5",
			want:        `[{"model":"gpt-4o"}]`,
			wantPatched: false,
		},
		{
			name:        "非法 JSON 返回原样",
			raw:         `{"model":"gpt-4o",`,
			model:       "gpt-5",
			want:        `{"model":"gpt-4o",`,
			wantPatched: false,
		},
		{
			name:        "嵌套 model 不被替换",
			raw:         `{"messages":[{"model":"nested","content":"hello"}]}`,
			model:       "gpt-5",
			want:        `{"messages":[{"model":"nested","content":"hello"}]}`,
			wantPatched: false,
		},
		{
			name: "顶层 model 后有嵌套对象和数组",
			raw: `{"metadata":{"model":"nested"},"model":"gpt-4o",` +
				`"messages":[{"role":"user","content":"hello"}],"extra":[{"a":1}]}`,
			model: "gpt-5",
			want: `{"metadata":{"model":"nested"},"model":"gpt-5",` +
				`"messages":[{"role":"user","content":"hello"}],"extra":[{"a":1}]}`,
			wantPatched: true,
		},
		{
			name:        "转义字符串不会破坏扫描",
			raw:         `{"note":"quote: \" and slash: \\","model":"gpt-4o","messages":["a,b",{"x":"}"}]}`,
			model:       "gpt-5",
			want:        `{"note":"quote: \" and slash: \\","model":"gpt-5","messages":["a,b",{"x":"}"}]}`,
			wantPatched: true,
		},
		{
			name:        "新 model 需要标准 JSON 转义",
			raw:         `{"model":"gpt-4o","messages":[]}`,
			model:       "gpt-\"5",
			want:        `{"model":"gpt-\"5","messages":[]}`,
			wantPatched: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, patched := PatchModel([]byte(tt.raw), tt.model)
			if patched != tt.wantPatched {
				t.Fatalf("PatchModel() patched = %v, want %v", patched, tt.wantPatched)
			}
			if string(got) != tt.want {
				t.Fatalf("PatchModel() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestPatchModelKeepsCacheSensitivePrefix(t *testing.T) {
	raw := []byte(`{"input":[{"role":"user","content":[` +
		`{"type":"input_text","text":"describe"},` +
		`{"type":"input_image","image_url":"data:image/png;base64,abc","detail":"low"}` +
		`]}],"model":"gpt-4o","stream":true}`)
	wantPrefix := `{"input":[{"role":"user","content":[` +
		`{"type":"input_text","text":"describe"},` +
		`{"type":"input_image","image_url":"data:image/png;base64,abc","detail":"low"}` +
		`]}],"model":`

	got, patched := PatchModel(raw, "gpt-5")
	if !patched {
		t.Fatal("PatchModel() patched = false, want true")
	}
	if !strings.HasPrefix(string(got), wantPrefix) {
		t.Fatalf("PatchModel() changed cache-sensitive prefix\ngot:  %s\nwant prefix: %s", got, wantPrefix)
	}
}
