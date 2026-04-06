package prometheus

import (
	"errors"
	"fmt"
	"strings"

	"github.com/oswalpalash/skale/internal/metrics"
)

var ErrInvalidQueries = errors.New("invalid prometheus signal queries")

// SignalQuery defines the PromQL boundary for one normalized signal.
//
// Expr may use the placeholders $namespace, $name, and $deployment. Queries should already aggregate down to a
// single series for the target workload or cluster signal; the adapter does not silently merge multiple series.
type SignalQuery struct {
	Expr     string
	Unit     string
	Required bool
}

// Render produces a concrete PromQL expression for the target workload.
func (q SignalQuery) Render(target metrics.Target) string {
	replacer := strings.NewReplacer(
		"$namespace", target.Namespace,
		"$name", target.Name,
		"$deployment", target.Name,
	)
	return replacer.Replace(q.Expr)
}

// Queries defines the explicit query boundary for the v1 signal set.
//
// Mandatory by default:
// - Demand
// - Replicas
//
// Strongly recommended for determining support/readiness:
// - CPU
// - Memory
// - Warmup
//
// Optional enrichment signals:
// - Latency
// - Errors
// - NodeHeadroom
type Queries struct {
	Demand       SignalQuery
	Replicas     SignalQuery
	CPU          SignalQuery
	Memory       SignalQuery
	Latency      SignalQuery
	Errors       SignalQuery
	Warmup       SignalQuery
	NodeHeadroom SignalQuery
}

// Validate checks that the adapter has the minimum required query contract for v1.
func (q Queries) Validate() error {
	var issues []string
	if strings.TrimSpace(q.Demand.Expr) == "" {
		issues = append(issues, "demand query is required")
	}
	if strings.TrimSpace(q.Replicas.Expr) == "" {
		issues = append(issues, "replicas query is required")
	}

	if len(issues) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidQueries, strings.Join(issues, "; "))
	}
	return nil
}

func (q Queries) queryFor(name metrics.SignalName) SignalQuery {
	switch name {
	case metrics.SignalDemand:
		return q.Demand
	case metrics.SignalReplicas:
		return q.Replicas
	case metrics.SignalCPU:
		return q.CPU
	case metrics.SignalMemory:
		return q.Memory
	case metrics.SignalLatency:
		return q.Latency
	case metrics.SignalErrors:
		return q.Errors
	case metrics.SignalWarmup:
		return q.Warmup
	case metrics.SignalNodeHeadroom:
		return q.NodeHeadroom
	default:
		return SignalQuery{}
	}
}

func signalRequired(name metrics.SignalName, query SignalQuery) bool {
	switch name {
	case metrics.SignalDemand, metrics.SignalReplicas:
		return true
	default:
		return query.Required
	}
}
