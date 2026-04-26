package telemetry

import (
	"context"
	"log"
	"strings"
	"time"

	"llm-gateway/internal/config"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdoutmetric"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

type Options struct {
	Exporter         string
	OTLPEndpoint     string
	OTLPInsecure     bool
	MetricInterval   time.Duration
	TraceSampleRatio float64
	ServiceName      string
	ServiceVersion   string
}

func InitProvider(opts Options) (func(context.Context) error, error) {
	if opts.Exporter == "" {
		opts.Exporter = config.DefaultTelemetryExporter
	}
	if opts.OTLPEndpoint == "" {
		opts.OTLPEndpoint = config.DefaultOTLPEndpoint
	}
	if opts.MetricInterval <= 0 {
		opts.MetricInterval = config.DefaultMetricInterval
	}
	if opts.TraceSampleRatio < 0 || opts.TraceSampleRatio > 1 {
		opts.TraceSampleRatio = config.DefaultTraceSampleRatio
	}
	if opts.ServiceName == "" {
		opts.ServiceName = config.DefaultServiceName
	}
	if opts.ServiceVersion == "" {
		opts.ServiceVersion = config.DefaultServiceVersion
	}

	ctx := context.Background()
	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName(opts.ServiceName),
		semconv.ServiceVersion(opts.ServiceVersion),
	))
	if err != nil {
		return nil, err
	}

	var spanExporter trace.SpanExporter
	var metricReader metric.Reader

	switch strings.ToLower(opts.Exporter) {
	case "otlp":
		traceOpts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(opts.OTLPEndpoint)}
		metricOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithEndpoint(opts.OTLPEndpoint)}
		if opts.OTLPInsecure {
			traceOpts = append(traceOpts, otlptracegrpc.WithInsecure())
			metricOpts = append(metricOpts, otlpmetricgrpc.WithInsecure())
		}

		spanExporter, err = otlptracegrpc.New(ctx, traceOpts...)
		if err != nil {
			return nil, err
		}
		metricExporter, err := otlpmetricgrpc.New(ctx, metricOpts...)
		if err != nil {
			return nil, err
		}
		metricReader = metric.NewPeriodicReader(metricExporter, metric.WithInterval(opts.MetricInterval))
	default:
		spanExporter, err = stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, err
		}
		metricExporter, err := stdoutmetric.New()
		if err != nil {
			return nil, err
		}
		metricReader = metric.NewPeriodicReader(metricExporter, metric.WithInterval(opts.MetricInterval))
	}

	tracerProvider := trace.NewTracerProvider(
		trace.WithBatcher(spanExporter),
		trace.WithSampler(trace.ParentBased(trace.TraceIDRatioBased(opts.TraceSampleRatio))),
		trace.WithResource(res),
	)
	meterProvider := metric.NewMeterProvider(
		metric.WithReader(metricReader),
		metric.WithResource(res),
	)
	otel.SetTracerProvider(tracerProvider)
	otel.SetMeterProvider(meterProvider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return func(ctx context.Context) error {
		var shutdownErr error
		if err := tracerProvider.Shutdown(ctx); err != nil {
			log.Printf("error shutting down tracer provider: %v", err)
			shutdownErr = err
		}
		if err := meterProvider.Shutdown(ctx); err != nil {
			log.Printf("error shutting down meter provider: %v", err)
			shutdownErr = err
		}
		return shutdownErr
	}, nil
}
