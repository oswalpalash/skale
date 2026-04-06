package safety

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// HeadroomStatus is the conservative schedulability estimate for a scale-up candidate.
//
// This is intentionally a sanity check, not a scheduler simulation. The estimate uses only
// request-based CPU and memory summaries plus optional per-node summaries. It does not model
// taints, affinity, topology, pod count limits, extended resources, or future node provisioning.
type HeadroomStatus string

const (
	HeadroomStatusSufficient   HeadroomStatus = "sufficient"
	HeadroomStatusUncertain    HeadroomStatus = "uncertain"
	HeadroomStatusInsufficient HeadroomStatus = "insufficient"
)

// Resources stores normalized Kubernetes resource values in milliCPU and bytes.
type Resources struct {
	CPUMilli    int64 `json:"cpuMilli,omitempty"`
	MemoryBytes int64 `json:"memoryBytes,omitempty"`
}

// AllocatableSummary is the request-based cluster or node summary used by the v1 headroom sanity check.
//
// Requested is the current sum of pod requests, not observed usage. The estimator only compares
// requests against allocatable resources because that is the clearest conservative approximation
// available without pretending to replicate kube-scheduler behavior.
type AllocatableSummary struct {
	Allocatable Resources `json:"allocatable,omitempty"`
	Requested   Resources `json:"requested,omitempty"`
}

// Available returns allocatable minus requested with negative values clamped to zero.
func (s AllocatableSummary) Available() Resources {
	return Resources{
		CPUMilli:    maxInt64(0, s.Allocatable.CPUMilli-s.Requested.CPUMilli),
		MemoryBytes: maxInt64(0, s.Allocatable.MemoryBytes-s.Requested.MemoryBytes),
	}
}

// NodeAllocatableSummary is the schedulable-node input used by the v1 headroom sanity check.
//
// The estimator treats each entry as a current request-based snapshot. It is still only a
// plausibility check because real scheduling can fail for many reasons beyond CPU and memory.
type NodeAllocatableSummary struct {
	Name        string             `json:"name,omitempty"`
	Schedulable bool               `json:"schedulable,omitempty"`
	Summary     AllocatableSummary `json:"summary,omitempty"`
}

// NodeHeadroomSignal is the raw request-based headroom input supplied to the safety engine.
//
// v1 keeps this deliberately narrow. Callers provide workload per-pod requests plus cluster and
// optional per-node allocatable/requested summaries. The estimator turns that into a conservative
// schedulability classification, but it does not claim to solve full cluster scheduling.
type NodeHeadroomSignal struct {
	State          NodeHeadroomState        `json:"state,omitempty"`
	ObservedAt     time.Time                `json:"observedAt,omitempty"`
	PodRequests    Resources                `json:"podRequests,omitempty"`
	ClusterSummary AllocatableSummary       `json:"clusterSummary,omitempty"`
	Nodes          []NodeAllocatableSummary `json:"nodes,omitempty"`
}

// NodeHeadroomAssessment is the structured output returned by the v1 headroom sanity check.
//
// The estimate is safe to surface in explanations and replay because every field comes from simple
// request-based arithmetic. It should still be described as a sanity check rather than evidence that
// kube-scheduler will definitely place the pods.
type NodeHeadroomAssessment struct {
	Status                      HeadroomStatus `json:"status,omitempty"`
	Message                     string         `json:"message,omitempty"`
	ObservedAt                  time.Time      `json:"observedAt,omitempty"`
	AdditionalPodsNeeded        int32          `json:"additionalPodsNeeded,omitempty"`
	EstimatedAdditionalPods     int32          `json:"estimatedAdditionalPods,omitempty"`
	EstimatedByClusterResources int32          `json:"estimatedByClusterResources,omitempty"`
	EstimatedByNodeSummaries    *int32         `json:"estimatedByNodeSummaries,omitempty"`
	SchedulableNodeCount        *int32         `json:"schedulableNodeCount,omitempty"`
	FittingNodeCount            *int32         `json:"fittingNodeCount,omitempty"`
	PodRequests                 Resources      `json:"podRequests,omitempty"`
	ClusterAvailable            Resources      `json:"clusterAvailable,omitempty"`
	LimitingResource            string         `json:"limitingResource,omitempty"`
}

// NodeHeadroomEstimator classifies whether a scale-up looks plausibly schedulable.
type NodeHeadroomEstimator interface {
	Assess(signal *NodeHeadroomSignal, additionalPodsNeeded int32) (NodeHeadroomAssessment, error)
}

// ConservativeNodeHeadroomEstimator is the default v1 request-based headroom sanity check.
//
// It intentionally fails closed when key inputs are missing. The estimator may prove that a
// recommendation is clearly unschedulable, and it may show that requests appear to fit on current
// schedulable nodes, but it never claims to predict actual scheduler success.
type ConservativeNodeHeadroomEstimator struct{}

func (ConservativeNodeHeadroomEstimator) Assess(signal *NodeHeadroomSignal, additionalPodsNeeded int32) (NodeHeadroomAssessment, error) {
	if additionalPodsNeeded < 0 {
		return NodeHeadroomAssessment{}, fmt.Errorf("%w: additional pods needed must be non-negative", ErrInvalidInput)
	}

	if signal == nil {
		return NodeHeadroomAssessment{
			Status:               HeadroomStatusUncertain,
			Message:              "node headroom summary is missing",
			AdditionalPodsNeeded: additionalPodsNeeded,
		}, nil
	}
	if err := signal.Validate(); err != nil {
		return NodeHeadroomAssessment{}, err
	}

	assessment := NodeHeadroomAssessment{
		Status:               HeadroomStatusUncertain,
		ObservedAt:           signal.ObservedAt,
		AdditionalPodsNeeded: additionalPodsNeeded,
		PodRequests:          signal.PodRequests,
		ClusterAvailable:     signal.ClusterSummary.Available(),
	}

	switch normalizedNodeHeadroomState(signal.State) {
	case NodeHeadroomStateMissing:
		assessment.Message = "node headroom summary is missing"
		return assessment, nil
	case NodeHeadroomStateStale:
		assessment.Message = "node headroom summary is stale"
		return assessment, nil
	case NodeHeadroomStateUnsupported:
		assessment.Message = "node headroom summary is unsupported"
		return assessment, nil
	case NodeHeadroomStateReady:
	default:
		return NodeHeadroomAssessment{}, fmt.Errorf("%w: unsupported node headroom state %q", ErrInvalidInput, signal.State)
	}

	if additionalPodsNeeded == 0 {
		assessment.Status = HeadroomStatusSufficient
		assessment.Message = "no additional pods are required"
		return assessment, nil
	}

	cpuKnown := signal.PodRequests.CPUMilli > 0
	memoryKnown := signal.PodRequests.MemoryBytes > 0
	if !cpuKnown && !memoryKnown {
		assessment.Message = "workload CPU and memory requests are both missing"
		return assessment, nil
	}

	clusterCapacity, clusterLimiter := capacityByResources(signal.ClusterSummary.Available(), signal.PodRequests)
	assessment.EstimatedByClusterResources = clusterCapacity
	assessment.LimitingResource = clusterLimiter
	if clusterCapacity < additionalPodsNeeded {
		assessment.Status = HeadroomStatusInsufficient
		assessment.EstimatedAdditionalPods = clusterCapacity
		assessment.Message = fmt.Sprintf(
			"cluster request-based headroom fits at most %d additional pods but %d are needed",
			clusterCapacity,
			additionalPodsNeeded,
		)
		return assessment, nil
	}

	if len(signal.Nodes) == 0 {
		assessment.EstimatedAdditionalPods = clusterCapacity
		assessment.Message = fmt.Sprintf(
			"cluster aggregate request-based headroom fits about %d additional pods, but node-level summaries are missing",
			clusterCapacity,
		)
		if !cpuKnown || !memoryKnown {
			assessment.Message = assessment.Message + missingRequestSuffix(signal.PodRequests)
		}
		return assessment, nil
	}

	schedulableNodes := int32(0)
	fittingNodes := int32(0)
	nodeCapacity := int32(0)
	for _, node := range signal.Nodes {
		if !node.Schedulable {
			continue
		}
		schedulableNodes++
		capacity, _ := capacityByResources(node.Summary.Available(), signal.PodRequests)
		if capacity > 0 {
			fittingNodes++
		}
		nodeCapacity = saturatingAdd(nodeCapacity, capacity)
	}

	assessment.SchedulableNodeCount = int32ValuePtr(schedulableNodes)
	assessment.FittingNodeCount = int32ValuePtr(fittingNodes)
	assessment.EstimatedByNodeSummaries = int32ValuePtr(nodeCapacity)
	assessment.EstimatedAdditionalPods = minInt32(clusterCapacity, nodeCapacity)

	if nodeCapacity < additionalPodsNeeded {
		assessment.Status = HeadroomStatusInsufficient
		assessment.LimitingResource = "node_packing"
		if fittingNodes == 0 {
			assessment.Message = "no schedulable node has enough free requested CPU and memory for one additional pod"
			return assessment, nil
		}
		assessment.Message = fmt.Sprintf(
			"schedulable node summaries fit at most %d additional pods but %d are needed",
			nodeCapacity,
			additionalPodsNeeded,
		)
		return assessment, nil
	}

	if !cpuKnown || !memoryKnown {
		assessment.Message = fmt.Sprintf(
			"request-based headroom could fit about %d additional pods, but %s request is missing",
			assessment.EstimatedAdditionalPods,
			missingRequestDimensions(signal.PodRequests),
		)
		return assessment, nil
	}

	assessment.Status = HeadroomStatusSufficient
	assessment.Message = fmt.Sprintf(
		"request-based headroom could fit about %d additional pods across %d schedulable nodes; %d are needed",
		assessment.EstimatedAdditionalPods,
		schedulableNodes,
		additionalPodsNeeded,
	)
	return assessment, nil
}

// Validate checks the input invariants for the v1 request-based headroom snapshot.
func (s NodeHeadroomSignal) Validate() error {
	switch normalizedNodeHeadroomState(s.State) {
	case NodeHeadroomStateReady, NodeHeadroomStateMissing, NodeHeadroomStateStale, NodeHeadroomStateUnsupported:
	default:
		return fmt.Errorf("%w: unsupported node headroom state %q", ErrInvalidInput, s.State)
	}
	if err := validateResources("pod requests", s.PodRequests); err != nil {
		return err
	}
	if err := validateAllocatableSummary("cluster summary", s.ClusterSummary); err != nil {
		return err
	}
	for _, node := range s.Nodes {
		label := "node summary"
		if node.Name != "" {
			label = fmt.Sprintf("node %q summary", node.Name)
		}
		if err := validateAllocatableSummary(label, node.Summary); err != nil {
			return err
		}
	}
	return nil
}

func validateAllocatableSummary(label string, summary AllocatableSummary) error {
	if err := validateResources(label+" allocatable", summary.Allocatable); err != nil {
		return err
	}
	if err := validateResources(label+" requested", summary.Requested); err != nil {
		return err
	}
	return nil
}

func validateResources(label string, resources Resources) error {
	if resources.CPUMilli < 0 {
		return fmt.Errorf("%w: %s CPU must be non-negative", ErrInvalidInput, label)
	}
	if resources.MemoryBytes < 0 {
		return fmt.Errorf("%w: %s memory must be non-negative", ErrInvalidInput, label)
	}
	return nil
}

func capacityByResources(available Resources, pod Resources) (int32, string) {
	type dimension struct {
		name     string
		capacity int32
	}

	limits := make([]dimension, 0, 2)
	if pod.CPUMilli > 0 {
		limits = append(limits, dimension{
			name:     "cpu",
			capacity: capacityByDimension(available.CPUMilli, pod.CPUMilli),
		})
	}
	if pod.MemoryBytes > 0 {
		limits = append(limits, dimension{
			name:     "memory",
			capacity: capacityByDimension(available.MemoryBytes, pod.MemoryBytes),
		})
	}
	if len(limits) == 0 {
		return 0, ""
	}

	limit := limits[0]
	for _, candidate := range limits[1:] {
		if candidate.capacity < limit.capacity {
			limit = candidate
		}
	}
	return limit.capacity, limit.name
}

func capacityByDimension(available, request int64) int32 {
	if request <= 0 || available <= 0 {
		return 0
	}
	capacity := available / request
	if capacity > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(capacity)
}

func missingRequestSuffix(requests Resources) string {
	dimensions := missingRequestDimensions(requests)
	if dimensions == "" {
		return ""
	}
	return "; " + dimensions + " request is missing"
}

func missingRequestDimensions(requests Resources) string {
	missing := make([]string, 0, 2)
	if requests.CPUMilli <= 0 {
		missing = append(missing, "CPU")
	}
	if requests.MemoryBytes <= 0 {
		missing = append(missing, "memory")
	}
	return strings.Join(missing, " and ")
}

func normalizedNodeHeadroomState(state NodeHeadroomState) NodeHeadroomState {
	if state == "" {
		return NodeHeadroomStateMissing
	}
	return state
}

func minInt32(a, b int32) int32 {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func int32ValuePtr(value int32) *int32 {
	return &value
}
