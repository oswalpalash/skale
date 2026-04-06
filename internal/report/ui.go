package report

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	htmltemplate "html/template"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/replay"
)

const defaultUIFocusWindow = 10 * time.Minute

// HTMLWriter renders a self-contained replay UI as static HTML.
//
// The view stays intentionally offline and recommendation-first:
// - one focused timeline on a shared time axis
// - no live backend, no dashboard server, no actuation
// - explicit caveats when replay data is partial or unsupported
type HTMLWriter struct {
	FocusWindow time.Duration
}

type replayHTMLView struct {
	Title               string
	Workload            string
	Status              string
	StatusTone          string
	TelemetryState      string
	TelemetryMessage    string
	GeneratedAt         string
	ReplayWindowLabel   string
	FocusWindowBadge    string
	FocusWindowLabel    string
	WarmupLabel         string
	CooldownLabel       string
	RecommendationCount int
	AvailableCount      int
	SuppressedCount     int
	UnavailableCount    int
	OverloadSummary     string
	ExcessSummary       string
	OverloadDelta       string
	ExcessDelta         string
	SuppressionSummary  string
	ForecastSummary     string
	SignalSummaries     []string
	EventCards          []htmlEventCard
	ConfidenceNotes     []string
	Caveats             []string
	UnsupportedReasons  []string
	InlineSummary       string
	ChartDataJSON       htmltemplate.JS
	SVG                 htmltemplate.HTML
}

type htmlEventCard struct {
	Headline string
	Meta     string
	Summary  string
}

type htmlWindow struct {
	Start time.Time
	End   time.Time
	Full  bool
}

type htmlEvalPoint struct {
	At               time.Time
	Demand           float64
	BaselineReplicas int32
	ReplayReplicas   int32
	Suppressed       bool
	SuppressionLabel string
}

type htmlEvent struct {
	EvaluatedAt     time.Time
	Activation      *time.Time
	From            int32
	To              int32
	PredictedDemand float64
	Confidence      float64
}

type svgBand struct {
	top    float64
	bottom float64
}

type replayChartData struct {
	PlotLeft     float64          `json:"plotLeft"`
	PlotRight    float64          `json:"plotRight"`
	PlotTop      float64          `json:"plotTop"`
	PlotBottom   float64          `json:"plotBottom"`
	FocusWindow  string           `json:"focusWindow"`
	Points       []chartPointData `json:"points"`
	Events       []chartEventData `json:"events"`
	InitialIndex int              `json:"initialIndex"`
}

type chartPointData struct {
	Index            int     `json:"index"`
	X                float64 `json:"x"`
	TimeLabel        string  `json:"timeLabel"`
	Demand           float64 `json:"demand"`
	DemandY          float64 `json:"demandY"`
	BaselineReplicas int32   `json:"baselineReplicas"`
	BaselineY        float64 `json:"baselineY"`
	ReplayReplicas   int32   `json:"replayReplicas"`
	ReplayY          float64 `json:"replayY"`
	Suppressed       bool    `json:"suppressed"`
	SuppressionLabel string  `json:"suppressionLabel,omitempty"`
}

type chartEventData struct {
	EvalIndex       int     `json:"evalIndex"`
	EvalX           float64 `json:"evalX"`
	EvalTimeLabel   string  `json:"evalTimeLabel"`
	ReadyX          float64 `json:"readyX"`
	ReadyTimeLabel  string  `json:"readyTimeLabel"`
	FromReplicas    int32   `json:"fromReplicas"`
	ToReplicas      int32   `json:"toReplicas"`
	PredictedDemand float64 `json:"predictedDemand"`
	Confidence      float64 `json:"confidence"`
}

func (w HTMLWriter) Write(_ context.Context, result replay.Result) ([]byte, error) {
	view, err := buildReplayHTMLView(result, normalizedUIFocusWindow(w.FocusWindow))
	if err != nil {
		return nil, err
	}

	var buffer bytes.Buffer
	if err := replayUITemplate.Execute(&buffer, view); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func buildReplayHTMLView(result replay.Result, focusWidth time.Duration) (replayHTMLView, error) {
	view := replayHTMLView{
		Title:               "Replay recommendation path",
		Workload:            renderTarget(result.Target),
		Status:              string(result.Status),
		StatusTone:          htmlStatusTone(result),
		TelemetryState:      fallbackText(result.TelemetryReadiness.State, "unknown"),
		TelemetryMessage:    fallbackText(result.TelemetryReadiness.Message, "telemetry summary was not recorded"),
		GeneratedAt:         formatTimestamp(result.GeneratedAt),
		ReplayWindowLabel:   fmt.Sprintf("%s to %s", formatTimestamp(result.Window.Start), formatTimestamp(result.Window.End)),
		WarmupLabel:         formatSeconds(result.Policy.WarmupSeconds),
		CooldownLabel:       formatOptionalSeconds(result.Policy.CooldownSeconds, "off"),
		RecommendationCount: result.Replay.RecommendationEventCount,
		AvailableCount:      result.Replay.AvailableCount,
		SuppressedCount:     result.Replay.SuppressedCount,
		UnavailableCount:    result.Replay.UnavailableCount,
		OverloadSummary: fmt.Sprintf(
			"%.2f -> %.2f proxy minutes",
			result.Baseline.OverloadMinutesProxy,
			result.Replay.OverloadMinutesProxy,
		),
		ExcessSummary: fmt.Sprintf(
			"%.2f -> %.2f proxy minutes",
			result.Baseline.ExcessHeadroomMinutesProxy,
			result.Replay.ExcessHeadroomMinutesProxy,
		),
		OverloadDelta:      formatSignedFloat(outcomeDelta(result.Baseline.OverloadMinutesProxy, result.Replay.OverloadMinutesProxy)),
		ExcessDelta:        formatSignedFloat(outcomeDelta(result.Baseline.ExcessHeadroomMinutesProxy, result.Replay.ExcessHeadroomMinutesProxy)),
		SuppressionSummary: fallbackText(renderCountsInline(result.Replay.SuppressionReasonCounts), "none"),
		ForecastSummary:    fallbackText(renderCountsInline(result.Replay.ForecastModelCounts), "none"),
		ConfidenceNotes:    append([]string(nil), result.ConfidenceNotes...),
		Caveats:            append([]string(nil), result.Caveats...),
		UnsupportedReasons: append([]string(nil), result.UnsupportedReasons...),
	}

	if view.ConfidenceNotes == nil {
		view.ConfidenceNotes = []string{}
	}
	if view.Caveats == nil {
		view.Caveats = []string{}
	}
	if view.UnsupportedReasons == nil {
		view.UnsupportedReasons = []string{}
	}

	for _, signal := range result.TelemetryReadiness.Signals {
		state := fallbackText(signal.State, "unknown")
		message := fallbackText(signal.Message, "no details")
		view.SignalSummaries = append(view.SignalSummaries, fmt.Sprintf("%s: %s - %s", signal.Name, state, message))
	}
	if len(view.SignalSummaries) == 0 {
		view.SignalSummaries = append(view.SignalSummaries, "no telemetry signal details were recorded")
	}

	for _, event := range result.RecommendationEvents {
		activation := "activation unavailable"
		if event.ActivationTime != nil {
			activation = "ready " + formatTimestamp(*event.ActivationTime)
		}
		view.EventCards = append(view.EventCards, htmlEventCard{
			Headline: fmt.Sprintf("%s: %d -> %d replicas", formatTimestamp(event.EvaluatedAt), event.Recommendation.CurrentReplicas, event.Recommendation.RecommendedReplicas),
			Meta:     fmt.Sprintf("%s, confidence %.2f, predicted demand %.0f", activation, event.Forecast.Confidence, event.Forecast.PredictedDemand),
			Summary:  fallbackText(strings.TrimSpace(event.Summary), "no recommendation summary"),
		})
	}
	if len(view.EventCards) == 0 {
		view.EventCards = append(view.EventCards, htmlEventCard{
			Headline: "No surfaced recommendation events",
			Meta:     "Replay did not produce any replay event cards for this window.",
			Summary:  "Check suppression reasons, telemetry readiness, and unsupported reasons below.",
		})
	}

	focus := selectHTMLFocusWindow(result, focusWidth)
	if focus.Full {
		view.FocusWindowBadge = "focus full window"
		view.FocusWindowLabel = "Chart shows the full replay window."
	} else {
		windowLabel := trimmedDuration(focus.End.Sub(focus.Start))
		view.FocusWindowBadge = "focus " + windowLabel
		view.FocusWindowLabel = fmt.Sprintf(
			"Chart focuses on %s around predictive activity inside the replay window %s to %s.",
			windowLabel,
			result.Window.Start.UTC().Format("15:04:05"),
			result.Window.End.UTC().Format("15:04:05"),
		)
	}

	view.InlineSummary = buildHTMLInlineSummary(result)
	view.SVG = htmltemplate.HTML(buildTimelineSVG(result, focus))
	chartData, err := buildReplayChartData(result, focus)
	if err != nil {
		return replayHTMLView{}, err
	}
	view.ChartDataJSON = chartData
	return view, nil
}

func normalizedUIFocusWindow(width time.Duration) time.Duration {
	if width <= 0 {
		return defaultUIFocusWindow
	}
	return width
}

func selectHTMLFocusWindow(result replay.Result, width time.Duration) htmlWindow {
	start := result.Window.Start.UTC()
	end := result.Window.End.UTC()
	if start.IsZero() || end.IsZero() || !end.After(start) {
		return htmlWindow{Start: start, End: end, Full: true}
	}
	if width <= 0 || end.Sub(start) <= width || len(result.Evaluations) == 0 {
		return htmlWindow{Start: start, End: end, Full: true}
	}

	anchorStart := start
	anchorEnd := end
	if len(result.RecommendationEvents) > 0 {
		anchorStart = result.RecommendationEvents[0].EvaluatedAt.UTC()
		anchorEnd = anchorStart
		for _, event := range result.RecommendationEvents {
			if event.EvaluatedAt.Before(anchorStart) {
				anchorStart = event.EvaluatedAt.UTC()
			}
			if event.EvaluatedAt.After(anchorEnd) {
				anchorEnd = event.EvaluatedAt.UTC()
			}
			if event.ActivationTime != nil && event.ActivationTime.After(anchorEnd) {
				anchorEnd = event.ActivationTime.UTC()
			}
		}
	} else {
		midpoint := start.Add(end.Sub(start) / 2)
		anchorStart = midpoint
		anchorEnd = midpoint
	}

	span := anchorEnd.Sub(anchorStart)
	if span >= width {
		windowStart := clampWindowStart(anchorStart, start, end, width)
		return htmlWindow{Start: windowStart, End: windowStart.Add(width)}
	}
	padding := (width - span) / 2
	windowStart := clampWindowStart(anchorStart.Add(-padding), start, end, width)
	return htmlWindow{Start: windowStart, End: windowStart.Add(width)}
}

func clampWindowStart(candidate, minimum, maximum time.Time, width time.Duration) time.Time {
	if candidate.Before(minimum) {
		return minimum
	}
	if candidate.Add(width).After(maximum) {
		return maximum.Add(-width)
	}
	return candidate
}

func buildHTMLInlineSummary(result replay.Result) string {
	return fmt.Sprintf(
		"%s telemetry with %d predictive recommendation events in replay. Overload delta %s and excess-headroom delta %s across the replay window.",
		fallbackText(result.TelemetryReadiness.State, "unknown"),
		result.Replay.RecommendationEventCount,
		formatSignedFloat(outcomeDelta(result.Baseline.OverloadMinutesProxy, result.Replay.OverloadMinutesProxy)),
		formatSignedFloat(outcomeDelta(result.Baseline.ExcessHeadroomMinutesProxy, result.Replay.ExcessHeadroomMinutesProxy)),
	)
}

func htmlStatusTone(result replay.Result) string {
	switch {
	case result.Status == replay.StatusUnsupported:
		return "status-unsupported"
	case strings.EqualFold(result.TelemetryReadiness.State, "degraded"):
		return "status-degraded"
	default:
		return "status-ready"
	}
}

func buildReplayChartData(result replay.Result, focus htmlWindow) (htmltemplate.JS, error) {
	if len(result.Evaluations) == 0 {
		return "null", nil
	}

	const (
		leftMargin  = 172.0
		rightMargin = 40.0
		width       = 1280.0
	)

	demandPanel := svgBand{top: 122, bottom: 280}
	replicaPanel := svgBand{top: 340, bottom: 666}
	replicaLineBand := svgBand{top: 490, bottom: 582}

	focusPoints := filterEvalPoints(result.Evaluations, focus.Start, focus.End)
	if len(focusPoints) == 0 {
		return "null", nil
	}

	plotRight := width - rightMargin
	mainX := timeScale(focus.Start, focus.End, leftMargin, plotRight)
	demandMax := maxDemandValue(focusPoints, result.RecommendationEvents)
	replicaMax := maxReplicaValue(focusPoints, result.RecommendationEvents)
	demandY := valueScale(0, demandMax, demandPanel.bottom, demandPanel.top)
	replicaY := valueScale(0, float64(replicaMax), replicaLineBand.bottom, replicaLineBand.top)

	points := make([]chartPointData, 0, len(focusPoints))
	for index, point := range focusPoints {
		points = append(points, chartPointData{
			Index:            index,
			X:                mainX(point.At),
			TimeLabel:        clockLabel(point.At),
			Demand:           point.Demand,
			DemandY:          demandY(point.Demand),
			BaselineReplicas: point.BaselineReplicas,
			BaselineY:        replicaY(float64(point.BaselineReplicas)),
			ReplayReplicas:   point.ReplayReplicas,
			ReplayY:          replicaY(float64(point.ReplayReplicas)),
			Suppressed:       point.Suppressed,
			SuppressionLabel: point.SuppressionLabel,
		})
	}

	events := filterEvents(result.RecommendationEvents, focus.Start, focus.End)
	eventData := make([]chartEventData, 0, len(events))
	initialIndex := len(points) / 2
	if len(events) > 0 {
		initialIndex = nearestPointIndex(points, mainX(events[0].EvaluatedAt))
	}
	for _, event := range events {
		readyX := mainX(event.EvaluatedAt)
		readyTime := "pending"
		evalX := mainX(event.EvaluatedAt)
		if event.Activation != nil {
			readyX = mainX(*event.Activation)
			readyTime = clockLabel(*event.Activation)
		}
		eventData = append(eventData, chartEventData{
			EvalIndex:       nearestPointIndex(points, evalX),
			EvalX:           evalX,
			EvalTimeLabel:   clockLabel(event.EvaluatedAt),
			ReadyX:          readyX,
			ReadyTimeLabel:  readyTime,
			FromReplicas:    event.From,
			ToReplicas:      event.To,
			PredictedDemand: event.PredictedDemand,
			Confidence:      event.Confidence,
		})
	}

	scene := replayChartData{
		PlotLeft:     leftMargin,
		PlotRight:    plotRight,
		PlotTop:      demandPanel.top,
		PlotBottom:   replicaPanel.bottom,
		FocusWindow:  trimmedDuration(focus.End.Sub(focus.Start)),
		Points:       points,
		Events:       eventData,
		InitialIndex: initialIndex,
	}
	encoded, err := json.Marshal(scene)
	if err != nil {
		return "", err
	}
	return htmltemplate.JS(encoded), nil
}

func buildTimelineSVG(result replay.Result, focus htmlWindow) string {
	if len(result.Evaluations) == 0 {
		return buildPlaceholderSVG(result, "No replay evaluations were available for timeline rendering.")
	}

	const (
		width       = 1280.0
		height      = 760.0
		leftMargin  = 172.0
		rightMargin = 40.0
		plotWidth   = width - leftMargin - rightMargin
	)

	demandPanel := svgBand{top: 122, bottom: 280}
	replicaPanel := svgBand{top: 340, bottom: 666}
	warmupTrack := svgBand{top: 430, bottom: 462}
	replicaLineBand := svgBand{top: 490, bottom: 582}
	heldTrack := svgBand{top: 610, bottom: 640}

	focusPoints := filterEvalPoints(result.Evaluations, focus.Start, focus.End)
	if len(focusPoints) == 0 {
		return buildPlaceholderSVG(result, "Replay evaluations exist, but none were available in the selected focus window.")
	}

	mainX := timeScale(focus.Start, focus.End, leftMargin, leftMargin+plotWidth)

	demandMax := maxDemandValue(focusPoints, result.RecommendationEvents)
	replicaMax := maxReplicaValue(focusPoints, result.RecommendationEvents)
	demandY := valueScale(0, demandMax, demandPanel.bottom, demandPanel.top)
	replicaY := valueScale(0, float64(replicaMax), replicaLineBand.bottom, replicaLineBand.top)

	suppressionMarks := buildSuppressionMarks(focusPoints)
	events := filterEvents(result.RecommendationEvents, focus.Start, focus.End)

	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "<svg id=\"replay-timeline\" class=\"timeline-svg\" viewBox=\"0 0 %.0f %.0f\" role=\"img\" aria-label=\"Replay lifecycle timeline\">", width, height)
	fmt.Fprintf(&buffer, "<defs>")
	fmt.Fprintf(&buffer, "<linearGradient id=\"demand-fill\" x1=\"0\" y1=\"0\" x2=\"0\" y2=\"1\"><stop offset=\"0%%\" stop-color=\"rgba(38,112,176,0.22)\"/><stop offset=\"100%%\" stop-color=\"rgba(38,112,176,0.02)\"/></linearGradient>")
	fmt.Fprintf(&buffer, "<filter id=\"soft-shadow\" x=\"-20%%\" y=\"-20%%\" width=\"140%%\" height=\"140%%\"><feDropShadow dx=\"0\" dy=\"22\" stdDeviation=\"18\" flood-color=\"rgba(0,0,0,0.18)\"/></filter>")
	fmt.Fprintf(&buffer, "</defs>")
	fmt.Fprintf(&buffer, "<rect x=\"10\" y=\"10\" width=\"1260\" height=\"740\" rx=\"30\" fill=\"#f6f2ea\" stroke=\"rgba(10,27,34,0.14)\" filter=\"url(#soft-shadow)\"/>")
	fmt.Fprintf(&buffer, "<text x=\"%.1f\" y=\"56\" fill=\"#13252c\" font-size=\"30\" font-family=\"Iowan Old Style, Palatino Linotype, Georgia, serif\">Demand and replica path</text>", leftMargin)
	fmt.Fprintf(&buffer, "<text x=\"%.1f\" y=\"84\" fill=\"rgba(19,37,44,0.72)\" font-size=\"14\">Top: observed demand. Bottom: actual replicas, predictive ready replicas, and separate timing rows for warmup and held checks.</text>", leftMargin)
	fmt.Fprintf(&buffer, "<text x=\"%.1f\" y=\"104\" fill=\"rgba(19,37,44,0.58)\" font-size=\"12\" letter-spacing=\"1.4\">MAIN CHART %s</text>", leftMargin, html.EscapeString(strings.ToUpper(trimmedDuration(focus.End.Sub(focus.Start))+" WINDOW")))

	drawLaneLabelCard(&buffer, 24, demandPanel, "Demand", "Observed load")
	drawLaneLabelCard(&buffer, 24, replicaPanel, "Replicas", "Actual vs predictive")

	drawPanelFrame(&buffer, leftMargin, width-rightMargin, demandPanel, "Observed demand")
	drawPanelFrame(&buffer, leftMargin, width-rightMargin, replicaPanel, "Actual replicas vs predictive ready replicas")
	drawReplicaPanelKey(&buffer, leftMargin, replicaPanel)
	drawTimingTrack(&buffer, leftMargin, width-rightMargin, warmupTrack, "warmup", "#138a7e")
	drawTimingTrack(&buffer, leftMargin, width-rightMargin, heldTrack, "held check", "#d99139")

	drawAxisGrid(&buffer, demandPanel, leftMargin, width-rightMargin, 4, func(index int) string {
		return fmt.Sprintf("%.0f", demandMax*float64(index)/4)
	}, func(index int) float64 {
		return demandY(demandMax * float64(index) / 4)
	})
	drawAxisGrid(&buffer, replicaLineBand, leftMargin, width-rightMargin, replicaMax, func(index int) string {
		return fmt.Sprintf("%d", index)
	}, func(index int) float64 {
		return replicaY(float64(index))
	})

	drawTimeTicks(&buffer, focus.Start, focus.End, mainX, demandPanel.top, replicaPanel.bottom, height-28)

	drawEventGuides(&buffer, events, mainX, demandPanel.top, replicaPanel.bottom)
	drawDemandSeries(&buffer, focusPoints, mainX, demandY, demandPanel)
	drawWarmupBands(&buffer, events, mainX, warmupTrack)
	drawReplicaSeries(&buffer, focusPoints, mainX, replicaY, focus.End)
	drawReplicaEndLabels(&buffer, focusPoints, mainX, replicaY, focus.End)
	drawForecastMarkers(&buffer, events, mainX, demandY, demandMax)
	drawSuppressionRow(&buffer, suppressionMarks, mainX, heldTrack)
	drawInteractiveCursor(&buffer, leftMargin, width-rightMargin, demandPanel.top, replicaPanel.bottom)

	fmt.Fprintf(&buffer, "</svg>")
	return buffer.String()
}

func buildPlaceholderSVG(result replay.Result, message string) string {
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "<svg viewBox=\"0 0 1240 320\" role=\"img\" aria-label=\"Replay lifecycle timeline unavailable\">")
	fmt.Fprintf(&buffer, "<rect x=\"10\" y=\"10\" width=\"1220\" height=\"300\" rx=\"28\" fill=\"#f6f2ea\" stroke=\"rgba(10,27,34,0.10)\"/>")
	fmt.Fprintf(&buffer, "<text x=\"72\" y=\"92\" fill=\"#13252c\" font-size=\"28\" font-family=\"Iowan Old Style, Palatino Linotype, Georgia, serif\">Replay timeline unavailable</text>")
	fmt.Fprintf(&buffer, "<text x=\"72\" y=\"130\" fill=\"rgba(19,37,44,0.76)\" font-size=\"15\">%s</text>", html.EscapeString(message))
	if len(result.UnsupportedReasons) > 0 {
		for index, reason := range result.UnsupportedReasons {
			fmt.Fprintf(&buffer, "<text x=\"72\" y=\"%d\" fill=\"rgba(19,37,44,0.74)\" font-size=\"13\">- %s</text>", 168+(index*24), html.EscapeString(reason))
		}
	}
	fmt.Fprintf(&buffer, "</svg>")
	return buffer.String()
}

func drawPanelFrame(buffer *bytes.Buffer, left, right float64, bounds svgBand, label string) {
	fmt.Fprintf(
		buffer,
		"<rect x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" rx=\"20\" fill=\"rgba(255,255,255,0.74)\" stroke=\"rgba(19,37,44,0.08)\"/>",
		left-12, bounds.top-26, right-left+24, bounds.bottom-bounds.top+40,
	)
	fmt.Fprintf(
		buffer,
		"<text x=\"%.1f\" y=\"%.1f\" fill=\"rgba(19,37,44,0.82)\" font-size=\"12\" letter-spacing=\"1.6\">%s</text>",
		left, bounds.top-8, html.EscapeString(strings.ToUpper(label)),
	)
}

func drawLaneLabelCard(buffer *bytes.Buffer, x float64, bounds svgBand, title, detail string) {
	const width = 124.0
	height := math.Max(78, bounds.bottom-bounds.top-18)
	y := bounds.top + ((bounds.bottom - bounds.top - height) / 2)
	fmt.Fprintf(
		buffer,
		"<rect x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" rx=\"18\" fill=\"#13252c\" opacity=\"0.96\"/>",
		x, y, width, height,
	)
	fmt.Fprintf(
		buffer,
		"<text x=\"%.1f\" y=\"%.1f\" fill=\"#f6f2ea\" font-size=\"16\" font-weight=\"650\">%s</text>",
		x+16, y+30, html.EscapeString(title),
	)
	fmt.Fprintf(
		buffer,
		"<text x=\"%.1f\" y=\"%.1f\" fill=\"rgba(246,242,234,0.72)\" font-size=\"12\">%s</text>",
		x+16, y+52, html.EscapeString(detail),
	)
}

func drawReplicaPanelKey(buffer *bytes.Buffer, left float64, bounds svgBand) {
	fmt.Fprintf(buffer, "<text x=\"%.1f\" y=\"%.1f\" fill=\"rgba(19,37,44,0.72)\" font-size=\"12\">orange = actual recorded replicas. green = predictive ready replicas. timing rows below make warmup and held checks explicit.</text>", left, bounds.top+28)
	fmt.Fprintf(buffer, "<text x=\"%.1f\" y=\"%.1f\" fill=\"rgba(19,37,44,0.68)\" font-size=\"11\">green row = evaluation to ready. amber row = checks that did not surface; hover for the reason such as cooldown.</text>", left, bounds.top+62)
}

func drawTimingTrack(buffer *bytes.Buffer, left, right float64, bounds svgBand, label, color string) {
	mid := bounds.top + ((bounds.bottom - bounds.top) / 2)
	fmt.Fprintf(
		buffer,
		"<rect x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" rx=\"12\" fill=\"rgba(19,37,44,0.04)\" stroke=\"rgba(19,37,44,0.08)\"/>",
		left, bounds.top, right-left, bounds.bottom-bounds.top,
	)
	fmt.Fprintf(buffer, "<line x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"%s\" stroke-opacity=\"0.28\" stroke-width=\"2\"/>", left+6, mid, right-6, mid, color)
	fmt.Fprintf(buffer, "<text x=\"%.1f\" y=\"%.1f\" fill=\"rgba(19,37,44,0.62)\" font-size=\"10.5\" letter-spacing=\"1.1\">%s</text>", left+10, bounds.top-6, html.EscapeString(strings.ToUpper(label)))
}

func drawEventGuides(buffer *bytes.Buffer, events []htmlEvent, x func(time.Time) float64, top, bottom float64) {
	for _, event := range events {
		evalX := x(event.EvaluatedAt)
		fmt.Fprintf(buffer, "<line class=\"event-guide\" x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"rgba(19,138,126,0.28)\" stroke-width=\"1.5\" stroke-dasharray=\"8 7\"/>", evalX, top, evalX, bottom)
		if event.Activation != nil {
			readyX := x(*event.Activation)
			fmt.Fprintf(buffer, "<line class=\"event-guide\" x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"rgba(19,138,126,0.18)\" stroke-width=\"2\"/>", readyX, top, readyX, bottom)
		}
	}
}

func drawAxisGrid(buffer *bytes.Buffer, _ svgBand, left, right float64, ticks int, label func(int) string, position func(int) float64) {
	for index := 0; index <= ticks; index++ {
		y := position(index)
		fmt.Fprintf(buffer, "<line x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"rgba(19,37,44,0.10)\" stroke-width=\"1\"/>", left, y, right, y)
		fmt.Fprintf(buffer, "<text x=\"%.1f\" y=\"%.1f\" text-anchor=\"end\" fill=\"rgba(19,37,44,0.66)\" font-size=\"11\">%s</text>", left-12, y+4, html.EscapeString(label(index)))
	}
}

type timeTickSpec struct {
	Step   time.Duration
	Layout string
}

func drawTimeTicks(buffer *bytes.Buffer, start, end time.Time, scale func(time.Time) float64, top, bottom, y float64) {
	spec := selectTimeTickSpec(start, end)
	first := start.Truncate(spec.Step)
	if first.Before(start) {
		first = first.Add(spec.Step)
	}
	for tick := first; !tick.After(end); tick = tick.Add(spec.Step) {
		x := scale(tick)
		fmt.Fprintf(buffer, "<line x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"rgba(19,37,44,0.08)\" stroke-width=\"1\"/>", x, top, x, bottom)
		fmt.Fprintf(buffer, "<text x=\"%.1f\" y=\"%.1f\" text-anchor=\"middle\" fill=\"rgba(19,37,44,0.66)\" font-size=\"11\">%s</text>", x, y, html.EscapeString(formatTimeTickLabel(tick, start, end, spec.Layout)))
	}
}

func selectTimeTickSpec(start, end time.Time) timeTickSpec {
	span := end.Sub(start)
	switch {
	case span <= 45*time.Minute:
		return timeTickSpec{Step: 5 * time.Minute, Layout: "15:04"}
	case span <= 3*time.Hour:
		return timeTickSpec{Step: 15 * time.Minute, Layout: "15:04"}
	case span <= 8*time.Hour:
		return timeTickSpec{Step: 30 * time.Minute, Layout: "15:04"}
	case span <= 18*time.Hour:
		return timeTickSpec{Step: time.Hour, Layout: "15:04"}
	case span <= 36*time.Hour:
		return timeTickSpec{Step: 2 * time.Hour, Layout: "15:04"}
	case span <= 72*time.Hour:
		return timeTickSpec{Step: 4 * time.Hour, Layout: "15:04"}
	default:
		return timeTickSpec{Step: 6 * time.Hour, Layout: "15:04"}
	}
}

func formatTimeTickLabel(tick, start, end time.Time, layout string) string {
	if layout == "" {
		layout = "15:04"
	}
	span := end.Sub(start)
	if span >= 18*time.Hour {
		if tick.Equal(start.Truncate(selectTimeTickSpec(start, end).Step)) || tick.Hour() == 0 {
			return tick.UTC().Format("Jan 2 15:04")
		}
	}
	return tick.UTC().Format(layout)
}

func drawFocusBand(buffer *bytes.Buffer, scale func(time.Time) float64, start, end time.Time, bounds svgBand) {
	x := scale(start)
	width := scale(end) - x
	fmt.Fprintf(buffer, "<rect x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" rx=\"14\" fill=\"rgba(38,112,176,0.12)\" stroke=\"rgba(38,112,176,0.22)\"/>", x, bounds.top, width, bounds.bottom-bounds.top)
}

func drawDemandSeries(buffer *bytes.Buffer, points []htmlEvalPoint, x func(time.Time) float64, y func(float64) float64, bounds svgBand) {
	if len(points) == 0 {
		return
	}
	var line bytes.Buffer
	var area bytes.Buffer
	for index, point := range points {
		command := "L"
		if index == 0 {
			command = "M"
			fmt.Fprintf(&area, "%s %.1f %.1f ", command, x(point.At), bounds.bottom)
			fmt.Fprintf(&area, "L %.1f %.1f ", x(point.At), y(point.Demand))
		}
		fmt.Fprintf(&line, "%s %.1f %.1f ", command, x(point.At), y(point.Demand))
		if index > 0 {
			fmt.Fprintf(&area, "L %.1f %.1f ", x(point.At), y(point.Demand))
		}
	}
	last := points[len(points)-1]
	fmt.Fprintf(&area, "L %.1f %.1f Z", x(last.At), bounds.bottom)
	fmt.Fprintf(buffer, "<path class=\"series-area\" d=\"%s\" fill=\"url(#demand-fill)\"/>", strings.TrimSpace(area.String()))
	fmt.Fprintf(buffer, "<path class=\"series-line demand-line\" d=\"%s\" fill=\"none\" stroke=\"#2670b0\" stroke-width=\"4\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>", strings.TrimSpace(line.String()))
}

func drawContextDemand(buffer *bytes.Buffer, points []htmlEvalPoint, x func(time.Time) float64, y func(float64) float64, _ svgBand) {
	if len(points) == 0 {
		return
	}
	var line bytes.Buffer
	for index, point := range points {
		command := "L"
		if index == 0 {
			command = "M"
		}
		fmt.Fprintf(&line, "%s %.1f %.1f ", command, x(point.At), y(point.Demand))
	}
	fmt.Fprintf(buffer, "<path d=\"%s\" fill=\"none\" stroke=\"rgba(38,112,176,0.72)\" stroke-width=\"2\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>", strings.TrimSpace(line.String()))
}

func drawReplicaSeries(buffer *bytes.Buffer, points []htmlEvalPoint, x func(time.Time) float64, y func(float64) float64, end time.Time) {
	if len(points) == 0 {
		return
	}
	fmt.Fprintf(
		buffer,
		"<path class=\"series-line baseline-line\" d=\"%s\" fill=\"none\" stroke=\"#d99139\" stroke-width=\"4.5\" stroke-dasharray=\"12 8\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>",
		stepPath(points, x, func(point htmlEvalPoint) float64 { return y(float64(point.BaselineReplicas)) }, end),
	)
	fmt.Fprintf(
		buffer,
		"<path class=\"series-line replay-line\" d=\"%s\" fill=\"none\" stroke=\"#138a7e\" stroke-width=\"5\" stroke-linecap=\"round\" stroke-linejoin=\"round\"/>",
		stepPath(points, x, func(point htmlEvalPoint) float64 { return y(float64(point.ReplayReplicas)) }, end),
	)
}

func drawReplicaEndLabels(buffer *bytes.Buffer, points []htmlEvalPoint, x func(time.Time) float64, y func(float64) float64, end time.Time) {
	if len(points) == 0 {
		return
	}
	last := points[len(points)-1]
	labelX := x(end) + 10
	actualY := y(float64(last.BaselineReplicas)) + 4
	predictiveY := y(float64(last.ReplayReplicas)) + 4
	if math.Abs(actualY-predictiveY) < 16 {
		actualY -= 10
		predictiveY += 14
	}
	fmt.Fprintf(buffer, "<text x=\"%.1f\" y=\"%.1f\" fill=\"#d99139\" font-size=\"12\" font-weight=\"650\">actual %d</text>", labelX, actualY, last.BaselineReplicas)
	fmt.Fprintf(buffer, "<text x=\"%.1f\" y=\"%.1f\" fill=\"#138a7e\" font-size=\"12\" font-weight=\"650\">predictive ready %d</text>", labelX, predictiveY, last.ReplayReplicas)
}

func drawForecastMarkers(buffer *bytes.Buffer, events []htmlEvent, x func(time.Time) float64, y func(float64) float64, _ float64) {
	for _, event := range events {
		if event.Activation == nil {
			continue
		}
		cx := x(*event.Activation)
		cy := y(event.PredictedDemand)
		fmt.Fprintf(buffer, "<circle class=\"forecast-point\" cx=\"%.1f\" cy=\"%.1f\" r=\"7\" fill=\"#f6f2ea\" stroke=\"#138a7e\" stroke-width=\"3\"/>", cx, cy)
	}
}

func drawWarmupBands(buffer *bytes.Buffer, events []htmlEvent, x func(time.Time) float64, bounds svgBand) {
	lineY := bounds.top + ((bounds.bottom - bounds.top) / 2)
	for _, event := range events {
		if event.Activation == nil {
			continue
		}
		startX := x(event.EvaluatedAt)
		endX := x(*event.Activation)
		width := endX - startX
		if width <= 0 {
			continue
		}
		fmt.Fprintf(
			buffer,
			"<rect class=\"warmup-band\" x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" rx=\"12\" fill=\"rgba(19,138,126,0.16)\" stroke=\"rgba(19,138,126,0.26)\"/>",
			startX, bounds.top+4, width, bounds.bottom-bounds.top-8,
		)
		fmt.Fprintf(buffer, "<circle class=\"warmup-band\" cx=\"%.1f\" cy=\"%.1f\" r=\"4.5\" fill=\"#138a7e\"/>", startX, lineY)
		fmt.Fprintf(buffer, "<circle class=\"warmup-band\" cx=\"%.1f\" cy=\"%.1f\" r=\"5\" fill=\"#f6f2ea\" stroke=\"#138a7e\" stroke-width=\"2\"/>", endX, lineY)
	}
}

func drawRecommendationEvents(buffer *bytes.Buffer, events []htmlEvent, x func(time.Time) float64, bounds svgBand) {
	rowY := bounds.top + 88
	for _, event := range events {
		startX := x(event.EvaluatedAt)
		endX := startX
		if event.Activation != nil {
			endX = x(*event.Activation)
		}
		fmt.Fprintf(buffer, "<line x1=\"%.1f\" y1=\"%.1f\" x2=\"%.1f\" y2=\"%.1f\" stroke=\"#138a7e\" stroke-width=\"5\" stroke-linecap=\"round\"/>", startX, rowY, endX, rowY)
		fmt.Fprintf(buffer, "<circle cx=\"%.1f\" cy=\"%.1f\" r=\"7\" fill=\"#138a7e\"/>", startX, rowY)
		fmt.Fprintf(buffer, "<circle cx=\"%.1f\" cy=\"%.1f\" r=\"7\" fill=\"#f6f2ea\" stroke=\"#138a7e\" stroke-width=\"3\"/>", endX, rowY)
	}
}

func drawRecommendationChips(buffer *bytes.Buffer, events []htmlEvent, x func(time.Time) float64, left, right float64, bounds svgBand) {
	for index, event := range events {
		startX := x(event.EvaluatedAt)
		endX := startX
		timing := "ready time unavailable"
		if event.Activation != nil {
			endX = x(*event.Activation)
			timing = fmt.Sprintf("eval %s -> ready %s", clockLabel(event.EvaluatedAt), clockLabel(*event.Activation))
		}
		centerX := (startX + endX) / 2
		boxWidth := 190.0
		boxHeight := 42.0
		boxX := clampFloat(centerX-(boxWidth/2), left+6, right-boxWidth-6)
		boxY := bounds.top + 12 + float64(index%2)*46
		fmt.Fprintf(
			buffer,
			"<rect x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" rx=\"14\" fill=\"rgba(19,138,126,0.12)\" stroke=\"rgba(19,138,126,0.26)\"/>",
			boxX, boxY, boxWidth, boxHeight,
		)
		fmt.Fprintf(
			buffer,
			"<text x=\"%.1f\" y=\"%.1f\" fill=\"#138a7e\" font-size=\"12\" font-weight=\"650\" text-anchor=\"middle\">%d→%d replicas</text>",
			boxX+(boxWidth/2), boxY+17, event.From, event.To,
		)
		fmt.Fprintf(
			buffer,
			"<text x=\"%.1f\" y=\"%.1f\" fill=\"rgba(19,138,126,0.76)\" font-size=\"10.5\" text-anchor=\"middle\">%s</text>",
			boxX+(boxWidth/2), boxY+31, html.EscapeString(timing),
		)
	}
}

func drawSuppressionRow(buffer *bytes.Buffer, marks []time.Time, x func(time.Time) float64, bounds svgBand) {
	rowY := bounds.top + ((bounds.bottom - bounds.top) / 2)
	for _, mark := range marks {
		cx := x(mark)
		fmt.Fprintf(buffer, "<rect class=\"suppression-mark\" x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" rx=\"6\" fill=\"rgba(217,145,57,0.22)\" stroke=\"#d99139\" stroke-width=\"1.5\"/>", cx-6, rowY-6, 12.0, 12.0)
	}
}

func drawInteractiveCursor(buffer *bytes.Buffer, left, right, top, bottom float64) {
	fmt.Fprintf(buffer, "<g id=\"interactive-cursor\" aria-hidden=\"true\">")
	fmt.Fprintf(buffer, "<rect id=\"timeline-hitbox\" x=\"%.1f\" y=\"%.1f\" width=\"%.1f\" height=\"%.1f\" fill=\"transparent\" pointer-events=\"all\"/>", left, top, right-left, bottom-top)
	fmt.Fprintf(buffer, "<line id=\"cursor-line\" x1=\"0\" y1=\"0\" x2=\"0\" y2=\"0\" stroke=\"rgba(19,37,44,0.55)\" stroke-width=\"1.5\" stroke-dasharray=\"7 6\"/>")
	fmt.Fprintf(buffer, "<circle id=\"cursor-demand\" cx=\"0\" cy=\"0\" r=\"7\" fill=\"#f6f2ea\" stroke=\"#2670b0\" stroke-width=\"3\"/>")
	fmt.Fprintf(buffer, "<circle id=\"cursor-baseline\" cx=\"0\" cy=\"0\" r=\"7\" fill=\"#f6f2ea\" stroke=\"#d99139\" stroke-width=\"3\"/>")
	fmt.Fprintf(buffer, "<circle id=\"cursor-replay\" cx=\"0\" cy=\"0\" r=\"7\" fill=\"#f6f2ea\" stroke=\"#138a7e\" stroke-width=\"3\"/>")
	fmt.Fprintf(buffer, "</g>")
}

func filterEvalPoints(evaluations []replay.Evaluation, start, end time.Time) []htmlEvalPoint {
	points := make([]htmlEvalPoint, 0, len(evaluations))
	for _, evaluation := range evaluations {
		at := evaluation.EvaluatedAt.UTC()
		if at.Before(start) || at.After(end) {
			continue
		}
		suppressed := false
		if len(evaluation.SuppressionReasons) > 0 {
			suppressed = true
		} else if evaluation.Decision != nil && len(evaluation.Decision.Outcome.SuppressionReasons) > 0 {
			suppressed = true
		}
		points = append(points, htmlEvalPoint{
			At:               at,
			Demand:           evaluation.CurrentDemand,
			BaselineReplicas: evaluation.BaselineReplicas,
			ReplayReplicas:   evaluation.SimulatedReplicas,
			Suppressed:       suppressed,
			SuppressionLabel: suppressionLabelForEvaluation(evaluation),
		})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].At.Before(points[j].At) })
	return points
}

func suppressionLabelForEvaluation(evaluation replay.Evaluation) string {
	reasons := evaluation.SuppressionReasons
	if len(reasons) == 0 && evaluation.Decision != nil {
		reasons = evaluation.Decision.Outcome.SuppressionReasons
	}
	if len(reasons) == 0 {
		return ""
	}
	for _, reason := range reasons {
		if strings.TrimSpace(reason.Code) == "cooldown_active" {
			return "held by cooldown"
		}
	}
	first := reasons[0]
	if code := strings.TrimSpace(first.Code); code != "" {
		return strings.ReplaceAll(code, "_", " ")
	}
	if message := strings.TrimSpace(first.Message); message != "" {
		return message
	}
	return "suppressed check"
}

func filterEvents(events []replay.RecommendationEvent, start, end time.Time) []htmlEvent {
	out := make([]htmlEvent, 0, len(events))
	for _, event := range events {
		activation := event.ActivationTime
		if activation != nil {
			normalized := activation.UTC()
			activation = &normalized
		}
		if activation == nil {
			if event.EvaluatedAt.Before(start) || event.EvaluatedAt.After(end) {
				continue
			}
		} else if activation.Before(start) || event.EvaluatedAt.After(end) {
			continue
		}
		out = append(out, htmlEvent{
			EvaluatedAt:     event.EvaluatedAt.UTC(),
			Activation:      activation,
			From:            event.Recommendation.CurrentReplicas,
			To:              event.Recommendation.RecommendedReplicas,
			PredictedDemand: math.Max(event.Forecast.PredictedDemand, 0),
			Confidence:      event.Forecast.Confidence,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EvaluatedAt.Before(out[j].EvaluatedAt) })
	return out
}

func buildSuppressionMarks(points []htmlEvalPoint) []time.Time {
	out := make([]time.Time, 0)
	for _, point := range points {
		if point.Suppressed {
			out = append(out, point.At)
		}
	}
	return out
}

func inferStepFromPoints(points []htmlEvalPoint) time.Duration {
	if len(points) < 2 {
		return 0
	}
	step := points[1].At.Sub(points[0].At)
	if step <= 0 {
		return 0
	}
	return step
}

func nearestPointIndex(points []chartPointData, x float64) int {
	if len(points) == 0 {
		return 0
	}
	best := 0
	bestDistance := math.MaxFloat64
	for index, point := range points {
		distance := math.Abs(point.X - x)
		if distance < bestDistance {
			best = index
			bestDistance = distance
		}
	}
	return best
}

func stepPath(points []htmlEvalPoint, x func(time.Time) float64, y func(htmlEvalPoint) float64, end time.Time) string {
	if len(points) == 0 {
		return ""
	}
	var buffer bytes.Buffer
	fmt.Fprintf(&buffer, "M %.1f %.1f", x(points[0].At), y(points[0]))
	for index := 1; index < len(points); index++ {
		fmt.Fprintf(&buffer, " H %.1f V %.1f", x(points[index].At), y(points[index]))
	}
	if !end.Before(points[len(points)-1].At) {
		fmt.Fprintf(&buffer, " H %.1f", x(end))
	}
	return buffer.String()
}

func timeScale(start, end time.Time, left, right float64) func(time.Time) float64 {
	duration := end.Sub(start)
	if duration <= 0 {
		return func(time.Time) float64 { return left }
	}
	return func(value time.Time) float64 {
		if value.Before(start) {
			return left
		}
		if value.After(end) {
			return right
		}
		ratio := float64(value.Sub(start)) / float64(duration)
		return left + ratio*(right-left)
	}
}

func valueScale(minValue, maxValue, bottom, top float64) func(float64) float64 {
	if maxValue <= minValue {
		return func(float64) float64 { return bottom }
	}
	return func(value float64) float64 {
		if value < minValue {
			value = minValue
		}
		if value > maxValue {
			value = maxValue
		}
		ratio := (value - minValue) / (maxValue - minValue)
		return bottom - ratio*(bottom-top)
	}
}

func maxDemandValue(points []htmlEvalPoint, events []replay.RecommendationEvent) float64 {
	maxValue := 0.0
	for _, point := range points {
		if point.Demand > maxValue {
			maxValue = point.Demand
		}
	}
	for _, event := range events {
		if event.Forecast.PredictedDemand > maxValue {
			maxValue = event.Forecast.PredictedDemand
		}
	}
	if maxValue <= 0 {
		return 1
	}
	return math.Ceil(maxValue/20) * 20
}

func maxReplicaValue(points []htmlEvalPoint, events []replay.RecommendationEvent) int {
	maxValue := 1
	for _, point := range points {
		maxValue = max(maxValue, int(point.BaselineReplicas))
		maxValue = max(maxValue, int(point.ReplayReplicas))
	}
	for _, event := range events {
		maxValue = max(maxValue, int(event.Recommendation.RecommendedReplicas))
	}
	return maxValue + 1
}

func trimmedDuration(value time.Duration) string {
	if value <= 0 {
		return "0s"
	}
	if value%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(value/time.Hour))
	}
	if value%time.Minute == 0 {
		hours := value / time.Hour
		minutes := (value % time.Hour) / time.Minute
		if hours > 0 {
			return fmt.Sprintf("%dh%dm", hours, minutes)
		}
		return fmt.Sprintf("%dm", minutes)
	}
	if value%time.Second == 0 {
		minutes := value / time.Minute
		seconds := (value % time.Minute) / time.Second
		if minutes > 0 {
			return fmt.Sprintf("%dm%ds", minutes, seconds)
		}
		return fmt.Sprintf("%ds", seconds)
	}
	return value.String()
}

func formatSignedFloat(value float64) string {
	return fmt.Sprintf("%+.2f", value)
}

func formatOptionalSeconds(seconds int64, zeroLabel string) string {
	if seconds <= 0 {
		return zeroLabel
	}
	return formatSeconds(seconds)
}

func clockLabel(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	return value.UTC().Format("15:04:05")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func clampFloat(value, minimum, maximum float64) float64 {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

var replayUITemplate = htmltemplate.Must(htmltemplate.New("replay-ui").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Skale Replay UI</title>
  <style>
    :root {
      --bg: #08131a;
      --panel: rgba(11, 24, 31, 0.84);
      --panel-strong: rgba(12, 28, 36, 0.96);
      --line: rgba(164, 198, 209, 0.18);
      --text: #e8f1ec;
      --muted: #9fb9b2;
      --teal: #59d2c3;
      --amber: #ffbf69;
      --paper: #f1eee5;
      --blue: #8bc3ff;
      --status-ready: rgba(89, 210, 195, 0.14);
      --status-degraded: rgba(255, 191, 105, 0.14);
      --status-unsupported: rgba(255, 125, 107, 0.14);
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      color: var(--text);
      background:
        radial-gradient(circle at top left, rgba(89,210,195,0.16), transparent 28%),
        radial-gradient(circle at top right, rgba(255,191,105,0.10), transparent 22%),
        linear-gradient(180deg, #09141c 0%, #071118 100%);
      font-family: "Avenir Next", "Segoe UI", sans-serif;
    }
    body::before {
      content: "";
      position: fixed;
      inset: 0;
      pointer-events: none;
      background-image:
        linear-gradient(rgba(255,255,255,0.02) 1px, transparent 1px),
        linear-gradient(90deg, rgba(255,255,255,0.02) 1px, transparent 1px);
      background-size: 24px 24px;
      mask-image: radial-gradient(circle at center, black 55%, transparent 92%);
    }
    main {
      width: min(1460px, calc(100vw - 40px));
      margin: 28px auto 38px;
      position: relative;
      z-index: 1;
    }
    .hero, .panel, .detail-card {
      background: linear-gradient(180deg, rgba(15, 31, 40, 0.90), rgba(8, 20, 27, 0.88));
      border: 1px solid rgba(233, 240, 237, 0.08);
      border-radius: 28px;
      box-shadow: 0 24px 60px rgba(0, 0, 0, 0.30);
    }
    .hero {
      padding: 28px 30px 24px;
      position: relative;
      overflow: hidden;
    }
    .hero::after {
      content: "";
      position: absolute;
      inset: auto -12% -58% 48%;
      height: 280px;
      background: radial-gradient(circle, rgba(89,210,195,0.24) 0%, transparent 62%);
      pointer-events: none;
    }
    .kicker {
      display: inline-flex;
      align-items: center;
      gap: 10px;
      padding: 8px 14px;
      background: rgba(255,255,255,0.05);
      border: 1px solid rgba(255,255,255,0.08);
      border-radius: 999px;
      color: var(--muted);
      font-size: 12px;
      letter-spacing: 0.14em;
      text-transform: uppercase;
    }
    .dot {
      width: 9px;
      height: 9px;
      border-radius: 50%;
      background: var(--teal);
      box-shadow: 0 0 0 8px rgba(89,210,195,0.12);
    }
    h1 {
      margin: 18px 0 10px;
      font-family: "Iowan Old Style", "Palatino Linotype", Georgia, serif;
      font-size: clamp(34px, 4vw, 60px);
      font-weight: 600;
      line-height: 0.95;
      letter-spacing: -0.03em;
    }
    .subtitle {
      margin: 0;
      max-width: 860px;
      color: var(--muted);
      font-size: 18px;
      line-height: 1.55;
    }
    .badges {
      display: flex;
      flex-wrap: wrap;
      gap: 10px;
      margin-top: 22px;
    }
    .badge {
      padding: 10px 14px;
      border-radius: 14px;
      background: rgba(255,255,255,0.05);
      border: 1px solid rgba(255,255,255,0.08);
      font-size: 13px;
      color: var(--paper);
    }
    .badge.{{.StatusTone}} {
      background: var(--status-ready);
    }
    .badge.status-degraded { background: var(--status-degraded); }
    .badge.status-unsupported { background: var(--status-unsupported); }
    .grid {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 14px;
      margin-top: 16px;
    }
    .card {
      background: var(--panel);
      border: 1px solid rgba(255,255,255,0.07);
      border-radius: 22px;
      padding: 18px 18px 16px;
      backdrop-filter: blur(8px);
      box-shadow: inset 0 1px 0 rgba(255,255,255,0.03);
    }
    .label {
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: 0.14em;
      color: var(--muted);
      margin-bottom: 10px;
    }
    .value {
      font-size: 34px;
      line-height: 1;
      font-weight: 650;
      margin-bottom: 8px;
    }
    .card p {
      margin: 0;
      color: var(--muted);
      line-height: 1.5;
      font-size: 14px;
    }
    .panel {
      margin-top: 18px;
      padding: 22px 22px 26px;
    }
    .panel h2, .detail-card h3 {
      margin: 0 0 8px;
      font-family: "Iowan Old Style", "Palatino Linotype", Georgia, serif;
      font-size: 28px;
      font-weight: 600;
      letter-spacing: -0.02em;
    }
    .panel p {
      margin: 0 0 16px;
      color: var(--muted);
      line-height: 1.55;
    }
    .timeline-shell {
      position: relative;
      overflow: hidden;
      border-radius: 26px;
      background: rgba(255,255,255,0.04);
      border: 1px solid rgba(255,255,255,0.06);
      padding: 8px;
    }
    .timeline-svg {
      width: 100%;
      height: auto;
      display: block;
    }
    .timeline-svg .series-line {
      stroke-dasharray: 2400;
      stroke-dashoffset: 2400;
      animation: lineReveal 1.4s cubic-bezier(.2,.8,.2,1) forwards;
    }
    .timeline-svg .baseline-line { animation-delay: 0.12s; }
    .timeline-svg .replay-line { animation-delay: 0.2s; }
    .timeline-svg .demand-line { animation-delay: 0.04s; }
    .timeline-svg .warmup-band,
    .timeline-svg .forecast-point,
    .timeline-svg .suppression-mark,
    .timeline-svg .event-guide,
    .timeline-svg .series-area {
      animation: fadeRise .6s ease both;
    }
    .timeline-svg #interactive-cursor {
      transition: opacity 160ms ease;
    }
    .timeline-svg #cursor-line,
    .timeline-svg #cursor-demand,
    .timeline-svg #cursor-baseline,
    .timeline-svg #cursor-replay {
      opacity: 0.95;
    }
    .timeline-hud {
      margin-top: 14px;
      background: rgba(255,255,255,0.05);
      color: var(--paper);
      border-radius: 18px;
      border: 1px solid rgba(255,255,255,0.08);
      padding: 14px 16px 12px;
    }
    .hud-kicker {
      font-size: 11px;
      letter-spacing: 0.14em;
      text-transform: uppercase;
      color: rgba(159,185,178,0.82);
      margin-bottom: 8px;
    }
    .hud-time {
      font-family: "Iowan Old Style", "Palatino Linotype", Georgia, serif;
      font-size: 28px;
      line-height: 1;
      margin-bottom: 12px;
    }
    .hud-grid {
      display: grid;
      grid-template-columns: repeat(4, minmax(0, 1fr));
      gap: 10px;
    }
    .hud-item {
      padding: 10px 10px 8px;
      border-radius: 14px;
      background: rgba(255,255,255,0.05);
      border: 1px solid rgba(255,255,255,0.06);
    }
    .hud-item strong {
      display: block;
      font-size: 11px;
      letter-spacing: 0.12em;
      text-transform: uppercase;
      color: rgba(159,185,178,0.78);
      margin-bottom: 6px;
    }
    .hud-item span {
      font-size: 20px;
      font-weight: 650;
      color: var(--paper);
    }
    .hud-status {
      display: flex;
      flex-wrap: wrap;
      gap: 8px;
      margin-top: 12px;
    }
    .hud-pill {
      padding: 6px 9px;
      border-radius: 999px;
      background: rgba(255,255,255,0.06);
      font-size: 12px;
      color: rgba(232,241,236,0.86);
    }
    .legend {
      display: flex;
      flex-wrap: wrap;
      gap: 14px;
      margin-bottom: 14px;
      color: var(--muted);
      font-size: 13px;
    }
    .legend span {
      display: inline-flex;
      align-items: center;
      gap: 8px;
    }
    .panel-note {
      margin-bottom: 14px;
      padding: 12px 14px;
      border-radius: 18px;
      background: rgba(255,255,255,0.05);
      border: 1px solid rgba(255,255,255,0.08);
      color: var(--muted);
      font-size: 14px;
      line-height: 1.55;
    }
    .swatch {
      width: 16px;
      height: 4px;
      border-radius: 999px;
      display: inline-block;
    }
    .dot-swatch {
      width: 10px;
      height: 10px;
      border-radius: 50%;
      display: inline-block;
    }
    .detail-grid {
      display: grid;
      grid-template-columns: 1.2fr 1fr 1fr;
      gap: 14px;
      margin-top: 18px;
    }
    .detail-card {
      padding: 20px 20px 18px;
    }
    .detail-card p, .detail-card li {
      color: var(--muted);
      line-height: 1.55;
      font-size: 14px;
    }
    .detail-card ul {
      margin: 0;
      padding-left: 18px;
    }
    .event-card {
      padding: 14px 0;
      border-top: 1px solid rgba(255,255,255,0.08);
    }
    .event-card:first-of-type {
      border-top: 0;
      padding-top: 0;
    }
    .event-card strong {
      display: block;
      margin-bottom: 4px;
      font-size: 15px;
      color: var(--paper);
    }
    .event-card span {
      display: block;
      margin-bottom: 8px;
      color: rgba(159,185,178,0.88);
      font-size: 12px;
      letter-spacing: 0.03em;
    }
    footer {
      margin-top: 14px;
      color: var(--muted);
      font-size: 13px;
      text-align: right;
    }
    @media (max-width: 1180px) {
      .grid { grid-template-columns: 1fr 1fr; }
      .detail-grid { grid-template-columns: 1fr 1fr; }
      .hud-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
    }
    @media (max-width: 760px) {
      main {
        width: min(100vw - 20px, 100%);
        margin: 14px auto 24px;
      }
      .hero, .panel, .detail-card {
        padding: 18px;
        border-radius: 22px;
      }
      .grid, .detail-grid {
        grid-template-columns: 1fr;
      }
      .hud-grid {
        grid-template-columns: 1fr;
      }
    }
    @keyframes lineReveal {
      to { stroke-dashoffset: 0; }
    }
    @keyframes fadeRise {
      from { opacity: 0; transform: translateY(10px); }
      to { opacity: 1; transform: translateY(0); }
    }
  </style>
</head>
<body>
  <main>
    <section class="hero">
      <div class="kicker"><span class="dot"></span>Skale replay UI</div>
      <h1>{{.Workload}}<br>{{.Title}}</h1>
      <p class="subtitle">{{.InlineSummary}}</p>
      <div class="badges">
        <div class="badge {{.StatusTone}}">status {{.Status}}</div>
        <div class="badge">telemetry {{.TelemetryState}}</div>
        <div class="badge">{{.FocusWindowBadge}}</div>
        <div class="badge">warmup {{.WarmupLabel}}</div>
        <div class="badge">cooldown {{.CooldownLabel}}</div>
      </div>
      <div class="grid">
        <div class="card">
          <div class="label">Recommendation Events</div>
          <div class="value">{{.RecommendationCount}}</div>
          <p>{{.ReplayWindowLabel}}. {{.ForecastSummary}} forecast model counts across replay evaluations.</p>
        </div>
        <div class="card">
          <div class="label">Evaluation Mix</div>
          <div class="value">{{.AvailableCount}}/{{.SuppressedCount}}</div>
          <p>{{.AvailableCount}} available, {{.SuppressedCount}} suppressed, {{.UnavailableCount}} unavailable evaluations.</p>
        </div>
        <div class="card">
          <div class="label">Overload Proxy</div>
          <div class="value">{{.OverloadDelta}}</div>
          <p>{{.OverloadSummary}}. Negative is better because the predictive path reduced proxy overload minutes.</p>
        </div>
        <div class="card">
          <div class="label">Excess Headroom</div>
          <div class="value">{{.ExcessDelta}}</div>
          <p>{{.ExcessSummary}}. This remains a proxy for extra ready capacity, not a cost guarantee.</p>
        </div>
      </div>
    </section>

    <section class="panel">
      <h2>Lifecycle Timeline</h2>
      <p>{{.TelemetryMessage}} {{.FocusWindowLabel}}</p>
      <div class="panel-note">Warmup is the time from a surfaced evaluation to predictive replicas being ready. Cooldown is the minimum wait before another surfaced replica change. The green timing row shows the {{.WarmupLabel}} warmup spans. The amber timing row shows held checks; hover a mark to see whether the hold came from cooldown or another safety reason.</div>
      <div class="legend">
        <span><i class="swatch" style="background:#2670b0"></i>Observed demand</span>
        <span><i class="swatch" style="background:#d99139"></i>Actual replicas</span>
        <span><i class="swatch" style="background:#138a7e"></i>Predictive ready replicas</span>
        <span><i class="dot-swatch" style="border:2px solid #138a7e; background:transparent"></i>Forecast at ready time</span>
        <span><i class="dot-swatch" style="background:#138a7e"></i>Warmup timing row</span>
        <span><i class="dot-swatch" style="background:#d99139"></i>Held-check timing row</span>
      </div>
      <div class="timeline-shell">
        {{.SVG}}
      </div>
      <aside class="timeline-hud" id="timeline-hud" aria-live="polite">
        <div class="hud-kicker">Time Slice</div>
        <div class="hud-time" id="hud-time">--:--:--</div>
        <div class="hud-grid">
          <div class="hud-item">
            <strong>Demand</strong>
            <span id="hud-demand">--</span>
          </div>
          <div class="hud-item">
            <strong>Actual Replicas</strong>
            <span id="hud-baseline">--</span>
          </div>
          <div class="hud-item">
            <strong>Predictive Ready Replicas</strong>
            <span id="hud-replay">--</span>
          </div>
          <div class="hud-item">
            <strong>Predictive Action</strong>
            <span id="hud-event">none</span>
          </div>
        </div>
        <div class="hud-status" id="hud-status"></div>
      </aside>
      <footer>Generated at {{.GeneratedAt}}</footer>
    </section>

    <section class="detail-grid">
      <article class="detail-card">
        <h3>Recommendation Events</h3>
        {{range .EventCards}}
        <div class="event-card">
          <strong>{{.Headline}}</strong>
          <span>{{.Meta}}</span>
          <p>{{.Summary}}</p>
        </div>
        {{end}}
      </article>
      <article class="detail-card">
        <h3>Suppression and Telemetry</h3>
        <p>Suppression summary: {{.SuppressionSummary}}</p>
        <ul>
          {{range .SignalSummaries}}
          <li>{{.}}</li>
          {{end}}
        </ul>
      </article>
      <article class="detail-card">
        <h3>Confidence and Caveats</h3>
        <ul>
          {{range .ConfidenceNotes}}
          <li>{{.}}</li>
          {{else}}
          <li>none</li>
          {{end}}
        </ul>
        <h3 style="margin-top:18px;">Limitations</h3>
        <ul>
          {{range .Caveats}}
          <li>{{.}}</li>
          {{else}}
          <li>none</li>
          {{end}}
        </ul>
        {{if .UnsupportedReasons}}
        <h3 style="margin-top:18px;">Unsupported Reasons</h3>
        <ul>
          {{range .UnsupportedReasons}}
          <li>{{.}}</li>
          {{end}}
        </ul>
        {{end}}
      </article>
    </section>
  </main>
  <script id="timeline-data" type="application/json">{{.ChartDataJSON}}</script>
  <script>
    (function () {
      const dataNode = document.getElementById('timeline-data');
      const svg = document.getElementById('replay-timeline');
      const hitbox = document.getElementById('timeline-hitbox');
      if (!dataNode || !svg || !hitbox) return;

      const scene = JSON.parse(dataNode.textContent || 'null');
      if (!scene || !Array.isArray(scene.points) || scene.points.length === 0) return;

      const cursorLine = document.getElementById('cursor-line');
      const demandCursor = document.getElementById('cursor-demand');
      const baselineCursor = document.getElementById('cursor-baseline');
      const replayCursor = document.getElementById('cursor-replay');
      const hudTime = document.getElementById('hud-time');
      const hudDemand = document.getElementById('hud-demand');
      const hudBaseline = document.getElementById('hud-baseline');
      const hudReplay = document.getElementById('hud-replay');
      const hudEvent = document.getElementById('hud-event');
      const hudStatus = document.getElementById('hud-status');

      let currentIndex = Math.max(0, Math.min(scene.initialIndex || 0, scene.points.length - 1));

      const nearestIndex = (x) => {
        let bestIndex = 0;
        let bestDistance = Number.POSITIVE_INFINITY;
        for (let index = 0; index < scene.points.length; index += 1) {
          const distance = Math.abs(scene.points[index].x - x);
          if (distance < bestDistance) {
            bestDistance = distance;
            bestIndex = index;
          }
        }
        return bestIndex;
      };

      const activeEventForPoint = (point) => {
        if (!Array.isArray(scene.events)) return null;
        return scene.events.find((event) => point.x >= event.evalX && point.x <= event.readyX) || null;
      };

      const renderStatus = (point, event) => {
        hudStatus.innerHTML = '';
        const pills = [];
        if (event) pills.push('warmup active ' + event.fromReplicas + '→' + event.toReplicas);
        if (point.suppressed) pills.push(point.suppressionLabel || 'suppressed check');
        if (pills.length === 0) pills.push('steady slice');
        for (const label of pills) {
          const pill = document.createElement('span');
          pill.className = 'hud-pill';
          pill.textContent = label;
          hudStatus.appendChild(pill);
        }
      };

      const renderPoint = (index) => {
        currentIndex = Math.max(0, Math.min(index, scene.points.length - 1));
        const point = scene.points[currentIndex];
        const event = activeEventForPoint(point);

        cursorLine.setAttribute('x1', point.x);
        cursorLine.setAttribute('x2', point.x);
        cursorLine.setAttribute('y1', scene.plotTop);
        cursorLine.setAttribute('y2', scene.plotBottom);

        demandCursor.setAttribute('cx', point.x);
        demandCursor.setAttribute('cy', point.demandY);
        baselineCursor.setAttribute('cx', point.x);
        baselineCursor.setAttribute('cy', point.baselineY);
        replayCursor.setAttribute('cx', point.x);
        replayCursor.setAttribute('cy', point.replayY);

        hudTime.textContent = point.timeLabel;
        hudDemand.textContent = String(Math.round(point.demand));
        hudBaseline.textContent = String(point.baselineReplicas);
        hudReplay.textContent = String(point.replayReplicas);
        hudEvent.textContent = event ? (event.fromReplicas + '→' + event.toReplicas + ' until ' + event.readyTimeLabel) : 'none';
        renderStatus(point, event);
      };

      hitbox.addEventListener('mousemove', (event) => {
        const pt = svg.createSVGPoint();
        pt.x = event.clientX;
        pt.y = event.clientY;
        const local = pt.matrixTransform(svg.getScreenCTM().inverse());
        renderPoint(nearestIndex(local.x));
      });

      hitbox.addEventListener('mouseleave', () => {
        renderPoint(scene.initialIndex || 0);
      });

      renderPoint(currentIndex);
    }());
  </script>
</body>
</html>`))
