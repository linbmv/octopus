package relay

import (
	"bytes"
	"encoding/json"

	"github.com/bestruirui/octopus/internal/conf"
	"github.com/looplj/axonhub/llm"
	"github.com/looplj/axonhub/llm/httpclient"
)

const streamLogTruncatedMarker = "\n[stream log truncated]"

type streamLogCollector struct {
	events        []*httpclient.StreamEvent
	eventBytes    int
	truncatedBody []byte
	usage         *llm.Usage
}

func newStreamLogCollector() *streamLogCollector {
	return &streamLogCollector{
		events: make([]*httpclient.StreamEvent, 0, 8),
	}
}

func (c *streamLogCollector) Add(event *httpclient.StreamEvent) {
	if event == nil || len(event.Data) == 0 {
		return
	}
	if usage := streamEventUsage(event.Data); usage != nil {
		c.usage = usage
	}
	if c.truncatedBody != nil {
		return
	}

	nextBytes := c.eventBytes + len(event.Data) + 1
	if nextBytes > conf.MaxRelayLogContentBytes {
		c.truncatedBody = c.buildTruncatedBody(event.Data)
		c.events = nil
		c.eventBytes = len(c.truncatedBody)
		return
	}

	copied := *event
	copied.Data = append([]byte(nil), event.Data...)
	c.events = append(c.events, &copied)
	c.eventBytes = nextBytes
}

func (c *streamLogCollector) Empty() bool {
	return c.truncatedBody == nil && len(c.events) == 0
}

func (c *streamLogCollector) Truncated() bool {
	return c.truncatedBody != nil
}

func (c *streamLogCollector) Events() []*httpclient.StreamEvent {
	return c.events
}

func (c *streamLogCollector) TruncatedBody() []byte {
	return c.truncatedBody
}

func (c *streamLogCollector) Usage() *llm.Usage {
	return c.usage
}

func (c *streamLogCollector) buildTruncatedBody(current []byte) []byte {
	limit := conf.MaxRelayLogContentBytes - len(streamLogTruncatedMarker)
	if limit < 0 {
		limit = 0
	}

	var buf bytes.Buffer
	writeBounded := func(data []byte) {
		if buf.Len() >= limit {
			return
		}
		remaining := limit - buf.Len()
		if len(data) > remaining {
			data = data[:remaining]
		}
		buf.Write(data)
	}

	for _, event := range c.events {
		if event == nil {
			continue
		}
		writeBounded(event.Data)
		writeBounded([]byte("\n"))
	}
	writeBounded(current)
	buf.WriteString(streamLogTruncatedMarker)
	return buf.Bytes()
}

func streamEventUsage(data []byte) *llm.Usage {
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return nil
	}
	var envelope struct {
		Usage *llm.Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil
	}
	return envelope.Usage
}
