package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EndpointSelector condition and reason values used in status reporting.
const (
	EndpointSelectorConditionAccepted = "Accepted"
	EndpointSelectorReasonAccepted    = "Accepted"
	EndpointSelectorReasonInvalid     = "Invalid"
)

// EndpointSelectorSpec identifies backend Pods by label selector.
type EndpointSelectorSpec struct {
	// Selector selects backend Pods in the same namespace.
	Selector metav1.LabelSelector `json:"selector"`
}

// EndpointSelectorStatus reports whether the selector was accepted by the controller.
type EndpointSelectorStatus struct {
	// Conditions describes the current status of the selector.
	// Only the Accepted condition type is supported in this phase.
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=1
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// XEndpointSelector selects backend Pods for Gateway API TCP routing.
type XEndpointSelector struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   EndpointSelectorSpec   `json:"spec"`
	Status EndpointSelectorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// XEndpointSelectorList contains a list of XEndpointSelector.
type XEndpointSelectorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []XEndpointSelector `json:"items"`
}
