package log

import (
	"context"
	"strconv"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
)

type contextKey string

const (
	requestIDKey contextKey = "request_id"
	userIDKey    contextKey = "user_id"
	channelIDKey contextKey = "channel_id"
)

func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

func WithUserID(ctx context.Context, userID uint) context.Context {
	return context.WithValue(ctx, userIDKey, userID)
}

func WithChannelID(ctx context.Context, channelID int) context.Context {
	return context.WithValue(ctx, channelIDKey, channelID)
}

func WithContext(ctx context.Context) *zap.SugaredLogger {
	if ctx == nil {
		return Logger
	}
	fields := make([]interface{}, 0, 8)
	if requestID, ok := ctx.Value(requestIDKey).(string); ok && requestID != "" {
		fields = append(fields, "request_id", requestID)
	}
	if userID, ok := ctx.Value(userIDKey).(uint); ok && userID != 0 {
		fields = append(fields, "user_id", strconv.FormatUint(uint64(userID), 10))
	}
	if channelID, ok := ctx.Value(channelIDKey).(int); ok && channelID != 0 {
		fields = append(fields, "channel_id", channelID)
	}
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		fields = append(fields, "trace_id", spanContext.TraceID().String(), "span_id", spanContext.SpanID().String())
	}
	if len(fields) == 0 {
		return Logger
	}
	return Logger.With(fields...)
}
