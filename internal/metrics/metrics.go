// Package metrics sets up OpenTelemetry metrics for a service.
//
// Instrumentation is written against the OTel API only: the Prometheus
// exporter is the deployment's choice of how metrics leave the process, not a
// dependency of the code that records them.
package metrics

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

// Metrics holds a service's instruments and the provider behind them.
type Metrics struct {
	provider *sdkmetric.MeterProvider
	registry *prometheus.Registry

	requests metric.Int64Counter
	duration metric.Float64Histogram
	inFlight metric.Int64UpDownCounter
}

// New builds a meter provider exporting to Prometheus and registers the HTTP
// instruments every service shares.
func New(serviceName, version string) (*Metrics, error) {
	registry := prometheus.NewRegistry()

	exporter, err := otelprom.New(otelprom.WithRegisterer(registry))
	if err != nil {
		return nil, fmt.Errorf("create prometheus exporter: %w", err)
	}

	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(version),
	))
	if err != nil {
		return nil, fmt.Errorf("build resource: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
		sdkmetric.WithResource(res),
	)
	otel.SetMeterProvider(provider)

	meter := provider.Meter("github.com/CoderSyndicate/doleances")

	m := &Metrics{provider: provider, registry: registry}
	if m.requests, err = meter.Int64Counter(
		"http.server.requests",
		metric.WithDescription("HTTP requests handled"),
	); err != nil {
		return nil, fmt.Errorf("create request counter: %w", err)
	}
	if m.duration, err = meter.Float64Histogram(
		"http.server.duration",
		metric.WithDescription("HTTP request duration"),
		metric.WithUnit("s"),
	); err != nil {
		return nil, fmt.Errorf("create duration histogram: %w", err)
	}
	if m.inFlight, err = meter.Int64UpDownCounter(
		"http.server.active_requests",
		metric.WithDescription("HTTP requests currently being handled"),
	); err != nil {
		return nil, fmt.Errorf("create in-flight counter: %w", err)
	}
	return m, nil
}

// Handler serves the metrics endpoint. It belongs on the maintenance port.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// RequestStarted records the start of a request and returns the function that
// records its completion.
func (m *Metrics) RequestStarted(ctx context.Context, method, route string) func(status int) {
	attrs := metric.WithAttributes(
		attribute.String("http.request.method", method),
		attribute.String("http.route", route),
	)
	m.inFlight.Add(ctx, 1, attrs)
	started := time.Now()

	return func(status int) {
		done := metric.WithAttributes(
			attribute.String("http.request.method", method),
			attribute.String("http.route", route),
			attribute.Int("http.response.status_code", status),
		)
		m.inFlight.Add(ctx, -1, attrs)
		m.requests.Add(ctx, 1, done)
		m.duration.Record(ctx, time.Since(started).Seconds(), done)
	}
}

// Shutdown flushes and stops the meter provider.
func (m *Metrics) Shutdown(ctx context.Context) error {
	return m.provider.Shutdown(ctx)
}
