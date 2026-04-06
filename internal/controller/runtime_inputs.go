package controller

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/metrics"
	prommetrics "github.com/oswalpalash/skale/internal/metrics/prometheus"
	"github.com/oswalpalash/skale/internal/safety"
)

// DependencyEvaluator resolves configured dependency health checks into the safety input used
// by live controller recommendation evaluation.
type DependencyEvaluator interface {
	Evaluate(
		ctx context.Context,
		target metrics.Target,
		checks []skalev1alpha1.DependencyHealthCheck,
		evaluatedAt time.Time,
	) ([]safety.DependencyHealthStatus, error)
}

// HeadroomProvider resolves request-based node headroom input for the current target workload.
type HeadroomProvider interface {
	HeadroomFor(ctx context.Context, target ResolvedTarget, evaluatedAt time.Time) (*safety.NodeHeadroomSignal, error)
}

// NoopDependencyEvaluator fails closed when dependency checks are configured but no live evaluator
// has been wired for the controller process.
type NoopDependencyEvaluator struct{}

func (NoopDependencyEvaluator) Evaluate(
	_ context.Context,
	_ metrics.Target,
	checks []skalev1alpha1.DependencyHealthCheck,
	_ time.Time,
) ([]safety.DependencyHealthStatus, error) {
	statuses := make([]safety.DependencyHealthStatus, 0, len(checks))
	for _, check := range checks {
		statuses = append(statuses, safety.DependencyHealthStatus{
			Name:                check.Name,
			Healthy:             false,
			HealthyRatio:        0,
			MinimumHealthyRatio: check.MinHealthyRatio,
			Message:             fmt.Sprintf("dependency check %q is configured but no dependency evaluator is available", check.Name),
		})
	}
	return statuses, nil
}

// PrometheusDependencyEvaluator evaluates dependency health checks with the same placeholder
// rendering contract used by the Prometheus metrics adapter.
type PrometheusDependencyEvaluator struct {
	API           prommetrics.API
	QueryLookback time.Duration
	Step          time.Duration
}

func (e PrometheusDependencyEvaluator) Evaluate(
	ctx context.Context,
	target metrics.Target,
	checks []skalev1alpha1.DependencyHealthCheck,
	evaluatedAt time.Time,
) ([]safety.DependencyHealthStatus, error) {
	if len(checks) == 0 {
		return nil, nil
	}
	if e.API == nil {
		return NoopDependencyEvaluator{}.Evaluate(ctx, target, checks, evaluatedAt)
	}

	lookback := e.QueryLookback
	if lookback <= 0 {
		lookback = time.Minute
	}
	step := e.Step
	if step <= 0 {
		step = 30 * time.Second
	}

	statuses := make([]safety.DependencyHealthStatus, 0, len(checks))
	for _, check := range checks {
		status := safety.DependencyHealthStatus{
			Name:                check.Name,
			MinimumHealthyRatio: check.MinHealthyRatio,
		}

		rendered := prommetrics.SignalQuery{Expr: check.Query}.Render(target)
		result, err := e.API.QueryRange(ctx, rendered, evaluatedAt.Add(-lookback), evaluatedAt, step)
		if err != nil {
			status.Message = fmt.Sprintf("dependency check %q query failed: %v", check.Name, err)
			statuses = append(statuses, status)
			continue
		}
		if len(result.Series) == 0 {
			status.Message = fmt.Sprintf("dependency check %q query returned no series", check.Name)
			statuses = append(statuses, status)
			continue
		}
		if len(result.Series) != 1 {
			status.Message = fmt.Sprintf("dependency check %q query returned %d series", check.Name, len(result.Series))
			statuses = append(statuses, status)
			continue
		}
		if len(result.Series[0].Samples) == 0 {
			status.Message = fmt.Sprintf("dependency check %q query returned no samples", check.Name)
			statuses = append(statuses, status)
			continue
		}

		latest := result.Series[0].Samples[len(result.Series[0].Samples)-1].Value
		if math.IsNaN(latest) || math.IsInf(latest, 0) || latest < 0 || latest > 1 {
			status.Message = fmt.Sprintf("dependency check %q query returned invalid healthy ratio %.4f", check.Name, latest)
			statuses = append(statuses, status)
			continue
		}

		status.HealthyRatio = latest
		status.Healthy = latest >= check.MinHealthyRatio
		if status.Healthy {
			status.Message = fmt.Sprintf("dependency %q healthy ratio %.2f meets minimum %.2f", check.Name, latest, check.MinHealthyRatio)
		} else {
			status.Message = fmt.Sprintf("dependency %q healthy ratio %.2f is below minimum %.2f", check.Name, latest, check.MinHealthyRatio)
		}
		statuses = append(statuses, status)
	}

	return statuses, nil
}

// KubernetesHeadroomProvider derives the structured request-based headroom snapshot for the
// current target Deployment from cluster API state.
type KubernetesHeadroomProvider struct {
	Reader client.Reader
}

func (p KubernetesHeadroomProvider) HeadroomFor(
	ctx context.Context,
	target ResolvedTarget,
	evaluatedAt time.Time,
) (*safety.NodeHeadroomSignal, error) {
	if p.Reader == nil {
		return &safety.NodeHeadroomSignal{
			State:      safety.NodeHeadroomStateUnsupported,
			ObservedAt: evaluatedAt.UTC(),
		}, nil
	}

	var deployment appsv1.Deployment
	if err := p.Reader.Get(ctx, client.ObjectKey{
		Namespace: target.Identity.Namespace,
		Name:      target.Identity.Name,
	}, &deployment); err != nil {
		return &safety.NodeHeadroomSignal{
			State:      safety.NodeHeadroomStateUnsupported,
			ObservedAt: evaluatedAt.UTC(),
		}, nil
	}

	var nodes corev1.NodeList
	if err := p.Reader.List(ctx, &nodes); err != nil {
		return &safety.NodeHeadroomSignal{
			State:      safety.NodeHeadroomStateUnsupported,
			ObservedAt: evaluatedAt.UTC(),
		}, nil
	}
	if len(nodes.Items) == 0 {
		return &safety.NodeHeadroomSignal{
			State:      safety.NodeHeadroomStateMissing,
			ObservedAt: evaluatedAt.UTC(),
		}, nil
	}

	var pods corev1.PodList
	if err := p.Reader.List(ctx, &pods); err != nil {
		return &safety.NodeHeadroomSignal{
			State:      safety.NodeHeadroomStateUnsupported,
			ObservedAt: evaluatedAt.UTC(),
		}, nil
	}

	nodeRequested := make(map[string]safety.Resources, len(nodes.Items))
	for _, pod := range pods.Items {
		if pod.Spec.NodeName == "" || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		requests := podSpecRequests(pod.Spec)
		nodeRequested[pod.Spec.NodeName] = addResources(nodeRequested[pod.Spec.NodeName], requests)
	}

	clusterSummary := safety.AllocatableSummary{}
	nodeSummaries := make([]safety.NodeAllocatableSummary, 0, len(nodes.Items))
	for _, node := range nodes.Items {
		allocatable := resourcesFromList(node.Status.Allocatable)
		requested := nodeRequested[node.Name]
		summary := safety.AllocatableSummary{
			Allocatable: allocatable,
			Requested:   requested,
		}
		schedulable := nodeSchedulable(node)
		nodeSummaries = append(nodeSummaries, safety.NodeAllocatableSummary{
			Name:        node.Name,
			Schedulable: schedulable,
			Summary:     summary,
		})
		if schedulable {
			clusterSummary.Allocatable = addResources(clusterSummary.Allocatable, allocatable)
			clusterSummary.Requested = addResources(clusterSummary.Requested, requested)
		}
	}

	return &safety.NodeHeadroomSignal{
		State:          safety.NodeHeadroomStateReady,
		ObservedAt:     evaluatedAt.UTC(),
		PodRequests:    podSpecRequests(deployment.Spec.Template.Spec),
		ClusterSummary: clusterSummary,
		Nodes:          nodeSummaries,
	}, nil
}

func nodeSchedulable(node corev1.Node) bool {
	if node.Spec.Unschedulable {
		return false
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return true
}

func podSpecRequests(spec corev1.PodSpec) safety.Resources {
	regular := resourceTotals(spec.Containers)
	init := initContainerMax(spec.InitContainers)
	return safety.Resources{
		CPUMilli:    maxResource64(regular.CPUMilli, init.CPUMilli),
		MemoryBytes: maxResource64(regular.MemoryBytes, init.MemoryBytes),
	}
}

func resourceTotals(containers []corev1.Container) safety.Resources {
	total := safety.Resources{}
	for _, container := range containers {
		total.CPUMilli += quantityMilliValue(container.Resources.Requests[corev1.ResourceCPU])
		total.MemoryBytes += quantityValue(container.Resources.Requests[corev1.ResourceMemory])
	}
	return total
}

func initContainerMax(containers []corev1.Container) safety.Resources {
	maximum := safety.Resources{}
	for _, container := range containers {
		requests := safety.Resources{
			CPUMilli:    quantityMilliValue(container.Resources.Requests[corev1.ResourceCPU]),
			MemoryBytes: quantityValue(container.Resources.Requests[corev1.ResourceMemory]),
		}
		maximum.CPUMilli = maxResource64(maximum.CPUMilli, requests.CPUMilli)
		maximum.MemoryBytes = maxResource64(maximum.MemoryBytes, requests.MemoryBytes)
	}
	return maximum
}

func resourcesFromList(list corev1.ResourceList) safety.Resources {
	return safety.Resources{
		CPUMilli:    quantityMilliValue(list[corev1.ResourceCPU]),
		MemoryBytes: quantityValue(list[corev1.ResourceMemory]),
	}
}

func quantityMilliValue(quantity resource.Quantity) int64 {
	if quantity.IsZero() {
		return 0
	}
	return quantity.MilliValue()
}

func quantityValue(quantity resource.Quantity) int64 {
	if quantity.IsZero() {
		return 0
	}
	return quantity.Value()
}

func addResources(left, right safety.Resources) safety.Resources {
	return safety.Resources{
		CPUMilli:    left.CPUMilli + right.CPUMilli,
		MemoryBytes: left.MemoryBytes + right.MemoryBytes,
	}
}

func maxResource64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func mergePolicyWindows(
	blackoutWindows []skalev1alpha1.BlackoutWindow,
	knownEvents []skalev1alpha1.KnownEvent,
) []safety.BlackoutWindow {
	windows := make([]safety.BlackoutWindow, 0, len(blackoutWindows)+len(knownEvents))
	for _, window := range blackoutWindows {
		windows = append(windows, safety.BlackoutWindow{
			Name:   window.Name,
			Start:  window.Start.Time.UTC(),
			End:    window.End.Time.UTC(),
			Reason: strings.TrimSpace(window.Reason),
		})
	}
	for _, event := range knownEvents {
		reason := strings.TrimSpace(event.Note)
		if reason == "" {
			reason = "known event window"
		}
		windows = append(windows, safety.BlackoutWindow{
			Name:   "known-event:" + event.Name,
			Start:  event.Start.Time.UTC(),
			End:    event.End.Time.UTC(),
			Reason: reason,
		})
	}
	return windows
}
