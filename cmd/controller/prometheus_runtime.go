package main

import (
	"net/http"
	"time"

	"github.com/oswalpalash/skale/internal/controller"
	"github.com/oswalpalash/skale/internal/metrics"
	prommetrics "github.com/oswalpalash/skale/internal/metrics/prometheus"
)

type prometheusRuntimeConfig struct {
	URL                     string
	Step                    time.Duration
	DependencyQueryLookback time.Duration
	DemandQuery             string
	ReplicasQuery           string
	CPUQuery                string
	MemoryQuery             string
	LatencyQuery            string
	ErrorsQuery             string
	WarmupQuery             string
	NodeHeadroomQuery       string
}

func (c prometheusRuntimeConfig) enabled() bool {
	return c.URL != ""
}

func (c prometheusRuntimeConfig) build() (metrics.Provider, controller.DependencyEvaluator) {
	api := prommetrics.HTTPAPI{
		BaseURL: c.URL,
		HTTPClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
	provider := prommetrics.Adapter{
		API: api,
		Queries: prommetrics.Queries{
			Demand:       prommetrics.SignalQuery{Expr: c.DemandQuery},
			Replicas:     prommetrics.SignalQuery{Expr: c.ReplicasQuery},
			CPU:          prommetrics.SignalQuery{Expr: c.CPUQuery, Required: true},
			Memory:       prommetrics.SignalQuery{Expr: c.MemoryQuery, Required: true},
			Latency:      prommetrics.SignalQuery{Expr: c.LatencyQuery},
			Errors:       prommetrics.SignalQuery{Expr: c.ErrorsQuery},
			Warmup:       prommetrics.SignalQuery{Expr: c.WarmupQuery},
			NodeHeadroom: prommetrics.SignalQuery{Expr: c.NodeHeadroomQuery},
		},
		Step: c.Step,
	}
	dependencyEvaluator := controller.PrometheusDependencyEvaluator{
		API:           api,
		QueryLookback: c.DependencyQueryLookback,
		Step:          c.Step,
	}
	return provider, dependencyEvaluator
}
