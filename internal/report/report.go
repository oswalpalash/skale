package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/replay"
)

// Writer renders replay outputs into a transport-specific representation.
type Writer interface {
	Write(ctx context.Context, result replay.Result) ([]byte, error)
}

// JSONWriter renders the structured replay result as indented JSON.
type JSONWriter struct{}

func (JSONWriter) Write(_ context.Context, result replay.Result) ([]byte, error) {
	return json.MarshalIndent(result, "", "  ")
}

// SummaryWriter renders a concise operator-facing replay summary for CLI use.
type SummaryWriter struct{}

func (SummaryWriter) Write(_ context.Context, result replay.Result) ([]byte, error) {
	var buffer bytes.Buffer

	fmt.Fprintf(&buffer, "Replay summary for %s\n", renderTarget(result.Target))
	fmt.Fprintf(&buffer, "status: %s\n", result.Status)
	fmt.Fprintf(
		&buffer,
		"window: %s to %s (step %s, lookback %s)\n",
		formatTimestamp(result.Window.Start),
		formatTimestamp(result.Window.End),
		formatSeconds(result.StepSeconds),
		formatSeconds(result.LookbackSeconds),
	)
	fmt.Fprintf(
		&buffer,
		"telemetry: %s",
		renderInlineState(result.TelemetryReadiness.State, result.TelemetryReadiness.Message),
	)
	if len(result.TelemetryReadiness.BlockingReasons) > 0 {
		fmt.Fprintf(&buffer, " [%s]", strings.Join(result.TelemetryReadiness.BlockingReasons, "; "))
	}
	buffer.WriteByte('\n')
	fmt.Fprintf(
		&buffer,
		"baseline: replicas %d -> %d, overload %.2f, excess %.2f\n",
		result.Baseline.StartReplicas,
		result.Baseline.EndReplicas,
		result.Baseline.OverloadMinutesProxy,
		result.Baseline.ExcessHeadroomMinutesProxy,
	)
	fmt.Fprintf(
		&buffer,
		"replay: replicas %d -> %d, overload %.2f, excess %.2f\n",
		result.Replay.StartReplicas,
		result.Replay.EndReplicas,
		result.Replay.OverloadMinutesProxy,
		result.Replay.ExcessHeadroomMinutesProxy,
	)
	fmt.Fprintf(
		&buffer,
		"recommendations: %d events, %d available, %d suppressed, %d unavailable\n",
		result.Replay.RecommendationEventCount,
		result.Replay.AvailableCount,
		result.Replay.SuppressedCount,
		result.Replay.UnavailableCount,
	)
	if len(result.Replay.SuppressionReasonCounts) > 0 {
		fmt.Fprintf(&buffer, "suppression: %s\n", renderCountsInline(result.Replay.SuppressionReasonCounts))
	} else {
		fmt.Fprintf(&buffer, "suppression: none\n")
	}
	fmt.Fprintf(
		&buffer,
		"outcome deltas (replay - baseline): overload %+0.2f, excess %+0.2f\n",
		outcomeDelta(result.Baseline.OverloadMinutesProxy, result.Replay.OverloadMinutesProxy),
		outcomeDelta(result.Baseline.ExcessHeadroomMinutesProxy, result.Replay.ExcessHeadroomMinutesProxy),
	)

	if len(result.ConfidenceNotes) > 0 {
		fmt.Fprintf(&buffer, "confidence notes:\n")
		for _, note := range result.ConfidenceNotes {
			fmt.Fprintf(&buffer, "- %s\n", note)
		}
	}

	fmt.Fprintf(&buffer, "caveats and limitations:\n")
	if len(result.Caveats) == 0 {
		fmt.Fprintf(&buffer, "- none\n")
	} else {
		for _, caveat := range result.Caveats {
			fmt.Fprintf(&buffer, "- %s\n", caveat)
		}
	}

	if len(result.UnsupportedReasons) > 0 {
		fmt.Fprintf(&buffer, "unsupported reasons:\n")
		for _, reason := range result.UnsupportedReasons {
			fmt.Fprintf(&buffer, "- %s\n", reason)
		}
	}

	return buffer.Bytes(), nil
}

// MarkdownWriter renders a concise operator-facing replay summary.
type MarkdownWriter struct{}

func (MarkdownWriter) Write(_ context.Context, result replay.Result) ([]byte, error) {
	var buffer bytes.Buffer

	fmt.Fprintf(&buffer, "# Replay Report\n\n")

	fmt.Fprintf(&buffer, "## Workload Summary\n\n")
	fmt.Fprintf(&buffer, "- workload: `%s`\n", renderTarget(result.Target))
	if strings.TrimSpace(result.Policy.Workload) != "" {
		fmt.Fprintf(&buffer, "- policy workload: `%s`\n", result.Policy.Workload)
	}
	fmt.Fprintf(&buffer, "- status: `%s`\n", result.Status)
	fmt.Fprintf(&buffer, "- generated at: `%s`\n", formatTimestamp(result.GeneratedAt))
	if result.Policy.NodeHeadroomMode != "" {
		fmt.Fprintf(&buffer, "- node headroom mode: `%s`\n", result.Policy.NodeHeadroomMode)
	}

	fmt.Fprintf(&buffer, "\n## Telemetry Readiness\n\n")
	fmt.Fprintf(&buffer, "- state: `%s`\n", result.TelemetryReadiness.State)
	fmt.Fprintf(&buffer, "- message: %s\n", fallbackText(result.TelemetryReadiness.Message, "no telemetry readiness summary was recorded"))
	writeStringList(&buffer, "reasons", result.TelemetryReadiness.Reasons)
	writeStringList(&buffer, "blocking reasons", result.TelemetryReadiness.BlockingReasons)
	if len(result.TelemetryReadiness.Signals) == 0 {
		fmt.Fprintf(&buffer, "- signals: none\n")
	} else {
		for _, signal := range result.TelemetryReadiness.Signals {
			fmt.Fprintf(&buffer, "- signal `%s`: `%s`", signal.Name, signal.State)
			if signal.Required {
				fmt.Fprintf(&buffer, " required")
			}
			if strings.TrimSpace(signal.Message) != "" {
				fmt.Fprintf(&buffer, " - %s", signal.Message)
			}
			fmt.Fprintf(&buffer, "\n")
		}
	}

	fmt.Fprintf(&buffer, "\n## Replay Window\n\n")
	fmt.Fprintf(&buffer, "- start: `%s`\n", formatTimestamp(result.Window.Start))
	fmt.Fprintf(&buffer, "- end: `%s`\n", formatTimestamp(result.Window.End))
	fmt.Fprintf(&buffer, "- step: `%s`\n", formatSeconds(result.StepSeconds))
	fmt.Fprintf(&buffer, "- lookback: `%s`\n", formatSeconds(result.LookbackSeconds))
	fmt.Fprintf(&buffer, "- forecast horizon: `%s`\n", formatSeconds(result.Policy.ForecastHorizonSeconds))
	fmt.Fprintf(&buffer, "- warmup assumption: `%s`\n", formatSeconds(result.Policy.WarmupSeconds))

	fmt.Fprintf(&buffer, "\n## Baseline Summary\n\n")
	writeSummary(&buffer, result.Baseline.Summary)

	fmt.Fprintf(&buffer, "\n## Recommendation Summary\n\n")
	fmt.Fprintf(&buffer, "- evaluations: `%d`\n", result.Replay.EvaluationCount)
	fmt.Fprintf(&buffer, "- available evaluations: `%d`\n", result.Replay.AvailableCount)
	fmt.Fprintf(&buffer, "- suppressed evaluations: `%d`\n", result.Replay.SuppressedCount)
	fmt.Fprintf(&buffer, "- unavailable evaluations: `%d`\n", result.Replay.UnavailableCount)
	fmt.Fprintf(&buffer, "- surfaced recommendation events: `%d`\n", result.Replay.RecommendationEventCount)
	writeCountsSection(&buffer, "forecast models", result.Replay.ForecastModelCounts)
	writeCountsSection(&buffer, "forecast reliability", result.Replay.ReliabilityCounts)

	fmt.Fprintf(&buffer, "\n## Suppression Summary\n\n")
	writeCountsSection(&buffer, "suppression reasons", result.Replay.SuppressionReasonCounts)

	fmt.Fprintf(&buffer, "\n## Outcome Deltas\n\n")
	fmt.Fprintf(&buffer, "- baseline overload-minute proxy: `%.2f`\n", result.Baseline.OverloadMinutesProxy)
	fmt.Fprintf(&buffer, "- replay overload-minute proxy: `%.2f`\n", result.Replay.OverloadMinutesProxy)
	fmt.Fprintf(
		&buffer,
		"- overload-minute proxy delta (replay - baseline): `%+.2f`\n",
		outcomeDelta(result.Baseline.OverloadMinutesProxy, result.Replay.OverloadMinutesProxy),
	)
	fmt.Fprintf(&buffer, "- baseline excess-headroom proxy: `%.2f`\n", result.Baseline.ExcessHeadroomMinutesProxy)
	fmt.Fprintf(&buffer, "- replay excess-headroom proxy: `%.2f`\n", result.Replay.ExcessHeadroomMinutesProxy)
	fmt.Fprintf(
		&buffer,
		"- excess-headroom proxy delta (replay - baseline): `%+.2f`\n",
		outcomeDelta(result.Baseline.ExcessHeadroomMinutesProxy, result.Replay.ExcessHeadroomMinutesProxy),
	)

	fmt.Fprintf(&buffer, "\n## Confidence Notes\n\n")
	writeSectionList(&buffer, result.ConfidenceNotes)

	fmt.Fprintf(&buffer, "\n## Caveats and Limitations\n\n")
	writeSectionList(&buffer, result.Caveats)

	fmt.Fprintf(&buffer, "\n## Recommendation Events\n\n")
	if len(result.RecommendationEvents) == 0 {
		fmt.Fprintf(&buffer, "- none\n")
	} else {
		for _, event := range result.RecommendationEvents {
			activationTime := "unknown"
			if event.ActivationTime != nil {
				activationTime = formatTimestamp(*event.ActivationTime)
			}
			fmt.Fprintf(
				&buffer,
				"- `%s`: `%d` -> `%d` replicas, activation `%s`, confidence `%.2f`, summary: %s\n",
				formatTimestamp(event.EvaluatedAt),
				event.Recommendation.CurrentReplicas,
				event.Recommendation.RecommendedReplicas,
				activationTime,
				event.Forecast.Confidence,
				fallbackText(strings.TrimSpace(event.Summary), "no summary"),
			)
		}
	}

	if len(result.UnsupportedReasons) > 0 {
		fmt.Fprintf(&buffer, "\n## Unsupported Reasons\n\n")
		writeSectionList(&buffer, result.UnsupportedReasons)
	}

	return buffer.Bytes(), nil
}

type countEntry struct {
	key   string
	value int
}

func writeSummary(buffer *bytes.Buffer, summary replay.Summary) {
	fmt.Fprintf(buffer, "- start replicas: `%d`\n", summary.StartReplicas)
	fmt.Fprintf(buffer, "- end replicas: `%d`\n", summary.EndReplicas)
	fmt.Fprintf(buffer, "- min replicas: `%d`\n", summary.MinReplicas)
	fmt.Fprintf(buffer, "- max replicas: `%d`\n", summary.MaxReplicas)
	fmt.Fprintf(buffer, "- mean replicas: `%.2f`\n", summary.MeanReplicas)
	fmt.Fprintf(buffer, "- scale-up events: `%d`\n", summary.ScaleUpEvents)
	fmt.Fprintf(buffer, "- scale-down events: `%d`\n", summary.ScaleDownEvents)
	fmt.Fprintf(buffer, "- overload-minute proxy: `%.2f`\n", summary.OverloadMinutesProxy)
	fmt.Fprintf(buffer, "- excess-headroom proxy: `%.2f`\n", summary.ExcessHeadroomMinutesProxy)
	fmt.Fprintf(buffer, "- scored minutes: `%.2f`\n", summary.ScoredMinutes)
	fmt.Fprintf(buffer, "- unscored minutes: `%.2f`\n", summary.UnscoredMinutes)
}

func writeCountsSection(buffer *bytes.Buffer, label string, counts map[string]int) {
	if len(counts) == 0 {
		fmt.Fprintf(buffer, "- %s: none\n", label)
		return
	}
	for _, entry := range sortedCounts(counts) {
		fmt.Fprintf(buffer, "- %s `%s`: `%d`\n", label, entry.key, entry.value)
	}
}

func writeStringList(buffer *bytes.Buffer, label string, values []string) {
	if len(values) == 0 {
		fmt.Fprintf(buffer, "- %s: none\n", label)
		return
	}
	for _, value := range values {
		fmt.Fprintf(buffer, "- %s: %s\n", label, value)
	}
}

func writeSectionList(buffer *bytes.Buffer, values []string) {
	if len(values) == 0 {
		fmt.Fprintf(buffer, "- none\n")
		return
	}
	for _, value := range values {
		fmt.Fprintf(buffer, "- %s\n", value)
	}
}

func renderTarget(target replay.TargetRef) string {
	switch {
	case target.Namespace != "" && target.Name != "":
		return target.Namespace + "/" + target.Name
	case target.Name != "":
		return target.Name
	default:
		return "unknown"
	}
}

func renderInlineState(state, message string) string {
	if strings.TrimSpace(message) == "" {
		return fallbackText(state, "unknown")
	}
	if strings.TrimSpace(state) == "" {
		return message
	}
	return state + " - " + message
}

func renderCountsInline(counts map[string]int) string {
	if len(counts) == 0 {
		return "none"
	}
	entries := sortedCounts(counts)
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		parts = append(parts, fmt.Sprintf("%s=%d", entry.key, entry.value))
	}
	return strings.Join(parts, ", ")
}

func formatTimestamp(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatSeconds(seconds int64) string {
	return (time.Duration(seconds) * time.Second).String()
}

func fallbackText(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func outcomeDelta(baseline, replayValue float64) float64 {
	return replayValue - baseline
}

func sortedCounts(counts map[string]int) []countEntry {
	out := make([]countEntry, 0, len(counts))
	for key, value := range counts {
		out = append(out, countEntry{key: key, value: value})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].value == out[j].value {
			return out[i].key < out[j].key
		}
		return out[i].value > out[j].value
	})
	return out
}
