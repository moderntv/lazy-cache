package lazy

import (
	cadre_metrics "github.com/moderntv/cadre/metrics"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	metricsPrefix = "cache_"
	subSystem     = "lazy_cache"
	labelName     = "name"
)

type Metrics struct {
	ItemsCount                prometheus.Gauge
	AutomaticLoadCount        prometheus.Counter
	LazyLoadCount             prometheus.Counter
	ErrorLoadCount            prometheus.Counter
	ReadsCount                prometheus.Counter
	ReceivedNatsInvalidations prometheus.Counter
	MemoryUsage               prometheus.Gauge
}

func New(
	name string,
	registry *cadre_metrics.Registry,
) (m *Metrics, err error) {
	itemsCount := registry.NewGauge(prometheus.GaugeOpts{
		Subsystem:   subSystem,
		Name:        "items_count",
		Help:        "Current number of cached items",
		ConstLabels: prometheus.Labels{labelName: name},
	})

	automaticLoadCount := registry.NewCounter(prometheus.CounterOpts{
		Subsystem:   subSystem,
		Name:        "automatic_loads",
		Help:        "Total number of automatic item loads (including preloading)",
		ConstLabels: prometheus.Labels{labelName: name},
	})

	lazyLoadCount := registry.NewCounter(prometheus.CounterOpts{
		Subsystem:   subSystem,
		Name:        "lazy_loads",
		Help:        "Total number of lazy item loads (triggered by user request)",
		ConstLabels: prometheus.Labels{labelName: name},
	})

	errorLoadCount := registry.NewCounter(prometheus.CounterOpts{
		Subsystem:   subSystem,
		Name:        "error_loads",
		Help:        "Count of item loads which ended with an error (except not found)",
		ConstLabels: prometheus.Labels{labelName: name},
	})

	readsCount := registry.NewCounter(prometheus.CounterOpts{
		Subsystem:   subSystem,
		Name:        "reads_count",
		Help:        "Total number of item  when item was found in cache",
		ConstLabels: prometheus.Labels{labelName: name},
	})

	receivedNatsInvalidations := registry.NewCounter(prometheus.CounterOpts{
		Subsystem:   subSystem,
		Name:        "received_nats_invalidations",
		Help:        "Total number of received invalidations",
		ConstLabels: prometheus.Labels{labelName: name},
	})

	memoryUsage := registry.NewGauge(prometheus.GaugeOpts{
		Subsystem:   subSystem,
		Name:        "memory_usage",
		Help:        "Current memory usage in bytes by cache",
		ConstLabels: prometheus.Labels{labelName: name},
	})

	err = registry.Register(metricsPrefix+name+"_items_count", itemsCount)
	if err != nil {
		return
	}

	err = registry.Register(metricsPrefix+name+"_automatic_load_count", automaticLoadCount)
	if err != nil {
		return
	}

	err = registry.Register(metricsPrefix+name+"_lazy_load_count", lazyLoadCount)
	if err != nil {
		return
	}

	err = registry.Register(metricsPrefix+name+"_error_load_count", errorLoadCount)
	if err != nil {
		return
	}

	err = registry.Register(metricsPrefix+name+"_reads_count", readsCount)
	if err != nil {
		return
	}

	err = registry.Register(metricsPrefix+name+"_received_nats_invalidations", receivedNatsInvalidations)
	if err != nil {
		return
	}

	err = registry.Register(metricsPrefix+name+"_memory_usage", memoryUsage)
	if err != nil {
		return
	}

	m = &Metrics{
		ItemsCount:                itemsCount,
		AutomaticLoadCount:        automaticLoadCount,
		LazyLoadCount:             lazyLoadCount,
		ErrorLoadCount:            errorLoadCount,
		ReadsCount:                readsCount,
		ReceivedNatsInvalidations: receivedNatsInvalidations,
		MemoryUsage:               memoryUsage,
	}
	return
}
