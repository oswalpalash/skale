package main

import (
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/livefixture"
	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replayinput"
	"github.com/oswalpalash/skale/internal/version"
)

const usageText = `livefixture converts captured live-demo samples into a replay-input JSON file.

Expected CSV header:
  timestamp,demand_qps,ready_replicas,cpu_ratio,memory_ratio

Examples:
  livefixture -input live-samples.csv
  livefixture -input live-samples.csv -out live-replay-input.json`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("livefixture", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		showVersion          bool
		inputPath            string
		outPath              string
		namespace            string
		name                 string
		workload             string
		step                 time.Duration
		replayDuration       time.Duration
		lookback             time.Duration
		forecastHorizon      time.Duration
		forecastSeasonality  time.Duration
		warmup               time.Duration
		targetUtilization    float64
		confidenceThreshold  float64
		minReplicas          int
		maxReplicas          int
		maxStepUp            int
		maxStepDown          int
		cooldownWindow       time.Duration
		minimumCapacitySteps int
	)

	flags.BoolVar(&showVersion, "version", false, "print the livefixture version")
	flags.StringVar(&inputPath, "input", "", "path to the captured live-demo CSV file")
	flags.StringVar(&outPath, "out", "", "optional path to write the replay-input JSON")
	flags.StringVar(&namespace, "namespace", "skale-live-demo", "target workload namespace")
	flags.StringVar(&name, "name", "checkout-api", "target workload name")
	flags.StringVar(&workload, "workload", "", "optional workload identity override (defaults to namespace/name)")
	flags.DurationVar(&step, "step", 30*time.Second, "capture step and replay step duration")
	flags.DurationVar(&replayDuration, "replay-duration", 12*time.Minute, "duration of the replay window ending at the final captured sample")
	flags.DurationVar(&lookback, "lookback", 12*time.Minute, "history duration made available before the replay window")
	flags.DurationVar(&forecastHorizon, "forecast-horizon", 2*time.Minute, "forecast horizon to encode in the replay policy")
	flags.DurationVar(&forecastSeasonality, "forecast-seasonality", 6*time.Minute, "forecast seasonality to encode in the replay policy")
	flags.DurationVar(&warmup, "warmup", 90*time.Second, "fixed warmup delay encoded into the replay policy")
	flags.Float64Var(&targetUtilization, "target-utilization", 0.8, "target utilization used for replica sizing")
	flags.Float64Var(&confidenceThreshold, "confidence-threshold", 0.65, "minimum forecast confidence used for surfacing strong recommendations")
	flags.IntVar(&minReplicas, "min-replicas", 2, "minimum replicas for the replay policy")
	flags.IntVar(&maxReplicas, "max-replicas", 6, "maximum replicas for the replay policy")
	flags.IntVar(&maxStepUp, "max-step-up", 2, "maximum scale-up step for the replay policy")
	flags.IntVar(&maxStepDown, "max-step-down", 1, "maximum scale-down step for the replay policy")
	flags.DurationVar(&cooldownWindow, "cooldown-window", 2*time.Minute, "cooldown window for replay recommendations")
	flags.IntVar(&minimumCapacitySteps, "minimum-capacity-steps", 4, "minimum samples required for capacity estimation")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), usageText)
		fmt.Fprintln(flags.Output())
		flags.PrintDefaults()
	}

	if err := flags.Parse(args); err != nil {
		return 2
	}
	if showVersion {
		fmt.Fprintln(stdout, version.String())
		return 0
	}
	if inputPath == "" {
		flags.Usage()
		fmt.Fprintln(stderr, "\n-input is required")
		return 2
	}

	samples, err := loadSamples(inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "load live samples: %v\n", err)
		return 1
	}
	if workload == "" {
		workload = namespace + "/" + name
	}
	up := int32(maxStepUp)
	down := int32(maxStepDown)
	document, err := livefixture.BuildDocument(livefixture.Config{
		Target:               metrics.Target{Namespace: namespace, Name: name},
		Workload:             workload,
		Step:                 step,
		ReplayDuration:       replayDuration,
		Lookback:             lookback,
		ForecastHorizon:      forecastHorizon,
		ForecastSeasonality:  forecastSeasonality,
		Warmup:               warmup,
		TargetUtilization:    targetUtilization,
		ConfidenceThreshold:  confidenceThreshold,
		MinReplicas:          int32(minReplicas),
		MaxReplicas:          int32(maxReplicas),
		MaxStepUp:            &up,
		MaxStepDown:          &down,
		CooldownWindow:       cooldownWindow,
		CapacityLookback:     lookback,
		MinimumCapacitySteps: minimumCapacitySteps,
	}, samples)
	if err != nil {
		fmt.Fprintf(stderr, "build live replay input: %v\n", err)
		return 1
	}

	bytes, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "marshal live replay input: %v\n", err)
		return 1
	}
	bytes = append(bytes, '\n')

	if outPath != "" {
		if err := os.WriteFile(outPath, bytes, 0o644); err != nil {
			fmt.Fprintf(stderr, "write live replay input %q: %v\n", outPath, err)
			return 1
		}
	}
	if _, err := stdout.Write(bytes); err != nil {
		fmt.Fprintf(stderr, "write live replay input: %v\n", err)
		return 1
	}

	return 0
}

func loadSamples(path string) ([]livefixture.Sample, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %q: %w", path, err)
	}
	defer file.Close()

	reader := csv.NewReader(file)
	reader.TrimLeadingSpace = true
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv %q: %w", path, err)
	}
	if len(rows) < 2 {
		return nil, fmt.Errorf("csv %q must contain a header and at least one sample row", path)
	}

	header := strings.Join(rows[0], ",")
	if header != "timestamp,demand_qps,ready_replicas,cpu_ratio,memory_ratio" {
		return nil, fmt.Errorf(
			"csv %q header = %q, want %q",
			path,
			header,
			"timestamp,demand_qps,ready_replicas,cpu_ratio,memory_ratio",
		)
	}

	samples := make([]livefixture.Sample, 0, len(rows)-1)
	for index, row := range rows[1:] {
		if len(row) != 5 {
			return nil, fmt.Errorf("csv row %d has %d columns, want 5", index+2, len(row))
		}
		timestamp, err := time.Parse(time.RFC3339, strings.TrimSpace(row[0]))
		if err != nil {
			return nil, fmt.Errorf("parse timestamp row %d: %w", index+2, err)
		}
		demandQPS, err := strconv.ParseFloat(strings.TrimSpace(row[1]), 64)
		if err != nil {
			return nil, fmt.Errorf("parse demand row %d: %w", index+2, err)
		}
		readyReplicas, err := strconv.ParseInt(strings.TrimSpace(row[2]), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("parse ready replicas row %d: %w", index+2, err)
		}
		cpuRatio, err := strconv.ParseFloat(strings.TrimSpace(row[3]), 64)
		if err != nil {
			return nil, fmt.Errorf("parse cpu ratio row %d: %w", index+2, err)
		}
		memoryRatio, err := strconv.ParseFloat(strings.TrimSpace(row[4]), 64)
		if err != nil {
			return nil, fmt.Errorf("parse memory ratio row %d: %w", index+2, err)
		}
		samples = append(samples, livefixture.Sample{
			Timestamp:     timestamp.UTC(),
			DemandQPS:     demandQPS,
			ReadyReplicas: int32(readyReplicas),
			CPURatio:      cpuRatio,
			MemoryRatio:   memoryRatio,
		})
	}

	return samples, nil
}

type replayInputDocument = replayinput.Document
