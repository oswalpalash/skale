package demofixture

import (
	"context"
	"testing"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replay"
)

type staticProvider struct {
	snapshot metrics.Snapshot
}

func (p staticProvider) LoadWindow(context.Context, metrics.Target, metrics.Window) (metrics.Snapshot, error) {
	return p.snapshot, nil
}

func TestDesignPartner24HourDocumentBuildsFullDayReplayWindow(t *testing.T) {
	t.Parallel()

	document := DesignPartner24HourDocument()

	if document.Target.Namespace != "skale-demo" || document.Target.Name != "checkout-api" {
		t.Fatalf("target = %s/%s, want skale-demo/checkout-api", document.Target.Namespace, document.Target.Name)
	}
	if document.Step.Duration != 15*time.Minute {
		t.Fatalf("step = %s, want 15m", document.Step.Duration)
	}
	if got, want := document.Window.End.Sub(document.Window.Start), 23*time.Hour+45*time.Minute; got != want {
		t.Fatalf("replay window span = %s, want %s", got, want)
	}
	if !document.Snapshot.Window.End.Equal(document.Window.End) {
		t.Fatalf("snapshot end = %s, want replay window end %s", document.Snapshot.Window.End, document.Window.End)
	}
	if len(document.Snapshot.Demand.Samples) < 300 {
		t.Fatalf("expected richer historical fixture, got %d demand samples", len(document.Snapshot.Demand.Samples))
	}
}

func TestDesignPartner24HourDocumentYieldsCredibleReplayWin(t *testing.T) {
	t.Parallel()

	document := DesignPartner24HourDocument()
	spec, snapshot := document.ReplaySpecAndSnapshot()

	engine := replay.Engine{
		Metrics: staticProvider{snapshot: snapshot},
	}
	result, err := engine.Run(context.Background(), spec)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if result.Status != replay.StatusComplete {
		t.Fatalf("status = %q, want %q", result.Status, replay.StatusComplete)
	}
	if result.Replay.RecommendationEventCount < 4 {
		t.Fatalf("recommendation events = %d, want at least 4", result.Replay.RecommendationEventCount)
	}
	if result.Replay.OverloadMinutesProxy >= result.Baseline.OverloadMinutesProxy {
		t.Fatalf(
			"replay overload %.2f should improve baseline overload %.2f",
			result.Replay.OverloadMinutesProxy,
			result.Baseline.OverloadMinutesProxy,
		)
	}

	last := result.Evaluations[len(result.Evaluations)-1]
	if last.Decision == nil {
		t.Fatal("expected controller-aligned final evaluation to include a decision explanation")
	}
	if last.Decision.Outcome.State != "available" {
		t.Fatalf("final decision state = %q, want available", last.Decision.Outcome.State)
	}
	if last.Decision.Outcome.FinalRecommendedReplicas < 3 {
		t.Fatalf("final recommended replicas = %d, want at least 3", last.Decision.Outcome.FinalRecommendedReplicas)
	}
}
