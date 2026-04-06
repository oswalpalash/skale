package main

import (
	"context"
	"time"

	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replay"
	"github.com/oswalpalash/skale/internal/replayinput"
	"github.com/oswalpalash/skale/internal/safety"
)

type replayInputDocument = replayinput.Document
type replayWindowDocument = replayinput.WindowDocument
type replayPolicyDocument = replayinput.PolicyDocument
type replayOptionsDocument = replayinput.OptionsDocument
type replayReadinessOptionsDocument = replayinput.ReadinessOptionsDocument
type durationValue = replayinput.DurationValue

type staticProvider struct {
	target   metrics.Target
	snapshot metrics.Snapshot
}

func (p staticProvider) LoadWindow(ctx context.Context, target metrics.Target, window metrics.Window) (metrics.Snapshot, error) {
	return replayinput.StaticProvider{
		Target:   p.target,
		Snapshot: p.snapshot,
	}.LoadWindow(ctx, target, window)
}

func loadReplayInput(path string) (replay.Spec, metrics.Provider, error) {
	spec, provider, err := replayinput.LoadFile(path)
	if err != nil {
		return replay.Spec{}, nil, err
	}
	static, ok := provider.(replayinput.StaticProvider)
	if !ok {
		return spec, provider, nil
	}
	return spec, staticProvider{target: static.Target, snapshot: static.Snapshot}, nil
}

func validWindow(window metrics.Window) bool {
	return replayinput.ValidWindow(window)
}

func inferredSnapshotWindow(snapshot metrics.Snapshot, replayWindow metrics.Window, lookback time.Duration) metrics.Window {
	return replayinput.InferredSnapshotWindow(snapshot, replayWindow, lookback)
}

func seriesBounds(snapshot metrics.Snapshot) (time.Time, time.Time, bool) {
	return replayinput.SeriesBounds(snapshot)
}

type blackoutWindow = safety.BlackoutWindow
