package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	skalev1alpha1 "github.com/oswalpalash/skale/api/v1alpha1"
	"github.com/oswalpalash/skale/internal/explain"
)

// ErrUnsupportedTargetRef is returned when the policy points at a workload outside the supported v1 wedge.
var ErrUnsupportedTargetRef = errors.New("unsupported target reference")

// ResolvedTarget is the controller-facing workload identity after API resolution.
type ResolvedTarget struct {
	Identity   explain.WorkloadIdentity
	APIVersion string
	Message    string
}

// TargetResolver resolves the CRD target reference into the concrete supported workload identity.
type TargetResolver interface {
	Resolve(ctx context.Context, reader client.Reader, policy *skalev1alpha1.PredictiveScalingPolicy) (ResolvedTarget, error)
}

// KubernetesTargetResolver resolves Deployments and optionally discovers the HPA targeting them.
type KubernetesTargetResolver struct{}

func (KubernetesTargetResolver) Resolve(ctx context.Context, reader client.Reader, policy *skalev1alpha1.PredictiveScalingPolicy) (ResolvedTarget, error) {
	target := policy.Spec.TargetRef
	if target.Kind != "" && target.Kind != "Deployment" {
		return ResolvedTarget{}, fmt.Errorf("%w: kind %q is not supported in v1", ErrUnsupportedTargetRef, target.Kind)
	}

	var deployment appsv1.Deployment
	key := client.ObjectKey{Namespace: policy.Namespace, Name: target.Name}
	if err := reader.Get(ctx, key, &deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return ResolvedTarget{}, err
		}
		return ResolvedTarget{}, fmt.Errorf("get target deployment: %w", err)
	}

	hpaName, hpaMessage, err := findTargetHPA(ctx, reader, deployment.Namespace, deployment.Name)
	if err != nil {
		return ResolvedTarget{}, err
	}

	identity := explain.WorkloadIdentity{
		Namespace: deployment.Namespace,
		Name:      deployment.Name,
		Kind:      "Deployment",
		Resource:  deployment.Namespace + "/" + deployment.Name,
		HPAName:   hpaName,
	}

	message := fmt.Sprintf("resolved Deployment %s/%s", deployment.Namespace, deployment.Name)
	if hpaMessage != "" {
		message = message + "; " + hpaMessage
	}

	return ResolvedTarget{
		Identity:   identity,
		APIVersion: "apps/v1",
		Message:    message,
	}, nil
}

func findTargetHPA(ctx context.Context, reader client.Reader, namespace, deploymentName string) (string, string, error) {
	var hpas autoscalingv2.HorizontalPodAutoscalerList
	if err := reader.List(ctx, &hpas, client.InNamespace(namespace)); err != nil {
		return "", "", fmt.Errorf("list horizontal pod autoscalers: %w", err)
	}

	matches := make([]string, 0, len(hpas.Items))
	for _, hpa := range hpas.Items {
		if hpa.Spec.ScaleTargetRef.Kind == "Deployment" && hpa.Spec.ScaleTargetRef.Name == deploymentName {
			matches = append(matches, hpa.Name)
		}
	}
	if len(matches) == 0 {
		return "", "no matching HPA reference was found for the target Deployment", nil
	}

	sort.Strings(matches)
	if len(matches) == 1 {
		return matches[0], fmt.Sprintf("resolved HPA %q for the target Deployment", matches[0]), nil
	}

	return matches[0], fmt.Sprintf("multiple HPAs reference the target Deployment; reporting %q", matches[0]), nil
}
