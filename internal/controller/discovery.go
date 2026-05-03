package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/oswalpalash/skale/internal/discovery"
)

const (
	defaultDiscoveryInterval  = 5 * time.Minute
	defaultDiscoveryNamespace = "skale-system"
	defaultDiscoveryName      = "skale-discovery-inventory"
)

// ClusterDiscoveryRunner periodically publishes the cluster-wide discovery inventory.
type ClusterDiscoveryRunner struct {
	Client          client.Client
	Scanner         discovery.Scanner
	Namespace       string
	ConfigMapName   string
	Interval        time.Duration
	PublishPolicies bool
}

// Start implements manager.Runnable.
func (r *ClusterDiscoveryRunner) Start(ctx context.Context) error {
	if r.Client == nil {
		return fmt.Errorf("cluster discovery runner requires a Kubernetes client")
	}
	if r.Scanner.Reader == nil {
		r.Scanner.Reader = r.Client
	}
	if err := r.scanAndPublish(ctx); err != nil {
		ctrl.LoggerFrom(ctx).Error(err, "cluster discovery scan failed")
	}

	interval := r.Interval
	if interval <= 0 {
		interval = defaultDiscoveryInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := r.scanAndPublish(ctx); err != nil {
				ctrl.LoggerFrom(ctx).Error(err, "cluster discovery scan failed")
			}
		}
	}
}

func (r *ClusterDiscoveryRunner) scanAndPublish(ctx context.Context) error {
	inventory, err := r.Scanner.Scan(ctx)
	if err != nil {
		return err
	}
	return r.publish(ctx, inventory)
}

func (r *ClusterDiscoveryRunner) publish(ctx context.Context, inventory discovery.Inventory) error {
	payload, err := json.MarshalIndent(inventory, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal discovery inventory: %w", err)
	}

	namespace := r.Namespace
	if namespace == "" {
		namespace = defaultDiscoveryNamespace
	}
	name := r.ConfigMapName
	if name == "" {
		name = defaultDiscoveryName
	}

	desired := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "skale-controller",
				"app.kubernetes.io/part-of":    "skale",
				"skale.io/discovery-inventory": "true",
			},
		},
		Data: map[string]string{
			"inventory.json": string(payload),
			"summary.txt":    discoverySummary(inventory),
		},
	}
	if r.PublishPolicies {
		desired.Data["policy-drafts.yaml"] = policyDrafts(inventory)
	}

	var current corev1.ConfigMap
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := r.Client.Get(ctx, key, &current); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get discovery ConfigMap: %w", err)
		}
		if err := r.Client.Create(ctx, desired); err != nil {
			return fmt.Errorf("create discovery ConfigMap: %w", err)
		}
		logDiscoveryPublished(ctx, inventory, namespace, name)
		return nil
	}

	updated := current.DeepCopy()
	updated.Labels = mergeStringMap(updated.Labels, desired.Labels)
	updated.Data = desired.Data
	if equality.Semantic.DeepEqual(current.Labels, updated.Labels) && equality.Semantic.DeepEqual(current.Data, updated.Data) {
		return nil
	}
	if err := r.Client.Update(ctx, updated); err != nil {
		return fmt.Errorf("update discovery ConfigMap: %w", err)
	}
	logDiscoveryPublished(ctx, inventory, namespace, name)
	return nil
}

func discoverySummary(inventory discovery.Inventory) string {
	return fmt.Sprintf(
		"Skale discovery inventory generated at %s across %s namespaces.\n\ncandidate: %d\nneeds configuration: %d\nlow confidence: %d\nunsupported: %d\npolicy-backed: %d\n\n%s\n",
		inventory.GeneratedAt.UTC().Format(time.RFC3339),
		inventory.Scope.Namespaces,
		inventory.Summary.Candidates,
		inventory.Summary.NeedsConfiguration,
		inventory.Summary.LowConfidence,
		inventory.Summary.Unsupported,
		inventory.Summary.PolicyBacked,
		inventory.Scope.Message,
	)
}

func policyDrafts(inventory discovery.Inventory) string {
	var b strings.Builder
	count := 0
	for _, finding := range inventory.Findings {
		if finding.PolicyDraft == "" {
			continue
		}
		if finding.Status != discovery.StatusCandidate && finding.Status != discovery.StatusNeedsConfiguration {
			continue
		}
		if count > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(finding.PolicyDraft)
		count++
	}
	if count == 0 {
		return "# No candidate or needs-configuration policy drafts were generated in the latest discovery scan.\n"
	}
	return b.String()
}

func mergeStringMap(current, desired map[string]string) map[string]string {
	if len(current) == 0 && len(desired) == 0 {
		return nil
	}
	merged := make(map[string]string, len(current)+len(desired))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range desired {
		merged[key] = value
	}
	return merged
}

func logDiscoveryPublished(ctx context.Context, inventory discovery.Inventory, namespace, name string) {
	ctrl.LoggerFrom(ctx).Info(
		"published cluster discovery inventory",
		"configMap", namespace+"/"+name,
		"candidates", inventory.Summary.Candidates,
		"needsConfiguration", inventory.Summary.NeedsConfiguration,
		"lowConfidence", inventory.Summary.LowConfidence,
		"unsupported", inventory.Summary.Unsupported,
		"policyBacked", inventory.Summary.PolicyBacked,
	)
}
