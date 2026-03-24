// Package gatewaymeta defines shared metadata for controller-managed Gateway resources.
package gatewaymeta

// Shared controller/agent metadata.
const (
	GatewayClassName                  = "clusterip-gw"
	MaxSupportedGatewayListeners      = 10
	ManagedByValue                    = "clusterip-gw-controller.grosskur.github.io"
	GatewayNamespaceLabelKey          = "gateway.networking.x-k8s.io/gateway-namespace"
	GatewayNameLabelKey               = "gateway.networking.x-k8s.io/gateway-name"
	GatewayListenerLabelKey           = "gateway.networking.x-k8s.io/gateway-listener"
	EndpointSelectorNamespaceLabelKey = "gateway.networking.x-k8s.io/endpoint-selector-namespace"
	EndpointSelectorNameLabelKey      = "gateway.networking.x-k8s.io/endpoint-selector-name"
)
