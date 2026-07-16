package tracing

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const instrumentationName = "github.com/bestruirui/octopus"

type Config struct {
	Enabled     bool
	ServiceName string
	Endpoint    string
	Insecure    bool
	SampleRatio float64
}

var (
	providerMu sync.Mutex
	provider   *sdktrace.TracerProvider
)

func Init(ctx context.Context, config Config) error {
	if !config.Enabled {
		return Shutdown(ctx)
	}
	if config.ServiceName == "" {
		config.ServiceName = "octopus"
	}
	if config.Endpoint == "" {
		config.Endpoint = "localhost:4318"
	}
	if config.SampleRatio <= 0 || config.SampleRatio > 1 {
		config.SampleRatio = 0.01
	}

	options := []otlptracehttp.Option{otlptracehttp.WithEndpoint(config.Endpoint)}
	if config.Insecure {
		options = append(options, otlptracehttp.WithInsecure())
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return fmt.Errorf("create OTLP trace exporter: %w", err)
	}
	res, err := resource.New(ctx, resource.WithAttributes(attribute.String("service.name", config.ServiceName)))
	if err != nil {
		return fmt.Errorf("create trace resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(config.SampleRatio))),
	)

	providerMu.Lock()
	old := provider
	provider = tp
	providerMu.Unlock()
	if old != nil {
		_ = old.Shutdown(ctx)
	}
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return nil
}

func Shutdown(ctx context.Context) error {
	providerMu.Lock()
	tp := provider
	provider = nil
	providerMu.Unlock()
	if tp == nil {
		otel.SetTracerProvider(noop.NewTracerProvider())
		return nil
	}
	err := tp.Shutdown(ctx)
	otel.SetTracerProvider(noop.NewTracerProvider())
	return err
}

func Tracer() trace.Tracer {
	return otel.Tracer(instrumentationName)
}

func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx := otel.GetTextMapPropagator().Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		spanName := c.Request.Method + " " + c.Request.URL.Path
		ctx, span := Tracer().Start(ctx, spanName, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()
		c.Request = c.Request.WithContext(ctx)

		if spanContext := span.SpanContext(); spanContext.IsValid() {
			traceID := spanContext.TraceID().String()
			c.Set("trace_id", traceID)
			c.Header("X-Trace-ID", traceID)
		}

		c.Next()

		route := c.FullPath()
		if route == "" {
			route = c.Request.URL.Path
		}
		span.SetName(c.Request.Method + " " + route)
		span.SetAttributes(
			attribute.String("http.request.method", c.Request.Method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", c.Writer.Status()),
		)
		if c.Writer.Status() >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(c.Writer.Status()))
		}
	}
}
