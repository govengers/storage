// Package prometheusadapter exports the operations of a storage.ObservedStore as Prometheus
// metrics on a registry the caller passes in (LIB-011). Metrics implements storage.Observer;
// give it to storage.ObservedStoreOptions.Observers.
package prometheusadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/govengers/storage"
)

const subsystem = "storage"

// durationBuckets reach from 1 ms to about a minute: a stat on a local disk to a large upload.
var durationBuckets = prometheus.ExponentialBuckets(0.001, 4, 9)

// Options configures Metrics.
type Options struct {
	Registerer prometheus.Registerer // required; the caller's registry, never the default one
	Namespace  string                // required: the service name, e.g. "billing" for billing_storage_operations_total
}

// Metrics records the operations of observed stores; it implements storage.Observer and is
// safe for concurrent use. Every label is a closed set: store names are declared in code,
// operations are the storage.Operation constants, outcomes the storage.ErrorClass constants. One Metrics serves every ObservedStore of a process.
//
//	<namespace>_storage_operations_total{store,operation,outcome}
//	<namespace>_storage_operation_duration_seconds{store,operation}
//	<namespace>_storage_bytes_total{store,operation}  bytes stored by put and copy, read by get
type Metrics struct {
	operations *prometheus.CounterVec
	durations  *prometheus.HistogramVec
	bytes      *prometheus.CounterVec
}

var _ storage.Observer = (*Metrics)(nil)

// New registers the metrics on options.Registerer. A failed registration, a duplicate for
// example, is returned as an error and registers nothing.
func New(options *Options) (*Metrics, error) {
	if options == nil || options.Registerer == nil || options.Namespace == "" {
		return nil, errors.New("prometheusadapter: Options needs a Registerer and a Namespace")
	}
	metrics := &Metrics{
		operations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "operations_total",
			Help: "Finished storage operations by store, operation and outcome.",
		}, []string{"store", "operation", "outcome"}),
		durations: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "operation_duration_seconds",
			Help: "Duration of storage operations by store and operation; a get lasts until its reader is closed.", Buckets: durationBuckets,
		}, []string{"store", "operation"}),
		bytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: options.Namespace, Subsystem: subsystem, Name: "bytes_total",
			Help: "Bytes stored by put and copy and read by get, by store and operation.",
		}, []string{"store", "operation"}),
	}
	collectors := []prometheus.Collector{metrics.operations, metrics.durations, metrics.bytes}
	for index, collector := range collectors {
		if err := options.Registerer.Register(collector); err != nil {
			for _, registered := range collectors[:index] {
				options.Registerer.Unregister(registered)
			}
			return nil, fmt.Errorf("prometheusadapter: register the %s_%s metrics: %v", options.Namespace, subsystem, err) //nolint:errorlint // LIB-010: client_golang errors are not part of the API
		}
	}
	return metrics, nil
}

// OperationStarted keeps nothing.
func (m *Metrics) OperationStarted(ctx context.Context, _ *storage.Operation) context.Context {
	return ctx
}

// OperationFinished counts the operation, observes its duration and adds its bytes.
func (m *Metrics) OperationFinished(_ context.Context, operation *storage.Operation, outcome *storage.Outcome) {
	name := string(operation.Name)
	m.operations.WithLabelValues(operation.Store, name, string(storage.ClassifyError(outcome.Err))).Inc()
	m.durations.WithLabelValues(operation.Store, name).Observe(outcome.Duration.Seconds())
	if outcome.Bytes > 0 {
		m.bytes.WithLabelValues(operation.Store, name).Add(float64(outcome.Bytes))
	}
}
