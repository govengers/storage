package oteladapter_test

import (
	"fmt"

	"go.opentelemetry.io/otel/trace/noop"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
	"github.com/govengers/storage/oteladapter"
)

func ExampleNew() {
	// main passes traces.TracerProvider() from github.com/govengers/telemetry/tracing.
	tracer, err := oteladapter.New(&oteladapter.Options{TracerProvider: noop.NewTracerProvider()})
	if err != nil {
		fmt.Println(err)
		return
	}
	_, err = storage.NewObservedStore(&storage.ObservedStoreOptions{Store: memory.New(), Name: "attachments", Observers: []storage.Observer{tracer}})
	fmt.Println(err)
	// Output: <nil>
}
