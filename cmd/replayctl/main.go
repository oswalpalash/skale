package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/oswalpalash/skale/internal/replay"
	"github.com/oswalpalash/skale/internal/report"
	"github.com/oswalpalash/skale/internal/version"
)

const usageText = `replayctl runs an offline replay from one explicit replay-input JSON file.

The input file packages one replay spec and one normalized historical snapshot.
v1 does not query Prometheus directly from the CLI.

Examples:
  replayctl -input replay.json
  replayctl -input replay.json -format json
  replayctl -input replay.json -format ui > replay.html
  replayctl -input replay.json -markdown-out replay.md -json-out replay.json.report -ui-out replay.html`

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("replayctl", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		showVersion bool
		inputPath   string
		format      string
		jsonOut     string
		markdownOut string
		uiOut       string
		uiFocus     time.Duration
	)

	flags.BoolVar(&showVersion, "version", false, "print the replayctl version")
	flags.StringVar(&inputPath, "input", "", "path to a replay-input JSON file")
	flags.StringVar(&format, "format", "summary", "stdout format: summary, json, markdown, or ui")
	flags.StringVar(&jsonOut, "json-out", "", "optional path to write the full replay result as JSON")
	flags.StringVar(&markdownOut, "markdown-out", "", "optional path to write a markdown replay report")
	flags.StringVar(&uiOut, "ui-out", "", "optional path to write a self-contained HTML replay UI")
	flags.DurationVar(&uiFocus, "ui-focus", 10*time.Minute, "focus window for UI output")
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

	spec, provider, err := loadReplayInput(inputPath)
	if err != nil {
		fmt.Fprintf(stderr, "load replay input: %v\n", err)
		return 1
	}

	engine := replay.Engine{
		Metrics: provider,
	}
	result, err := engine.Run(ctx, spec)
	if err != nil {
		fmt.Fprintf(stderr, "run replay: %v\n", err)
		return 1
	}

	stdoutWriter, err := writerForFormat(format, uiFocus)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 2
	}

	if err := writeReport(ctx, stdoutWriter, result, stdout); err != nil {
		fmt.Fprintf(stderr, "render %s output: %v\n", format, err)
		return 1
	}

	if jsonOut != "" {
		if err := writeReportFile(ctx, report.JSONWriter{}, result, jsonOut); err != nil {
			fmt.Fprintf(stderr, "write JSON report: %v\n", err)
			return 1
		}
	}
	if markdownOut != "" {
		if err := writeReportFile(ctx, report.MarkdownWriter{}, result, markdownOut); err != nil {
			fmt.Fprintf(stderr, "write markdown report: %v\n", err)
			return 1
		}
	}
	if uiOut != "" {
		if err := writeReportFile(ctx, report.HTMLWriter{FocusWindow: uiFocus}, result, uiOut); err != nil {
			fmt.Fprintf(stderr, "write UI report: %v\n", err)
			return 1
		}
	}

	return 0
}

func writerForFormat(format string, uiFocus time.Duration) (report.Writer, error) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "", "summary", "text":
		return report.SummaryWriter{}, nil
	case "json":
		return report.JSONWriter{}, nil
	case "markdown", "md":
		return report.MarkdownWriter{}, nil
	case "ui", "html":
		return report.HTMLWriter{FocusWindow: uiFocus}, nil
	default:
		return nil, fmt.Errorf("unsupported format %q; want summary, json, markdown, or ui", format)
	}
}

func writeReport(ctx context.Context, writer report.Writer, result replay.Result, output io.Writer) error {
	bytes, err := writer.Write(ctx, result)
	if err != nil {
		return err
	}
	if len(bytes) == 0 {
		return nil
	}
	if _, err := output.Write(bytes); err != nil {
		return err
	}
	if bytes[len(bytes)-1] != '\n' {
		if _, err := io.WriteString(output, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func writeReportFile(ctx context.Context, writer report.Writer, result replay.Result, path string) error {
	bytes, err := writer.Write(ctx, result)
	if err != nil {
		return err
	}
	if len(bytes) > 0 && bytes[len(bytes)-1] != '\n' {
		bytes = append(bytes, '\n')
	}
	return os.WriteFile(path, bytes, 0o644)
}
