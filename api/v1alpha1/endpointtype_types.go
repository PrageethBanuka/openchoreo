// Copyright 2026 The OpenChoreo Authors
// SPDX-License-Identifier: Apache-2.0

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EndpointCapability is a routing feature an EndpointType can render.
// +kubebuilder:validation:Enum=Match;RegularExpression;Mirror;MirrorPercent
type EndpointCapability string

const (
	// EndpointCapabilityMatch renders backends that have a match.
	EndpointCapabilityMatch EndpointCapability = "Match"
	// EndpointCapabilityRegularExpression renders header matches by regular expression.
	EndpointCapabilityRegularExpression EndpointCapability = "RegularExpression"
	// EndpointCapabilityMirror renders mirror backends.
	EndpointCapabilityMirror EndpointCapability = "Mirror"
	// EndpointCapabilityMirrorPercent renders a mirror of a share of the
	// requests, rather than all of them.
	EndpointCapabilityMirrorPercent EndpointCapability = "MirrorPercent"
)

// EndpointTypeSpec defines the desired state of EndpointType.
type EndpointTypeSpec struct {
	// Parameters is the schema for Endpoint.spec.parameters values supplied by
	// Endpoint authors (e.g. hostnames, path prefixes). Validated against this
	// schema.
	// +optional
	Parameters *SchemaSection `json:"parameters,omitempty"`

	// EnvironmentConfigs defines the per-env schema.
	// Validates EndpointReleaseBinding.spec.endpointTypeEnvironmentConfigs.
	// +optional
	EnvironmentConfigs *SchemaSection `json:"environmentConfigs,omitempty"`

	// Validations are CEL-based rules evaluated before rendering. All rules must
	// evaluate to true for rendering to proceed.
	// +optional
	Validations []ValidationRule `json:"validations,omitempty"`

	// Capabilities lists the routing features this type renders beyond
	// weighted backends. An Endpoint that uses one the type doesn't list is not
	// released, and clients offer only the strategies it supports. Unset means
	// the type doesn't declare them, and nothing is checked.
	// +optional
	// +listType=set
	Capabilities []EndpointCapability `json:"capabilities,omitempty"`

	// Resources are the routing manifests (e.g. HTTPRoute, Ingress, OpenShift
	// Route) the EndpointType emits on the data plane for each
	// EndpointReleaseBinding. Uses the same templating engine as ComponentType.
	// At render time the CEL context exposes metadata.*, parameters.*,
	// environmentConfigs.*, dataplane.*, environment.* (aliased gateway.*),
	// and backends[] resolved for the binding's environment. rules[] lists the
	// same backends as routing rules: one per backend with a match, in order,
	// then the default rule that splits by weight.
	// +kubebuilder:validation:MinItems=1
	// +listType=map
	// +listMapKey=id
	Resources []ResourceTemplate `json:"resources"`
}

// EndpointTypeStatus defines the observed state of EndpointType.
type EndpointTypeStatus struct {
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=ept;epts
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// EndpointType is the Schema for the endpointtypes API.
// PEs publish EndpointType templates in a namespace; Endpoint authors reference
// them by name from Endpoint.spec.type.
type EndpointType struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EndpointTypeSpec   `json:"spec,omitempty"`
	Status EndpointTypeStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// EndpointTypeList contains a list of EndpointType.
type EndpointTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []EndpointType `json:"items"`
}

func init() {
	SchemeBuilder.Register(&EndpointType{}, &EndpointTypeList{})
}
