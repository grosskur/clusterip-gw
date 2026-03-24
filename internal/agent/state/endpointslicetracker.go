package state

import (
	"net"
	"slices"
	"sync"

	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/types"
)

// EndpointsChangeTracker tracks EndpointSlice objects and materializes endpoint snapshots.
type EndpointsChangeTracker struct {
	lock   sync.RWMutex
	slices map[types.NamespacedName]*discoveryv1.EndpointSlice
}

// NewEndpointsChangeTracker returns an empty EndpointsChangeTracker.
func NewEndpointsChangeTracker() *EndpointsChangeTracker {
	return &EndpointsChangeTracker{
		slices: make(map[types.NamespacedName]*discoveryv1.EndpointSlice),
	}
}

// Update records an EndpointSlice add, update, or delete event.
func (ect *EndpointsChangeTracker) Update(endpointSlice *discoveryv1.EndpointSlice, remove bool) bool {
	if endpointSlice == nil {
		return false
	}

	key := types.NamespacedName{Namespace: endpointSlice.Namespace, Name: endpointSlice.Name}

	ect.lock.Lock()
	defer ect.lock.Unlock()

	if remove {
		delete(ect.slices, key)
		return true
	}

	endpointSliceCopy := endpointSlice.DeepCopy()
	ect.slices[key] = endpointSliceCopy
	return true
}

// Snapshot returns the current ready IPv4 TCP endpoints keyed by frontend port.
func (ect *EndpointsChangeTracker) Snapshot() EndpointsMap {
	ect.lock.RLock()
	defer ect.lock.RUnlock()

	out := make(EndpointsMap)
	for _, slice := range ect.slices {
		key, ok := frontendKeyForEndpointSlice(slice)
		if !ok || slice.AddressType != discoveryv1.AddressTypeIPv4 {
			continue
		}

		for i := range slice.Ports {
			port := &slice.Ports[i]
			if port.Port == nil {
				continue
			}
			if port.Protocol != nil && *port.Protocol != v1.ProtocolTCP {
				continue
			}

			for j := range slice.Endpoints {
				endpoint := &slice.Endpoints[j]
				if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
					continue
				}
				for _, addr := range endpoint.Addresses {
					ip := net.ParseIP(addr)
					if ip == nil || ip.To4() == nil {
						continue
					}
					out[key] = append(out[key], NewEndpoint(ip.String(), int(*port.Port)))
				}
			}
		}
	}

	for key := range out {
		slices.SortFunc(out[key], func(a, b Endpoint) int {
			if a.String() < b.String() {
				return -1
			}
			if a.String() > b.String() {
				return 1
			}
			return 0
		})
	}

	return out
}

func frontendKeyForEndpointSlice(endpointSlice *discoveryv1.EndpointSlice) (FrontendKey, bool) {
	if endpointSlice == nil {
		return FrontendKey{}, false
	}
	if endpointSlice.Labels[discoveryv1.LabelManagedBy] != gatewaymeta.ManagedByValue {
		return FrontendKey{}, false
	}

	gatewayName := endpointSlice.Labels[gatewaymeta.GatewayNameLabelKey]
	if gatewayName == "" {
		return FrontendKey{}, false
	}
	namespace := endpointSlice.Labels[gatewaymeta.GatewayNamespaceLabelKey]
	if namespace == "" {
		namespace = endpointSlice.Namespace
	}
	if namespace == "" {
		return FrontendKey{}, false
	}

	return FrontendKey{
		NamespacedName: types.NamespacedName{Namespace: namespace, Name: gatewayName},
		Listener:       endpointSlice.Labels[gatewaymeta.GatewayListenerLabelKey],
	}, true
}
