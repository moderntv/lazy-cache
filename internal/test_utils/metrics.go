package test_utils

import (
	"github.com/moderntv/cadre/metrics"
)

func MetricsWithNamespace() (m *metrics.Registry) {
	return Metrics("appapi_test")
}

func Metrics(namespace string) (m *metrics.Registry) {
	var err error
	m, err = metrics.NewRegistry(namespace, nil)
	if err != nil || m == nil {
		panic("cannot crete metrics registry")
	}

	return
}
