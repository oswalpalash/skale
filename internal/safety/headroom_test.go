package safety

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type headroomFixture struct {
	AdditionalPodsNeeded        int32              `json:"additionalPodsNeeded"`
	ExpectedStatus              HeadroomStatus     `json:"expectedStatus"`
	ExpectedEstimatedAdditional int32              `json:"expectedEstimatedAdditionalPods"`
	Signal                      NodeHeadroomSignal `json:"signal"`
}

func TestConservativeNodeHeadroomEstimatorFixtures(t *testing.T) {
	t.Parallel()

	estimator := ConservativeNodeHeadroomEstimator{}
	fixtures := []string{
		"headroom_sufficient.json",
		"headroom_uncertain_aggregate_only.json",
		"headroom_insufficient_node_packing.json",
	}

	for _, name := range fixtures {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture := loadHeadroomFixture(t, name)
			assessment, err := estimator.Assess(&fixture.Signal, fixture.AdditionalPodsNeeded)
			if err != nil {
				t.Fatalf("Assess() error = %v", err)
			}

			if assessment.Status != fixture.ExpectedStatus {
				t.Fatalf("status = %q, want %q", assessment.Status, fixture.ExpectedStatus)
			}
			if assessment.EstimatedAdditionalPods != fixture.ExpectedEstimatedAdditional {
				t.Fatalf("estimated additional pods = %d, want %d", assessment.EstimatedAdditionalPods, fixture.ExpectedEstimatedAdditional)
			}
			if fixture.ExpectedStatus == HeadroomStatusSufficient && assessment.EstimatedByNodeSummaries == nil {
				t.Fatal("expected node summary estimate to be recorded")
			}
		})
	}
}

func TestConservativeNodeHeadroomEstimatorReturnsUncertainWhenRequestDimensionMissing(t *testing.T) {
	t.Parallel()

	fixture := loadHeadroomFixture(t, "headroom_sufficient.json")
	fixture.Signal.PodRequests.MemoryBytes = 0

	assessment, err := ConservativeNodeHeadroomEstimator{}.Assess(&fixture.Signal, fixture.AdditionalPodsNeeded)
	if err != nil {
		t.Fatalf("Assess() error = %v", err)
	}

	if assessment.Status != HeadroomStatusUncertain {
		t.Fatalf("status = %q, want %q", assessment.Status, HeadroomStatusUncertain)
	}
	if !strings.Contains(assessment.Message, "memory request is missing") {
		t.Fatalf("expected uncertainty message about memory request, got %q", assessment.Message)
	}
}

func TestConservativeNodeHeadroomEstimatorRejectsNegativeResources(t *testing.T) {
	t.Parallel()

	_, err := ConservativeNodeHeadroomEstimator{}.Assess(&NodeHeadroomSignal{
		State: NodeHeadroomStateReady,
		PodRequests: Resources{
			CPUMilli:    -100,
			MemoryBytes: 1024,
		},
	}, 1)
	if err == nil {
		t.Fatal("expected invalid input error")
	}
}

func loadHeadroomFixture(t *testing.T, name string) headroomFixture {
	t.Helper()

	bytes, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}

	var fixture headroomFixture
	if err := json.Unmarshal(bytes, &fixture); err != nil {
		t.Fatalf("unmarshal fixture %q: %v", name, err)
	}
	return fixture
}
