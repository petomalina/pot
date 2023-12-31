package pot

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.21.0"
)

// OTELResource returns the open telemetry resource for the current pot instance
func OTELResource() (*resource.Resource, error) {
	return resource.Merge(
		resource.Default(),
		resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceName("github.com/petomalina/pot"),
			semconv.ServiceVersion("2.0.0"),
		),
	)
}

// newPropagator initializes the propagator for context of traces
func newPropagator() propagation.TextMapPropagator {
	return propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	)
}

func newTraceProvider(ctx context.Context, res *resource.Resource) (*trace.TracerProvider, error) {
	traceExporter, err := otlptrace.New(ctx, otlptracegrpc.NewClient(otlptracegrpc.WithInsecure()))
	if err != nil {
		return nil, err
	}

	traceProvider := trace.NewTracerProvider(
		trace.WithResource(res),
		trace.WithBatcher(traceExporter),
	)

	return traceProvider, nil
}

func newMeterProvider(ctx context.Context, res *resource.Resource) (*metric.MeterProvider, error) {
	metricExporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithInsecure())
	if err != nil {
		return nil, err
	}

	meterProvider := metric.NewMeterProvider(
		metric.WithResource(res),
		metric.WithReader(
			metric.NewPeriodicReader(
				metricExporter,
			),
		),
	)

	return meterProvider, nil
}

func BootstrapOTEL(ctx context.Context) (func(context.Context) error, error) {
	res, err := OTELResource()
	if err != nil {
		return nil, err
	}

	meterProvider, err := newMeterProvider(ctx, res)
	if err != nil {
		return nil, err
	}

	traceProvider, err := newTraceProvider(ctx, res)
	if err != nil {
		return nil, err
	}

	propagator := newPropagator()

	// set global meter provider
	otel.SetMeterProvider(meterProvider)

	// set global trace provider
	otel.SetTracerProvider(traceProvider)

	// set global propagator
	otel.SetTextMapPropagator(propagator)

	return meterProvider.Shutdown, nil
}
