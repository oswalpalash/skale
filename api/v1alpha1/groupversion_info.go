// Package v1alpha1 contains API schema definitions for the skale.io v1alpha1 API group.
// +kubebuilder:object:generate=true
// +groupName=skale.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion identifies the API group and version for predictive scaling resources.
	GroupVersion = schema.GroupVersion{Group: "skale.io", Version: "v1alpha1"}

	// SchemeBuilder registers API types into a runtime scheme.
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds this API group to a runtime scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)
