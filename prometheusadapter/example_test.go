package prometheusadapter_test

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/govengers/storage"
	"github.com/govengers/storage/memory"
	"github.com/govengers/storage/prometheusadapter"
)

func ExampleNew() {
	registry := prometheus.NewRegistry() // the service's own registry, served on its internal /metrics
	metrics, err := prometheusadapter.New(&prometheusadapter.Options{Registerer: registry, Namespace: "todo"})
	if err != nil {
		fmt.Println(err)
		return
	}
	// One Metrics for every store of the process; Name tells them apart.
	_, err = storage.NewObservedStore(&storage.ObservedStoreOptions{Store: memory.New(), Name: "attachments", Observers: []storage.Observer{metrics}})
	fmt.Println(err)
	// Output: <nil>
}
