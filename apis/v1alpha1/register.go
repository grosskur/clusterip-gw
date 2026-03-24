package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Group and Version identify the XEndpointSelector API served by this module.
const (
	Group   = "gateway.networking.x-k8s.io"
	Version = "v1alpha1"
)

var (
	// SchemeGroupVersion identifies the XEndpointSelector API group and version.
	SchemeGroupVersion = schema.GroupVersion{Group: Group, Version: Version}
	// SchemeBuilder registers XEndpointSelector types with a runtime.Scheme.
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	// AddToScheme adds XEndpointSelector types to a runtime.Scheme.
	AddToScheme = SchemeBuilder.AddToScheme
)

// Resource returns a GroupResource for the XEndpointSelector API group.
func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
}

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&XEndpointSelector{},
		&XEndpointSelectorList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
