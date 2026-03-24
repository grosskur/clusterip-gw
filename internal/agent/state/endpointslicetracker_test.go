package state

import (
	"testing"

	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
)

func TestEndpointsChangeTrackerAggregatesReadyTCPBackends(t *testing.T) {
	tracker := NewEndpointsChangeTracker()
	tcp := v1.ProtocolTCP
	portName := "tcp"
	portNumber := int32(8080)
	ready := true
	notReady := false

	slice := &discoveryv1.EndpointSlice{}
	slice.Namespace = "default"
	slice.Name = "slice-1"
	slice.AddressType = discoveryv1.AddressTypeIPv4
	slice.Labels = map[string]string{
		discoveryv1.LabelManagedBy:           gatewaymeta.ManagedByValue,
		gatewaymeta.GatewayNameLabelKey:      "demo",
		gatewaymeta.GatewayNamespaceLabelKey: "default",
		gatewaymeta.GatewayListenerLabelKey:  "tcp",
	}
	slice.Ports = []discoveryv1.EndpointPort{{
		Name:     &portName,
		Protocol: &tcp,
		Port:     &portNumber,
	}}
	slice.Endpoints = []discoveryv1.Endpoint{
		{Addresses: []string{"10.0.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: &ready}},
		{Addresses: []string{"10.0.0.3"}, Conditions: discoveryv1.EndpointConditions{Ready: &notReady}},
	}

	tracker.Update(slice, false)
	snapshot := tracker.Snapshot()

	if len(snapshot) != 1 {
		t.Fatalf("expected 1 endpoint key, got %d", len(snapshot))
	}

	key := FrontendKey{
		Listener: "tcp",
	}
	key.Namespace = "default"
	key.Name = "demo"
	endpoints := snapshot[key]
	if len(endpoints) != 1 {
		t.Fatalf("expected only ready endpoints, got %d", len(endpoints))
	}
}
