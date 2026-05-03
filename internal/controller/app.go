package controller

import (
	"context"
	"fmt"
	"time"

	autoscalingv2 "k8s.io/api/autoscaling/v2"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/discovery"
	"github.com/oswalpalash/skale/internal/forecast"
	"github.com/oswalpalash/skale/internal/metrics"
	"github.com/oswalpalash/skale/internal/recommend"
)

// Options captures controller manager runtime configuration.
type Options struct {
	MetricsBindAddress     string
	HealthProbeBindAddress string
	DashboardBindAddress   string
	LeaderElection         bool
	MetricsProvider        metrics.Provider
	ForecastModel          forecast.Model
	DashboardForecasts     []forecast.Model
	RecommendEngine        recommend.Engine
	DependencyEvaluator    DependencyEvaluator
	HeadroomProvider       HeadroomProvider
	// Demo-only overrides keep the static fixture path aligned with replay without changing
	// the normal live-controller defaults for telemetry cadence or forecast seasonality.
	ReadinessExpectedResolution time.Duration
	ForecastSeasonalityOverride time.Duration
	Now                         func() time.Time
	DiscoveryDisabled           bool
	DiscoveryNamespace          string
	DiscoveryConfigMapName      string
	DiscoveryInterval           time.Duration
}

// Run wires the operator manager and the recommendation-only predictive scaling reconciler.
func Run(ctx context.Context, opts Options) error {
	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(autoscalingv2.AddToScheme(scheme))
	utilruntime.Must(skalev1alpha1.AddToScheme(scheme))

	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))

	manager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: opts.MetricsBindAddress,
		},
		HealthProbeBindAddress: opts.HealthProbeBindAddress,
		LeaderElection:         opts.LeaderElection,
		LeaderElectionID:       "controller.skale.io",
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}

	if err := (&PredictiveScalingPolicyReconciler{
		Client:   manager.GetClient(),
		Scheme:   manager.GetScheme(),
		Resolver: KubernetesTargetResolver{},
		Now:      opts.Now,
		Pipeline: EvaluationPipeline{
			MetricsProvider:             opts.MetricsProvider,
			ForecastModel:               opts.ForecastModel,
			RecommendEngine:             opts.RecommendEngine,
			DependencyEvaluator:         opts.DependencyEvaluator,
			HeadroomProvider:            defaultHeadroomProvider(opts.HeadroomProvider, manager.GetClient()),
			ReadinessExpectedResolution: opts.ReadinessExpectedResolution,
			ForecastSeasonalityOverride: opts.ForecastSeasonalityOverride,
		},
	}).SetupWithManager(manager); err != nil {
		return fmt.Errorf("setup predictive scaling policy reconciler: %w", err)
	}

	if !opts.DiscoveryDisabled {
		if err := manager.Add(&ClusterDiscoveryRunner{
			Client: manager.GetClient(),
			Scanner: discovery.Scanner{
				Reader:                       manager.GetClient(),
				MetricsProvider:              opts.MetricsProvider,
				Now:                          opts.Now,
				ExpectedResolution:           opts.ReadinessExpectedResolution,
				IncludeDeploymentsWithoutHPA: true,
			},
			Namespace:       opts.DiscoveryNamespace,
			ConfigMapName:   opts.DiscoveryConfigMapName,
			Interval:        opts.DiscoveryInterval,
			PublishPolicies: true,
		}); err != nil {
			return fmt.Errorf("setup cluster discovery runner: %w", err)
		}
	}

	if opts.DashboardBindAddress != "0" {
		if err := manager.Add(&DashboardServer{
			Client:        manager.GetClient(),
			Namespace:     opts.DiscoveryNamespace,
			ConfigMapName: opts.DiscoveryConfigMapName,
			BindAddress:   opts.DashboardBindAddress,
			Metrics:       opts.MetricsProvider,
			Forecasts:     opts.DashboardForecasts,
			Now:           opts.Now,
		}); err != nil {
			return fmt.Errorf("setup dashboard server: %w", err)
		}
	}

	if err := manager.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("register healthz check: %w", err)
	}
	if err := manager.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return fmt.Errorf("register readyz check: %w", err)
	}

	return manager.Start(ctx)
}

func defaultHeadroomProvider(provider HeadroomProvider, reader client.Reader) HeadroomProvider {
	if provider != nil {
		return provider
	}
	return KubernetesHeadroomProvider{Reader: reader}
}
