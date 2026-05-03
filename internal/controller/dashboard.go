package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/dashboard"
	"github.com/oswalpalash/skale/internal/discovery"
	"github.com/oswalpalash/skale/internal/metrics"
)

const defaultDashboardBindAddress = ":8082"

// DashboardServer serves the read-only workload qualification console.
type DashboardServer struct {
	Client        client.Client
	Namespace     string
	ConfigMapName string
	BindAddress   string
	Metrics       metrics.Provider
	Now           func() time.Time
}

// Start implements manager.Runnable.
func (s *DashboardServer) Start(ctx context.Context) error {
	if s.Client == nil {
		return fmt.Errorf("dashboard server requires a Kubernetes client")
	}
	address := s.BindAddress
	if address == "" {
		address = defaultDashboardBindAddress
	}
	if address == "0" {
		<-ctx.Done()
		return nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleHTML)
	mux.HandleFunc("/api/overview", s.handleOverview)
	mux.HandleFunc("/api/workloads/", s.handleTimeline)

	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown dashboard server: %w", err)
		}
		return nil
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("serve dashboard: %w", err)
		}
		return nil
	}
}

// NeedLeaderElection keeps the read-only dashboard available while the controller
// is waiting to acquire the reconciliation lease.
func (s *DashboardServer) NeedLeaderElection() bool {
	return false
}

func (s *DashboardServer) handleHTML(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	overview, err := s.overview(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	body, err := dashboard.RenderHTML(overview)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func (s *DashboardServer) handleOverview(w http.ResponseWriter, r *http.Request) {
	overview, err := s.overview(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(overview)
}

func (s *DashboardServer) handleTimeline(w http.ResponseWriter, r *http.Request) {
	namespace, name, ok := parseTimelinePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	timeline, err := s.timeline(r.Context(), namespace, name, parseLookback(r.URL.Query().Get("lookback")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(timeline)
}

func (s *DashboardServer) overview(ctx context.Context) (dashboard.Overview, error) {
	namespace := s.Namespace
	if namespace == "" {
		namespace = defaultDiscoveryNamespace
	}
	name := s.ConfigMapName
	if name == "" {
		name = defaultDiscoveryName
	}

	var configMap corev1.ConfigMap
	if err := s.Client.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &configMap); err != nil {
		if apierrors.IsNotFound(err) {
			return dashboard.Overview{}, fmt.Errorf("discovery inventory %s/%s is not available yet", namespace, name)
		}
		return dashboard.Overview{}, fmt.Errorf("load discovery inventory %s/%s: %w", namespace, name, err)
	}

	payload := configMap.Data["inventory.json"]
	if payload == "" {
		return dashboard.Overview{}, fmt.Errorf("discovery inventory %s/%s does not contain inventory.json", namespace, name)
	}
	var inventory discovery.Inventory
	if err := json.Unmarshal([]byte(payload), &inventory); err != nil {
		return dashboard.Overview{}, fmt.Errorf("parse discovery inventory %s/%s: %w", namespace, name, err)
	}

	var policies skalev1alpha1.PredictiveScalingPolicyList
	if err := s.Client.List(ctx, &policies); err != nil {
		return dashboard.Overview{}, fmt.Errorf("list predictive scaling policies: %w", err)
	}

	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	return dashboard.BuildOverview(inventory, policies.Items, now), nil
}

func (s *DashboardServer) timeline(ctx context.Context, namespace, name string, lookback time.Duration) (dashboard.Timeline, error) {
	now := time.Now().UTC()
	if s.Now != nil {
		now = s.Now().UTC()
	}
	if lookback <= 0 {
		lookback = 30 * time.Minute
	}
	timeline := dashboard.Timeline{
		Workload:    namespace + "/" + name,
		GeneratedAt: now,
		WindowStart: now.Add(-lookback),
		WindowEnd:   now,
		Source:      "prometheus range query",
	}
	if s.Metrics == nil {
		timeline.UnavailableText = "metrics provider is not configured"
		return timeline, nil
	}

	snapshot, err := s.Metrics.LoadWindow(ctx, metrics.Target{Namespace: namespace, Name: name}, metrics.Window{
		Start: timeline.WindowStart,
		End:   timeline.WindowEnd,
	})
	if err != nil {
		timeline.UnavailableText = err.Error()
		return timeline, nil
	}
	for _, sample := range snapshot.Replicas.Samples {
		timeline.Samples = append(timeline.Samples, dashboard.TimelineSample{
			Timestamp: sample.Timestamp,
			Current:   sample.Value,
		})
	}
	timeline.Demand = signalSamples(snapshot.Demand)
	if snapshot.CPU != nil {
		timeline.CPU = signalSamples(*snapshot.CPU)
	}
	if snapshot.Memory != nil {
		timeline.Memory = signalSamples(*snapshot.Memory)
	}

	var policies skalev1alpha1.PredictiveScalingPolicyList
	if err := s.Client.List(ctx, &policies, client.InNamespace(namespace)); err == nil {
		for _, policy := range policies.Items {
			policy.Default()
			if policy.Spec.TargetRef.Name != name || policy.Status.LastRecommendation == nil {
				continue
			}
			recommendation := policy.Status.LastRecommendation
			if timelineRecommendationDisplayable(policy.Status, recommendation) {
				timestamp := now
				if recommendation.EvaluatedAt != nil {
					timestamp = recommendation.EvaluatedAt.Time.UTC()
				}
				timeline.Recommendation = &dashboard.TimelinePoint{
					Timestamp: timestamp,
					Replicas:  float64(recommendation.RecommendedReplicas),
					State:     string(recommendation.State),
				}
			}
			break
		}
	}
	return timeline, nil
}

func timelineRecommendationDisplayable(status skalev1alpha1.PredictiveScalingPolicyStatus, recommendation *skalev1alpha1.RecommendationSummary) bool {
	if recommendation == nil {
		return false
	}
	if status.TelemetryReadiness == nil || status.TelemetryReadiness.State != skalev1alpha1.TelemetryReadinessStateReady {
		return false
	}
	for _, reason := range status.SuppressionReasons {
		if reason.Code == "telemetry_not_ready" {
			return false
		}
	}
	return recommendation.State == skalev1alpha1.RecommendationStateAvailable ||
		recommendation.State == skalev1alpha1.RecommendationStateSuppressed
}

func signalSamples(series metrics.SignalSeries) []dashboard.SignalSample {
	samples := make([]dashboard.SignalSample, 0, len(series.Samples))
	for _, sample := range series.Samples {
		samples = append(samples, dashboard.SignalSample{
			Timestamp: sample.Timestamp,
			Value:     sample.Value,
		})
	}
	return samples
}

func parseTimelinePath(path string) (string, string, bool) {
	trimmed := strings.TrimPrefix(path, "/api/workloads/")
	if trimmed == path {
		return "", "", false
	}
	parts := strings.Split(trimmed, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] != "timeline" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func parseLookback(value string) time.Duration {
	if value == "" {
		return 30 * time.Minute
	}
	lookback, err := time.ParseDuration(value)
	if err != nil || lookback <= 0 {
		return 30 * time.Minute
	}
	if lookback > 6*time.Hour {
		return 6 * time.Hour
	}
	return lookback
}
