package state

import (
	"net"
	"sync"

	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// GatewayChangeTracker tracks Gateway objects and materializes frontend snapshots.
type GatewayChangeTracker struct {
	lock     sync.RWMutex
	gateways map[types.NamespacedName]FrontendMap
}

// NewGatewayChangeTracker returns an empty GatewayChangeTracker.
func NewGatewayChangeTracker() *GatewayChangeTracker {
	return &GatewayChangeTracker{
		gateways: make(map[types.NamespacedName]FrontendMap),
	}
}

// Update records a Gateway add, update, or delete event.
func (gct *GatewayChangeTracker) Update(previous, current *gatewayv1.Gateway) bool {
	gct.lock.Lock()
	defer gct.lock.Unlock()

	var gateway *gatewayv1.Gateway
	switch {
	case current != nil:
		gateway = current
	case previous != nil:
		gateway = previous
	default:
		return false
	}

	key := types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}
	if current == nil {
		delete(gct.gateways, key)
		return true
	}

	m := gatewayToFrontendMap(current)
	if len(m) == 0 {
		delete(gct.gateways, key)
		return true
	}
	gct.gateways[key] = m
	return true
}

// Snapshot returns the current supported frontend ports keyed by frontend name.
func (gct *GatewayChangeTracker) Snapshot() FrontendMap {
	gct.lock.RLock()
	defer gct.lock.RUnlock()

	out := make(FrontendMap)
	for _, frontendPorts := range gct.gateways {
		for name, port := range frontendPorts {
			out[name] = port
		}
	}
	return out
}

func gatewayToFrontendMap(gateway *gatewayv1.Gateway) FrontendMap {
	if gateway == nil || !gatewaySupported(gateway) {
		return nil
	}

	vip := net.ParseIP(gateway.Status.Addresses[0].Value)
	if vip == nil || vip.To4() == nil {
		return nil
	}

	out := make(FrontendMap, len(gateway.Spec.Listeners))
	for i := range gateway.Spec.Listeners {
		listener := gateway.Spec.Listeners[i]
		key := FrontendKey{
			NamespacedName: types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name},
			Listener:       string(listener.Name),
		}
		out[key] = NewFrontend(vip, int(listener.Port))
	}
	return out
}

func gatewaySupported(gateway *gatewayv1.Gateway) bool {
	if gateway.Namespace == "" || gateway.Name == "" {
		return false
	}
	if gateway.DeletionTimestamp != nil {
		return false
	}
	if gateway.Spec.GatewayClassName != gatewayv1.ObjectName(gatewaymeta.GatewayClassName) {
		return false
	}
	if len(gateway.Spec.Listeners) == 0 || len(gateway.Spec.Listeners) > gatewaymeta.MaxSupportedGatewayListeners {
		return false
	}
	names := make(map[gatewayv1.SectionName]struct{}, len(gateway.Spec.Listeners))
	ports := make(map[gatewayv1.PortNumber]struct{}, len(gateway.Spec.Listeners))
	for i := range gateway.Spec.Listeners {
		listener := gateway.Spec.Listeners[i]
		if listener.Protocol != gatewayv1.ProtocolType("TCP") || listener.Name == "" || listener.Port == 0 {
			return false
		}
		if _, ok := names[listener.Name]; ok {
			return false
		}
		names[listener.Name] = struct{}{}
		if _, ok := ports[listener.Port]; ok {
			return false
		}
		ports[listener.Port] = struct{}{}
	}
	if len(gateway.Status.Addresses) != 1 {
		return false
	}
	address := gateway.Status.Addresses[0]
	if address.Type == nil || *address.Type != gatewayv1.IPAddressType || address.Value == "" {
		return false
	}
	return true
}
