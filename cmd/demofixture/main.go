package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/oswalpalash/skale/internal/demofixture"
	"github.com/oswalpalash/skale/internal/replayinput"
	"github.com/oswalpalash/skale/internal/version"
)

const usageText = `demofixture renders one checked-in synthetic replay-input document for local demos.

Examples:
  demofixture
  demofixture -scenario design-partner-24h
  demofixture -out ./demo/output/checkout-api-replay-input.json`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("demofixture", flag.ContinueOnError)
	flags.SetOutput(stderr)

	var (
		showVersion bool
		scenario    string
		outPath     string
	)

	flags.BoolVar(&showVersion, "version", false, "print the demofixture version")
	flags.StringVar(&scenario, "scenario", "design-partner-24h", "demo fixture scenario to render")
	flags.StringVar(&outPath, "out", "", "optional path to write the generated replay-input JSON")
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

	document, err := documentForScenario(scenario)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}

	bytes, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "marshal fixture: %v\n", err)
		return 1
	}
	bytes = append(bytes, '\n')

	if outPath != "" {
		if err := os.WriteFile(outPath, bytes, 0o644); err != nil {
			fmt.Fprintf(stderr, "write fixture %q: %v\n", outPath, err)
			return 1
		}
	}
	if _, err := stdout.Write(bytes); err != nil {
		fmt.Fprintf(stderr, "write fixture output: %v\n", err)
		return 1
	}

	return 0
}

func documentForScenario(name string) (replayinput.Document, error) {
	switch name {
	case "", "design-partner", "design-partner-24h":
		return demofixture.DesignPartner24HourDocument(), nil
	default:
		return replayinput.Document{}, fmt.Errorf("unsupported demo fixture scenario %q; want design-partner-24h", name)
	}
}
