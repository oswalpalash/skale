package metrics

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

var ErrInvalidReadinessInput = errors.New("invalid telemetry readiness input")

type ReadinessLevel string

const (
	ReadinessLevelSupported   ReadinessLevel = "supported"
	ReadinessLevelDegraded    ReadinessLevel = "degraded"
	ReadinessLevelUnsupported ReadinessLevel = "unsupported"
)

type SignalLevel string

const (
	SignalLevelSupported   SignalLevel = "supported"
	SignalLevelDegraded    SignalLevel = "degraded"
	SignalLevelUnsupported SignalLevel = "unsupported"
	SignalLevelMissing     SignalLevel = "missing"
)

// ReadinessOptions controls the operator-facing thresholds used for telemetry validation.
type ReadinessOptions struct {
	MinimumLookback                   time.Duration
	ExpectedResolution                time.Duration
	DegradedMissingFraction           float64
	UnsupportedMissingFraction        float64
	DegradedResolutionMultiplier      float64
	UnsupportedResolutionMultiplier   float64
	DegradedGapMultiplier             float64
	UnsupportedGapMultiplier          float64
	MinimumWarmupSamplesToEstimate    int
	DemandStepChangeThreshold         float64
	DegradedDemandUnstableFraction    float64
	UnsupportedDemandUnstableFraction float64
}

// DefaultReadinessOptions returns conservative defaults for supported v1 workloads.
func DefaultReadinessOptions() ReadinessOptions {
	return ReadinessOptions{
		MinimumLookback:                   30 * time.Minute,
		ExpectedResolution:                30 * time.Second,
		DegradedMissingFraction:           0.10,
		UnsupportedMissingFraction:        0.25,
		DegradedResolutionMultiplier:      2,
		UnsupportedResolutionMultiplier:   4,
		DegradedGapMultiplier:             4,
		UnsupportedGapMultiplier:          8,
		MinimumWarmupSamplesToEstimate:    5,
		DemandStepChangeThreshold:         1.5,
		DegradedDemandUnstableFraction:    0.35,
		UnsupportedDemandUnstableFraction: 0.60,
	}
}

func (o ReadinessOptions) withDefaults() ReadinessOptions {
	defaults := DefaultReadinessOptions()
	if o.MinimumLookback == 0 {
		o.MinimumLookback = defaults.MinimumLookback
	}
	if o.ExpectedResolution == 0 {
		o.ExpectedResolution = defaults.ExpectedResolution
	}
	if o.DegradedMissingFraction == 0 {
		o.DegradedMissingFraction = defaults.DegradedMissingFraction
	}
	if o.UnsupportedMissingFraction == 0 {
		o.UnsupportedMissingFraction = defaults.UnsupportedMissingFraction
	}
	if o.DegradedResolutionMultiplier == 0 {
		o.DegradedResolutionMultiplier = defaults.DegradedResolutionMultiplier
	}
	if o.UnsupportedResolutionMultiplier == 0 {
		o.UnsupportedResolutionMultiplier = defaults.UnsupportedResolutionMultiplier
	}
	if o.DegradedGapMultiplier == 0 {
		o.DegradedGapMultiplier = defaults.DegradedGapMultiplier
	}
	if o.UnsupportedGapMultiplier == 0 {
		o.UnsupportedGapMultiplier = defaults.UnsupportedGapMultiplier
	}
	if o.MinimumWarmupSamplesToEstimate == 0 {
		o.MinimumWarmupSamplesToEstimate = defaults.MinimumWarmupSamplesToEstimate
	}
	if o.DemandStepChangeThreshold == 0 {
		o.DemandStepChangeThreshold = defaults.DemandStepChangeThreshold
	}
	if o.DegradedDemandUnstableFraction == 0 {
		o.DegradedDemandUnstableFraction = defaults.DegradedDemandUnstableFraction
	}
	if o.UnsupportedDemandUnstableFraction == 0 {
		o.UnsupportedDemandUnstableFraction = defaults.UnsupportedDemandUnstableFraction
	}
	return o
}

// ReadinessInput provides the normalized telemetry required for readiness evaluation.
type ReadinessInput struct {
	EvaluatedAt time.Time
	Snapshot    Snapshot
	KnownWarmup *time.Duration
	Options     ReadinessOptions
}

// SignalReport describes health for one normalized signal.
type SignalReport struct {
	Name                     SignalName  `json:"name,omitempty"`
	Required                 bool        `json:"required,omitempty"`
	Level                    SignalLevel `json:"level,omitempty"`
	Message                  string      `json:"message,omitempty"`
	Issues                   []string    `json:"issues,omitempty"`
	SampleCount              int         `json:"sampleCount,omitempty"`
	RequestedLookbackSeconds int64       `json:"requestedLookbackSeconds,omitempty"`
	ObservedCoverageSeconds  int64       `json:"observedCoverageSeconds,omitempty"`
	MissingFraction          float64     `json:"missingFraction,omitempty"`
	MedianResolutionSeconds  int64       `json:"medianResolutionSeconds,omitempty"`
	MaxGapSeconds            int64       `json:"maxGapSeconds,omitempty"`
	LabelSignatureCount      int         `json:"labelSignatureCount,omitempty"`
	WarmupKnown              bool        `json:"warmupKnown,omitempty"`
	WarmupEstimable          bool        `json:"warmupEstimable,omitempty"`
	EstimatedWarmupSeconds   *int64      `json:"estimatedWarmupSeconds,omitempty"`
	DemandUnstableFraction   *float64    `json:"demandUnstableFraction,omitempty"`
}

// ReadinessReport is the structured telemetry readiness result shared by controller, CLI, and replay.
type ReadinessReport struct {
	Level           ReadinessLevel `json:"level,omitempty"`
	CheckedAt       time.Time      `json:"checkedAt,omitempty"`
	Summary         string         `json:"summary,omitempty"`
	Reasons         []string       `json:"reasons,omitempty"`
	BlockingReasons []string       `json:"blockingReasons,omitempty"`
	Signals         []SignalReport `json:"signals,omitempty"`
}

// Evaluator checks whether workload telemetry is sufficient for replay and recommendation evaluation.
type Evaluator interface {
	Evaluate(input ReadinessInput) (ReadinessReport, error)
}

// DefaultEvaluator implements the default v1 telemetry readiness checks.
type DefaultEvaluator struct{}

type seriesStats struct {
	samples          []Sample
	coverage         time.Duration
	lookback         time.Duration
	missingFraction  float64
	medianResolution time.Duration
	maxGap           time.Duration
	labelCount       int
}

type signalEvaluation struct {
	report   SignalReport
	degraded []string
	blocking []string
}

// Evaluate checks required signal availability and telemetry quality for the supported workload wedge.
func (e DefaultEvaluator) Evaluate(input ReadinessInput) (ReadinessReport, error) {
	options := input.Options.withDefaults()
	if err := validateReadinessInput(input, options); err != nil {
		return ReadinessReport{}, err
	}

	report := ReadinessReport{
		CheckedAt: evaluatedAt(input),
	}

	evaluations := []signalEvaluation{
		e.evaluateStandardSignal(SignalDemand, input.Snapshot.Signal(SignalDemand), true, input.Snapshot.Window, options),
		e.evaluateStandardSignal(SignalReplicas, input.Snapshot.Signal(SignalReplicas), true, input.Snapshot.Window, options),
		e.evaluateStandardSignal(SignalCPU, input.Snapshot.Signal(SignalCPU), true, input.Snapshot.Window, options),
		e.evaluateStandardSignal(SignalMemory, input.Snapshot.Signal(SignalMemory), true, input.Snapshot.Window, options),
		e.evaluateWarmup(input, options),
	}

	if signal := input.Snapshot.Signal(SignalNodeHeadroom); signal != nil {
		evaluations = append(evaluations, e.evaluateStandardSignal(SignalNodeHeadroom, signal, false, input.Snapshot.Window, options))
	}

	for _, evaluation := range evaluations {
		report.Signals = append(report.Signals, evaluation.report)
		report.Reasons = append(report.Reasons, evaluation.degraded...)
		report.BlockingReasons = append(report.BlockingReasons, evaluation.blocking...)
	}

	report.BlockingReasons = dedupeStrings(report.BlockingReasons)
	report.Reasons = dedupeStrings(append(report.Reasons, report.BlockingReasons...))
	switch {
	case len(report.BlockingReasons) > 0:
		report.Level = ReadinessLevelUnsupported
	case len(report.Reasons) > 0:
		report.Level = ReadinessLevelDegraded
	default:
		report.Level = ReadinessLevelSupported
	}
	report.Summary = buildReadinessSummary(report)

	return report, nil
}

func (e DefaultEvaluator) evaluateStandardSignal(
	name SignalName,
	series *SignalSeries,
	required bool,
	window Window,
	options ReadinessOptions,
) signalEvaluation {
	result := signalEvaluation{
		report: SignalReport{
			Name:     name,
			Required: required,
		},
	}

	if series == nil || len(series.Samples) == 0 {
		result.report.Level = SignalLevelMissing
		result.report.Message = signalMissingMessage(name, required)
		if required {
			result.blocking = append(result.blocking, result.report.Message)
		}
		return result
	}

	stats := analyzeSeries(series, window, options.ExpectedResolution)
	result.report.SampleCount = len(stats.samples)
	result.report.RequestedLookbackSeconds = int64(stats.lookback / time.Second)
	result.report.ObservedCoverageSeconds = int64(stats.coverage / time.Second)
	result.report.MissingFraction = stats.missingFraction
	result.report.MedianResolutionSeconds = int64(stats.medianResolution / time.Second)
	result.report.MaxGapSeconds = int64(stats.maxGap / time.Second)
	result.report.LabelSignatureCount = stats.labelCount

	severity := SignalLevelSupported
	var issues []string

	if stats.lookback < options.MinimumLookback {
		issues = append(issues, fmt.Sprintf("%s lookback covers %s; need at least %s", name, formatDuration(stats.lookback), formatDuration(options.MinimumLookback)))
		if stats.lookback < options.MinimumLookback/2 {
			severity = worsenSignalLevel(severity, SignalLevelUnsupported)
		} else {
			severity = worsenSignalLevel(severity, SignalLevelDegraded)
		}
	}

	if stats.missingFraction > options.UnsupportedMissingFraction {
		issues = append(issues, fmt.Sprintf("%s is missing %.0f%% of expected samples", name, stats.missingFraction*100))
		severity = worsenSignalLevel(severity, SignalLevelUnsupported)
	} else if stats.missingFraction > options.DegradedMissingFraction {
		issues = append(issues, fmt.Sprintf("%s is missing %.0f%% of expected samples", name, stats.missingFraction*100))
		severity = worsenSignalLevel(severity, SignalLevelDegraded)
	}

	if stats.medianResolution > time.Duration(float64(options.ExpectedResolution)*options.UnsupportedResolutionMultiplier) {
		issues = append(issues, fmt.Sprintf("%s median scrape resolution is %s; target is %s", name, formatDuration(stats.medianResolution), formatDuration(options.ExpectedResolution)))
		severity = worsenSignalLevel(severity, SignalLevelUnsupported)
	} else if stats.medianResolution > time.Duration(float64(options.ExpectedResolution)*options.DegradedResolutionMultiplier) {
		issues = append(issues, fmt.Sprintf("%s median scrape resolution is %s; target is %s", name, formatDuration(stats.medianResolution), formatDuration(options.ExpectedResolution)))
		severity = worsenSignalLevel(severity, SignalLevelDegraded)
	}

	if stats.maxGap > time.Duration(float64(options.ExpectedResolution)*options.UnsupportedGapMultiplier) {
		issues = append(issues, fmt.Sprintf("%s has a maximum telemetry gap of %s", name, formatDuration(stats.maxGap)))
		severity = worsenSignalLevel(severity, SignalLevelUnsupported)
	} else if stats.maxGap > time.Duration(float64(options.ExpectedResolution)*options.DegradedGapMultiplier) {
		issues = append(issues, fmt.Sprintf("%s has a maximum telemetry gap of %s", name, formatDuration(stats.maxGap)))
		severity = worsenSignalLevel(severity, SignalLevelDegraded)
	}

	if stats.labelCount > 1 {
		issues = append(issues, fmt.Sprintf("%s observed %d label signatures across the lookback window", name, stats.labelCount))
		severity = worsenSignalLevel(severity, SignalLevelUnsupported)
	}

	if name == SignalDemand {
		issues, severity, result.report.DemandUnstableFraction = evaluateDemandStability(stats.samples, issues, severity, options)
	}

	result.report.Level = severity
	if len(issues) == 0 {
		result.report.Message = fmt.Sprintf("%s signal is available with %d samples over %s", name, result.report.SampleCount, formatDuration(stats.lookback))
		return result
	}

	result.report.Issues = issues
	result.report.Message = strings.Join(issues, "; ")
	switch severity {
	case SignalLevelUnsupported, SignalLevelMissing:
		if required {
			result.blocking = append(result.blocking, result.report.Message)
		}
	case SignalLevelDegraded:
		if required {
			result.degraded = append(result.degraded, result.report.Message)
		}
	}

	return result
}

func (e DefaultEvaluator) evaluateWarmup(input ReadinessInput, options ReadinessOptions) signalEvaluation {
	if input.KnownWarmup != nil {
		if *input.KnownWarmup <= 0 {
			message := "warmup lag is configured but not positive"
			return signalEvaluation{
				report: SignalReport{
					Name:     SignalWarmup,
					Required: true,
					Level:    SignalLevelUnsupported,
					Message:  message,
					Issues:   []string{message},
				},
				blocking: []string{message},
			}
		}

		seconds := int64((*input.KnownWarmup) / time.Second)
		return signalEvaluation{
			report: SignalReport{
				Name:                   SignalWarmup,
				Required:               true,
				Level:                  SignalLevelSupported,
				Message:                fmt.Sprintf("warmup lag is explicitly known at %s", formatDuration(*input.KnownWarmup)),
				WarmupKnown:            true,
				EstimatedWarmupSeconds: &seconds,
			},
		}
	}

	series := input.Snapshot.Signal(SignalWarmup)
	if series == nil || len(series.Samples) == 0 {
		message := "warmup lag is not known and no warmup observations are available to estimate it"
		return signalEvaluation{
			report: SignalReport{
				Name:     SignalWarmup,
				Required: true,
				Level:    SignalLevelMissing,
				Message:  message,
				Issues:   []string{message},
			},
			blocking: []string{message},
		}
	}

	samples := normalizeSamples(series.Samples)
	report := SignalReport{
		Name:                    SignalWarmup,
		Required:                true,
		SampleCount:             len(samples),
		LabelSignatureCount:     uniqueNonEmptyCount(series.ObservedLabelSignatures),
		ObservedCoverageSeconds: int64(coverageDuration(samples) / time.Second),
	}

	if report.LabelSignatureCount > 1 {
		message := fmt.Sprintf("warmup observations use %d label signatures across the lookback window", report.LabelSignatureCount)
		report.Level = SignalLevelUnsupported
		report.Message = message
		report.Issues = []string{message}
		return signalEvaluation{
			report:   report,
			blocking: []string{message},
		}
	}

	values := positiveSampleValues(series)
	if len(values) < options.MinimumWarmupSamplesToEstimate {
		message := fmt.Sprintf("warmup lag cannot be estimated reliably; only %d positive warmup samples are available", len(values))
		report.Level = SignalLevelUnsupported
		report.Message = message
		report.Issues = []string{message}
		return signalEvaluation{
			report:   report,
			blocking: []string{message},
		}
	}

	estimate := medianFloat(values)
	if estimate <= 0 {
		message := "warmup lag estimate is not positive"
		report.Level = SignalLevelUnsupported
		report.Message = message
		report.Issues = []string{message}
		return signalEvaluation{
			report:   report,
			blocking: []string{message},
		}
	}

	estimatedSeconds := int64(math.Round(estimate))
	message := fmt.Sprintf("warmup lag can be estimated from %d samples; median estimated warmup is %s", len(values), formatDuration(time.Duration(estimatedSeconds)*time.Second))
	report.WarmupEstimable = true
	report.EstimatedWarmupSeconds = &estimatedSeconds
	report.Level = SignalLevelDegraded
	report.Issues = []string{message}
	report.Message = message
	return signalEvaluation{
		report:   report,
		degraded: []string{message},
	}
}

func validateReadinessInput(input ReadinessInput, options ReadinessOptions) error {
	if options.MinimumLookback <= 0 {
		return fmt.Errorf("%w: minimum lookback must be positive", ErrInvalidReadinessInput)
	}
	if options.ExpectedResolution <= 0 {
		return fmt.Errorf("%w: expected resolution must be positive", ErrInvalidReadinessInput)
	}
	if options.DegradedMissingFraction < 0 || options.DegradedMissingFraction > 1 {
		return fmt.Errorf("%w: degraded missing fraction must be between 0 and 1", ErrInvalidReadinessInput)
	}
	if options.UnsupportedMissingFraction < 0 || options.UnsupportedMissingFraction > 1 {
		return fmt.Errorf("%w: unsupported missing fraction must be between 0 and 1", ErrInvalidReadinessInput)
	}
	if options.DegradedMissingFraction > options.UnsupportedMissingFraction {
		return fmt.Errorf("%w: degraded missing fraction must not exceed unsupported missing fraction", ErrInvalidReadinessInput)
	}
	if options.DegradedResolutionMultiplier <= 0 || options.UnsupportedResolutionMultiplier <= 0 {
		return fmt.Errorf("%w: resolution multipliers must be positive", ErrInvalidReadinessInput)
	}
	if options.DegradedGapMultiplier <= 0 || options.UnsupportedGapMultiplier <= 0 {
		return fmt.Errorf("%w: gap multipliers must be positive", ErrInvalidReadinessInput)
	}
	if options.DegradedResolutionMultiplier > options.UnsupportedResolutionMultiplier {
		return fmt.Errorf("%w: degraded resolution multiplier must not exceed unsupported resolution multiplier", ErrInvalidReadinessInput)
	}
	if options.DegradedGapMultiplier > options.UnsupportedGapMultiplier {
		return fmt.Errorf("%w: degraded gap multiplier must not exceed unsupported gap multiplier", ErrInvalidReadinessInput)
	}
	if options.MinimumWarmupSamplesToEstimate < 1 {
		return fmt.Errorf("%w: minimum warmup samples must be at least 1", ErrInvalidReadinessInput)
	}
	if options.DemandStepChangeThreshold <= 0 {
		return fmt.Errorf("%w: demand step change threshold must be positive", ErrInvalidReadinessInput)
	}
	if options.DegradedDemandUnstableFraction < 0 || options.DegradedDemandUnstableFraction > 1 {
		return fmt.Errorf("%w: degraded demand unstable fraction must be between 0 and 1", ErrInvalidReadinessInput)
	}
	if options.UnsupportedDemandUnstableFraction < 0 || options.UnsupportedDemandUnstableFraction > 1 {
		return fmt.Errorf("%w: unsupported demand unstable fraction must be between 0 and 1", ErrInvalidReadinessInput)
	}
	if options.DegradedDemandUnstableFraction > options.UnsupportedDemandUnstableFraction {
		return fmt.Errorf("%w: degraded demand unstable fraction must not exceed unsupported demand unstable fraction", ErrInvalidReadinessInput)
	}
	if input.KnownWarmup != nil && *input.KnownWarmup < 0 {
		return fmt.Errorf("%w: known warmup must be non-negative", ErrInvalidReadinessInput)
	}
	return nil
}

func evaluateDemandStability(samples []Sample, issues []string, severity SignalLevel, options ReadinessOptions) ([]string, SignalLevel, *float64) {
	if len(samples) < 2 {
		issues = append(issues, "demand signal has fewer than two samples in the lookback window")
		return issues, worsenSignalLevel(severity, SignalLevelUnsupported), nil
	}

	meanDemand := meanFloat(sampleValues(samples))
	if meanDemand <= 0 {
		issues = append(issues, "demand signal has no sustained activity in the lookback window")
		severity = worsenSignalLevel(severity, SignalLevelUnsupported)
		return issues, severity, nil
	}

	transitions := 0
	unstable := 0
	for i := 1; i < len(samples); i++ {
		prev := samples[i-1].Value
		curr := samples[i].Value
		denominator := math.Max((math.Abs(prev)+math.Abs(curr))/2, 1)
		ratio := math.Abs(curr-prev) / denominator
		transitions++
		if ratio > options.DemandStepChangeThreshold {
			unstable++
		}
	}
	if transitions == 0 {
		return issues, severity, nil
	}

	unstableFraction := float64(unstable) / float64(transitions)
	switch {
	case unstableFraction > options.UnsupportedDemandUnstableFraction:
		issues = append(issues, fmt.Sprintf("demand signal changes too abruptly between scrapes (%.0f%% of steps exceed %.2fx relative change)", unstableFraction*100, options.DemandStepChangeThreshold))
		severity = worsenSignalLevel(severity, SignalLevelUnsupported)
	case unstableFraction > options.DegradedDemandUnstableFraction:
		issues = append(issues, fmt.Sprintf("demand signal is noisy between scrapes (%.0f%% of steps exceed %.2fx relative change)", unstableFraction*100, options.DemandStepChangeThreshold))
		severity = worsenSignalLevel(severity, SignalLevelDegraded)
	}

	return issues, severity, &unstableFraction
}

func analyzeSeries(series *SignalSeries, window Window, expectedResolution time.Duration) seriesStats {
	samples := normalizeSamples(series.Samples)
	coverage := time.Duration(0)
	if len(samples) > 1 {
		coverage = samples[len(samples)-1].Timestamp.Sub(samples[0].Timestamp)
	}

	lookback := coverage
	if !window.Start.IsZero() && !window.End.IsZero() && window.End.After(window.Start) {
		lookback = window.End.Sub(window.Start)
	}

	medianResolution := medianSampleResolution(samples)
	maxGap := maxSampleGap(samples, window)
	missingFraction := missingFraction(samples, lookback, expectedResolution)

	return seriesStats{
		samples:          samples,
		coverage:         coverage,
		lookback:         lookback,
		missingFraction:  missingFraction,
		medianResolution: medianResolution,
		maxGap:           maxGap,
		labelCount:       uniqueNonEmptyCount(series.ObservedLabelSignatures),
	}
}

func evaluatedAt(input ReadinessInput) time.Time {
	if !input.EvaluatedAt.IsZero() {
		return input.EvaluatedAt
	}
	if !input.Snapshot.Window.End.IsZero() {
		return input.Snapshot.Window.End
	}
	return time.Time{}
}

func buildReadinessSummary(report ReadinessReport) string {
	switch report.Level {
	case ReadinessLevelSupported:
		return "Telemetry is sufficient for replay and recommendation evaluation."
	case ReadinessLevelDegraded:
		return fmt.Sprintf("Telemetry is usable but degraded: %s.", strings.Join(report.Reasons, "; "))
	default:
		return fmt.Sprintf("Telemetry is not sufficient for replay or recommendation evaluation: %s.", strings.Join(report.BlockingReasons, "; "))
	}
}

func signalMissingMessage(name SignalName, required bool) string {
	if required {
		return fmt.Sprintf("required signal %s is missing", name)
	}
	return fmt.Sprintf("optional signal %s is missing", name)
}

func worsenSignalLevel(current, next SignalLevel) SignalLevel {
	order := map[SignalLevel]int{
		SignalLevelSupported:   0,
		SignalLevelDegraded:    1,
		SignalLevelUnsupported: 2,
		SignalLevelMissing:     3,
	}
	if order[next] > order[current] {
		return next
	}
	return current
}

func normalizeSamples(samples []Sample) []Sample {
	if len(samples) == 0 {
		return nil
	}

	normalized := append([]Sample(nil), samples...)
	sort.Slice(normalized, func(i, j int) bool {
		return normalized[i].Timestamp.Before(normalized[j].Timestamp)
	})

	deduped := make([]Sample, 0, len(normalized))
	for _, sample := range normalized {
		if len(deduped) == 0 || !sample.Timestamp.Equal(deduped[len(deduped)-1].Timestamp) {
			deduped = append(deduped, sample)
			continue
		}
		deduped[len(deduped)-1] = sample
	}
	return deduped
}

func medianSampleResolution(samples []Sample) time.Duration {
	if len(samples) < 2 {
		return 0
	}

	intervals := make([]int64, 0, len(samples)-1)
	for i := 1; i < len(samples); i++ {
		intervals = append(intervals, int64(samples[i].Timestamp.Sub(samples[i-1].Timestamp)))
	}
	sort.Slice(intervals, func(i, j int) bool {
		return intervals[i] < intervals[j]
	})
	mid := len(intervals) / 2
	if len(intervals)%2 == 1 {
		return time.Duration(intervals[mid])
	}
	return time.Duration((intervals[mid-1] + intervals[mid]) / 2)
}

func maxSampleGap(samples []Sample, window Window) time.Duration {
	if len(samples) == 0 {
		if !window.Start.IsZero() && !window.End.IsZero() && window.End.After(window.Start) {
			return window.End.Sub(window.Start)
		}
		return 0
	}

	maxGap := time.Duration(0)
	if !window.Start.IsZero() && samples[0].Timestamp.After(window.Start) {
		maxGap = samples[0].Timestamp.Sub(window.Start)
	}
	for i := 1; i < len(samples); i++ {
		gap := samples[i].Timestamp.Sub(samples[i-1].Timestamp)
		if gap > maxGap {
			maxGap = gap
		}
	}
	if !window.End.IsZero() && window.End.After(samples[len(samples)-1].Timestamp) {
		tailGap := window.End.Sub(samples[len(samples)-1].Timestamp)
		if tailGap > maxGap {
			maxGap = tailGap
		}
	}
	return maxGap
}

func missingFraction(samples []Sample, lookback, expectedResolution time.Duration) float64 {
	if lookback <= 0 || expectedResolution <= 0 {
		return 0
	}

	expected := int(math.Floor(float64(lookback)/float64(expectedResolution))) + 1
	if expected <= 0 {
		return 0
	}
	missing := expected - len(samples)
	if missing <= 0 {
		return 0
	}
	return float64(missing) / float64(expected)
}

func uniqueNonEmptyCount(values []string) int {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	return len(seen)
}

func positiveSampleValues(series *SignalSeries) []float64 {
	if series == nil {
		return nil
	}
	values := make([]float64, 0, len(series.Samples))
	for _, sample := range normalizeSamples(series.Samples) {
		if sample.Value > 0 {
			values = append(values, sample.Value)
		}
	}
	return values
}

func coverageDuration(samples []Sample) time.Duration {
	if len(samples) < 2 {
		return 0
	}
	return samples[len(samples)-1].Timestamp.Sub(samples[0].Timestamp)
}

func medianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

func meanFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return sum / float64(len(values))
}

func sampleValues(samples []Sample) []float64 {
	values := make([]float64, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample.Value)
	}
	return values
}

func dedupeStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	deduped := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		deduped = append(deduped, value)
	}
	return deduped
}

func formatDuration(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	return duration.String()
}
