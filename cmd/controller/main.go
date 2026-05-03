package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/oswalpalash/skale/internal/controller"
	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/replay"
	"github.com/oswalpalash/skale/internal/replayinput"
	"github.com/oswalpalash/skale/internal/version"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {
	var metricsAddr string
	var probeAddr string
	var dashboardAddr string
	var enableLeaderElection bool
	var demoReplayInput string
	var showVersion bool
	var discoveryEnabled bool
	var discoveryNamespace string
	var discoveryConfigMapName string
	var discoveryInterval time.Duration
	var promConfig prometheusRuntimeConfig

	flag.StringVar(&metricsAddr, "metrics-bind-address", ":8080", "The address the metric endpoint binds to.")
	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.StringVar(&dashboardAddr, "dashboard-bind-address", ":8082", "The address the read-only workload qualification dashboard binds to; set to 0 to disable.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false, "Enable leader election for controller manager.")
	flag.BoolVar(&showVersion, "version", false, "print the controller version")
	flag.BoolVar(&discoveryEnabled, "cluster-discovery", true, "publish cluster-wide discovery inventory for HPA-managed Deployments")
	flag.StringVar(&discoveryNamespace, "discovery-namespace", "skale-system", "namespace for the cluster discovery inventory ConfigMap")
	flag.StringVar(&discoveryConfigMapName, "discovery-configmap", "skale-discovery-inventory", "name of the cluster discovery inventory ConfigMap")
	flag.DurationVar(&discoveryInterval, "discovery-interval", 5*time.Minute, "interval between cluster discovery inventory scans")
	flag.StringVar(
		&demoReplayInput,
		"demo-replay-input",
		"",
		"optional path to a replay-input JSON fixture used as a static metrics provider for local demos",
	)
	flag.StringVar(&promConfig.URL, "prometheus-url", "", "optional Prometheus base URL for live controller telemetry")
	flag.DurationVar(&promConfig.Step, "prometheus-step", 30*time.Second, "Prometheus query step for live controller telemetry")
	flag.DurationVar(&promConfig.DependencyQueryLookback, "dependency-query-lookback", time.Minute, "lookback window used to evaluate dependency health checks")
	flag.StringVar(&promConfig.DemandQuery, "promql-demand", "", "PromQL query for normalized demand; may use $namespace, $name, and $deployment")
	flag.StringVar(&promConfig.ReplicasQuery, "promql-replicas", "", "PromQL query for current replicas; may use $namespace, $name, and $deployment")
	flag.StringVar(&promConfig.CPUQuery, "promql-cpu", "", "PromQL query for CPU saturation ratio")
	flag.StringVar(&promConfig.MemoryQuery, "promql-memory", "", "PromQL query for memory saturation ratio")
	flag.StringVar(&promConfig.LatencyQuery, "promql-latency", "", "optional PromQL query for latency enrichment")
	flag.StringVar(&promConfig.ErrorsQuery, "promql-errors", "", "optional PromQL query for error-rate enrichment")
	flag.StringVar(&promConfig.WarmupQuery, "promql-warmup", "", "optional PromQL query for warmup observations when warmup is not fixed in policy")
	flag.StringVar(&promConfig.NodeHeadroomQuery, "promql-node-headroom", "", "optional PromQL query for node-headroom telemetry enrichment")
	flag.Parse()
	if showVersion {
		fmt.Fprintln(os.Stdout, version.String())
		return
	}

	var metricsProvider metrics.Provider
	var dependencyEvaluator controller.DependencyEvaluator
	var evaluationNow func() time.Time
	var readinessExpectedResolution time.Duration
	var forecastSeasonalityOverride time.Duration
	if demoReplayInput != "" {
		spec, provider, err := replayinput.LoadFile(demoReplayInput)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load demo replay input: %v\n", err)
			os.Exit(1)
		}
		metricsProvider = provider
		readinessExpectedResolution = demoReadinessExpectedResolution(spec)
		forecastSeasonalityOverride = demoForecastSeasonality(spec)
		evaluationTime, ok := demoEvaluationTime(provider)
		if ok {
			evaluationNow = func() time.Time { return evaluationTime }
		}
		fmt.Fprintf(
			os.Stdout,
			"controller demo mode: using static replay input for %s/%s",
			spec.Target.Namespace,
			spec.Target.Name,
		)
		if ok {
			fmt.Fprintf(os.Stdout, " at %s", evaluationTime.UTC().Format(time.RFC3339))
		}
		fmt.Fprintln(os.Stdout)
	}
	if metricsProvider == nil && promConfig.enabled() {
		metricsProvider, dependencyEvaluator = promConfig.build()
	}

	if err := controller.Run(
		ctrl.SetupSignalHandler(),
		controller.Options{
			MetricsBindAddress:          metricsAddr,
			HealthProbeBindAddress:      probeAddr,
			DashboardBindAddress:        dashboardAddr,
			LeaderElection:              enableLeaderElection,
			MetricsProvider:             metricsProvider,
			DependencyEvaluator:         dependencyEvaluator,
			ReadinessExpectedResolution: readinessExpectedResolution,
			ForecastSeasonalityOverride: forecastSeasonalityOverride,
			Now:                         evaluationNow,
			DiscoveryDisabled:           !discoveryEnabled,
			DiscoveryNamespace:          discoveryNamespace,
			DiscoveryConfigMapName:      discoveryConfigMapName,
			DiscoveryInterval:           discoveryInterval,
		},
	); err != nil {
		fmt.Fprintf(os.Stderr, "controller exited with error: %v\n", err)
		os.Exit(1)
	}
}

func demoEvaluationTime(provider metrics.Provider) (time.Time, bool) {
	static, ok := provider.(replayinput.StaticProvider)
	if !ok {
		return time.Time{}, false
	}
	if !static.Snapshot.Window.End.IsZero() {
		return static.Snapshot.Window.End.UTC(), true
	}
	_, end, ok := replayinput.SeriesBounds(static.Snapshot)
	if !ok {
		return time.Time{}, false
	}
	return end.UTC(), true
}

func demoReadinessExpectedResolution(spec replay.Spec) time.Duration {
	if spec.Step <= 0 {
		return 0
	}
	return spec.Step
}

func demoForecastSeasonality(spec replay.Spec) time.Duration {
	if spec.Policy.ForecastSeasonality <= 0 {
		return 0
	}
	return spec.Policy.ForecastSeasonality
}
