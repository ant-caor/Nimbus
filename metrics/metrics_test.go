package metrics_test

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/ant-caor/nimbus"
	"github.com/ant-caor/nimbus/metrics"
)

func TestRegisterReportsStats(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	loader := func(_ context.Context, _ string) (int, error) { return 1, nil }
	c, err := nimbus.NewBuilder[string, int](loader).TTL(time.Hour, 0).Build()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()

	reg, err := metrics.Register(provider.Meter("test"), c)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reg.Unregister() }()

	ctx := context.Background()
	if _, err := c.GetOrLoad(ctx, "k"); err != nil { // miss + load
		t.Fatal(err)
	}
	if _, err := c.GetOrLoad(ctx, "k"); err != nil { // fresh hit
		t.Fatal(err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}

	checks := map[string]int64{
		"nimbus.hits":   1,
		"nimbus.misses": 1,
		"nimbus.loads":  1,
	}
	for name, want := range checks {
		if got := scrapeInt64(t, rm, name); got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
}

func scrapeInt64(t *testing.T, rm metricdata.ResourceMetrics, name string) int64 {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				if len(d.DataPoints) > 0 {
					return d.DataPoints[0].Value
				}
			case metricdata.Gauge[int64]:
				if len(d.DataPoints) > 0 {
					return d.DataPoints[0].Value
				}
			}
		}
	}
	t.Fatalf("metric %q not found in collected data", name)
	return 0
}
