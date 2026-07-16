package log

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestSetLevel(t *testing.T) {
	original := atomicLevel.Level()
	t.Cleanup(func() { atomicLevel.SetLevel(original) })

	SetLevel("debug")
	if atomicLevel.Level() != zapcore.DebugLevel {
		t.Fatalf("level = %v, want debug", atomicLevel.Level())
	}

	SetLevel("not-a-level")
	if atomicLevel.Level() != zapcore.DebugLevel {
		t.Fatalf("invalid level changed logger level to %v", atomicLevel.Level())
	}
}

func TestWithContextAddsCorrelationFields(t *testing.T) {
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := Logger
	Logger = zap.New(core).Sugar()
	t.Cleanup(func() { Logger = oldLogger })

	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1},
		SpanID:     trace.SpanID{2},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), spanContext)
	ctx = WithRequestID(ctx, "request-1")
	ctx = WithUserID(ctx, 7)
	ctx = WithChannelID(ctx, 11)
	WithContext(ctx).Infow("correlated")

	entries := observed.All()
	if len(entries) != 1 {
		t.Fatalf("log entry count = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	for key, want := range map[string]interface{}{
		"request_id": "request-1",
		"user_id":    "7",
		"channel_id": int64(11),
		"trace_id":   spanContext.TraceID().String(),
		"span_id":    spanContext.SpanID().String(),
	} {
		if got := fields[key]; got != want {
			t.Errorf("field %s = %#v, want %#v", key, got, want)
		}
	}
}

func TestLoggingHelpers(t *testing.T) {
	original := atomicLevel.Level()
	atomicLevel.SetLevel(zapcore.DebugLevel)
	t.Cleanup(func() { atomicLevel.SetLevel(original) })

	Infof("info %d", 1)
	Warnf("warn %d", 2)
	Errorf("error %d", 3)
	Debugf("debug %d", 4)
}
