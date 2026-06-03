package jsonpatch

import (
	"strings"
	"testing"
)

func TestEnsureStreamIncludeUsage(t *testing.T) {
	tests := []struct {
		name        string
		raw         string
		want        string
		wantPatched bool
	}{
		{
			name:        "无 stream_options 时追加顶层字段",
			raw:         `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`,
			want:        `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}],"stream_options":{"include_usage":true}}`,
			wantPatched: true,
		},
		{
			name:        "stream_options 为空对象时插入 include_usage",
			raw:         `{"model":"gpt-4o","stream_options":{}}`,
			want:        `{"model":"gpt-4o","stream_options":{"include_usage":true}}`,
			wantPatched: true,
		},
		{
			name:        "stream_options 有其它字段时追加 include_usage",
			raw:         `{"model":"gpt-4o","stream_options":{"foo":1}}`,
			want:        `{"model":"gpt-4o","stream_options":{"foo":1,"include_usage":true}}`,
			wantPatched: true,
		},
		{
			name:        "include_usage 为 false 时最小替换为 true",
			raw:         `{"model":"gpt-4o","stream_options":{"include_usage":false}}`,
			want:        `{"model":"gpt-4o","stream_options":{"include_usage":true}}`,
			wantPatched: true,
		},
		{
			name:        "include_usage 已是 true 时保持原样",
			raw:         `{"model":"gpt-4o","stream_options":{"include_usage":true}}`,
			want:        `{"model":"gpt-4o","stream_options":{"include_usage":true}}`,
			wantPatched: false,
		},
		{
			name:        "保留空白和字段顺序",
			raw:         `{ "model" : "gpt-4o" , "messages" : [{"role":"user","content":"hi"}] }`,
			want:        `{ "model" : "gpt-4o" , "messages" : [{"role":"user","content":"hi"}],"stream_options":{"include_usage":true} }`,
			wantPatched: true,
		},
		{
			name:        "空对象追加 stream_options",
			raw:         `{}`,
			want:        `{"stream_options":{"include_usage":true}}`,
			wantPatched: true,
		},
		{
			name:        "非对象返回原样",
			raw:         `[{"model":"gpt-4o"}]`,
			want:        `[{"model":"gpt-4o"}]`,
			wantPatched: false,
		},
		{
			name:        "非法 JSON 返回原样",
			raw:         `{"model":"gpt-4o",`,
			want:        `{"model":"gpt-4o",`,
			wantPatched: false,
		},
		{
			name:        "stream_options 非对象返回原样",
			raw:         `{"model":"gpt-4o","stream_options":true}`,
			want:        `{"model":"gpt-4o","stream_options":true}`,
			wantPatched: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, patched := EnsureStreamIncludeUsage([]byte(tt.raw))
			if patched != tt.wantPatched {
				t.Fatalf("EnsureStreamIncludeUsage() patched = %v, want %v", patched, tt.wantPatched)
			}
			if string(got) != tt.want {
				t.Fatalf("EnsureStreamIncludeUsage() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestEnsureStreamIncludeUsageKeepsCacheSensitivePrefix(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}],"model":"gpt-4o","stream":true}`)
	wantPrefix := `{"messages":[{"role":"user","content":[` +
		`{"type":"text","text":"hello","cache_control":{"type":"ephemeral"}}]}],"model":"gpt-4o","stream":true`

	got, patched := EnsureStreamIncludeUsage(raw)
	if !patched {
		t.Fatal("EnsureStreamIncludeUsage() patched = false, want true")
	}
	if !strings.HasPrefix(string(got), wantPrefix) {
		t.Fatalf("EnsureStreamIncludeUsage() 破坏了缓存敏感前缀\ngot:  %s\nwant prefix: %s", got, wantPrefix)
	}
}

func TestTopLevelModel(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   string
		wantOK bool
	}{
		{
			name:   "读取顶层 model",
			raw:    `{"model":"gpt-4o","messages":[]}`,
			want:   "gpt-4o",
			wantOK: true,
		},
		{
			name:   "model 在中部仍可读取",
			raw:    `{"temperature":0.7,"model":"gpt-5","stream":true}`,
			want:   "gpt-5",
			wantOK: true,
		},
		{
			name:   "缺失顶层 model",
			raw:    `{"messages":[]}`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "顶层 model 非字符串",
			raw:    `{"model":123}`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "嵌套 model 不被误读",
			raw:    `{"messages":[{"model":"nested"}]}`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "顶层重复 model 判失败",
			raw:    `{"model":"allowed","model":"real"}`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "顶层重复 model 夹杂其它字段也判失败",
			raw:    `{"model":"a","temperature":1,"model":"b","stream":true}`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "非对象",
			raw:    `["model"]`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "非法 JSON",
			raw:    `{"model":"x"`,
			want:   "",
			wantOK: false,
		},
		{
			name:   "转义字符串 model",
			raw:    `{"model":"gpt-\"5"}`,
			want:   `gpt-"5`,
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := TopLevelModel([]byte(tt.raw))
			if ok != tt.wantOK {
				t.Fatalf("TopLevelModel() ok = %v, want %v", ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("TopLevelModel() = %q, want %q", got, tt.want)
			}
		})
	}
}
