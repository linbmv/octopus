package relay

import (
	"strings"

	"github.com/bestruirui/octopus/internal/utils/jsonpatch"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

// applyRawPassthrough 在 OpenAI Chat 入站转 OpenAI Chat 出站、且渠道开启开关时，
// 用客户端原始 JSON body 替换 outbound transformer 重序列化后的 body，避免字段重排和未知字段丢失，
// 以提升上游 prompt cache 命中率。仅替换顶层 model 为本次 attempt 的真实上游模型，并按需补流式 usage 标记。
// 返回 true 表示本次确实用原始 body 替换了出站请求。
func (ra *relayAttempt) applyRawPassthrough(outboundRequest *httpclient.Request) bool {
	if !ra.channel.RawPassthrough {
		return false
	}
	// 入站与出站都必须是 OpenAI Chat，且为 chat 请求，避免把原始 body 透传给异构协议或 embedding/image 等请求。
	if ra.internalRequest.APIFormat != llm.APIFormatOpenAIChatCompletion || ra.channel.Type != llm.APIFormatOpenAIChatCompletion {
		return false
	}
	if ra.internalRequest.RequestType != "" && ra.internalRequest.RequestType != llm.RequestTypeChat {
		return false
	}
	if ra.internalRequest.RawRequest == nil || len(ra.internalRequest.RawRequest.Body) == 0 {
		return false
	}
	// 出站必须是 JSON body；multipart 等非 JSON 请求不参与透传。
	if !strings.Contains(strings.ToLower(outboundRequest.Headers.Get("Content-Type")+" "+outboundRequest.ContentType), "application/json") {
		return false
	}

	rawBody := ra.internalRequest.RawRequest.Body
	// 安全门槛：raw body 必须是带顶层 string model 的合法 JSON 对象，否则回退常规转换路径，绝不发送错误模型。
	rawModel, ok := jsonpatch.TopLevelModel(rawBody)
	if !ok {
		return false
	}

	// 复制后再 patch，禁止原地修改 RawRequest.Body，避免重试或切换渠道时污染原始请求。
	patched := make([]byte, len(rawBody))
	copy(patched, rawBody)

	if rawModel != ra.internalRequest.Model {
		// 实际上游模型与原始请求不同：必须 patch 成功才能透传，否则会把请求发到错误模型上。
		next, modelPatched := jsonpatch.PatchModel(patched, ra.internalRequest.Model)
		if !modelPatched {
			return false
		}
		patched = next
	}

	// 流式请求补 stream_options.include_usage=true：raw body 替换发生在 stream.EnsureUsage 之后，
	// 不补这个标记会丢失 usage 聚合所需的最终 usage chunk。
	if ra.internalRequest.Stream != nil && *ra.internalRequest.Stream {
		if next, _ := jsonpatch.EnsureStreamIncludeUsage(patched); next != nil {
			patched = next
		}
	}

	outboundRequest.Body = patched
	return true
}
