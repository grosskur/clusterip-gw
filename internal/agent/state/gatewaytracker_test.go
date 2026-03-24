package state

import (
	"testing"

	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayChangeTrackerKeepsTCPFrontend(t *testing.T) {
	tracker := NewGatewayChangeTracker()
	gateway := testGateway("default", "demo", gatewaymeta.GatewayClassName, "10.96.0.10")

	tracker.Update(nil, gateway)
	snapshot := tracker.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("expected 1 frontend port, got %d", len(snapshot))
	}
}

func TestGatewayChangeTrackerRejectsUnsupportedGateway(t *testing.T) {
	tracker := NewGatewayChangeTracker()
	gateway := testGateway("default", "demo", "other-controller", "10.96.0.10")

	tracker.Update(nil, gateway)
	snapshot := tracker.Snapshot()
	if len(snapshot) != 0 {
		t.Fatalf("expected unsupported gateway to be omitted, got %d frontends", len(snapshot))
	}
}

func TestGatewayChangeTrackerOmitsTerminatingGateway(t *testing.T) {
	tracker := NewGatewayChangeTracker()
	gateway := testGateway("default", "demo", gatewaymeta.GatewayClassName, "10.96.0.10")
	now := metav1.Now()
	gateway.DeletionTimestamp = &now

	tracker.Update(nil, gateway)
	snapshot := tracker.Snapshot()
	if len(snapshot) != 0 {
		t.Fatalf("expected terminating gateway to be omitted, got %d frontends", len(snapshot))
	}
}

func testGateway(namespace, name, className, vip string) *gatewayv1.Gateway {
	addressType := gatewayv1.IPAddressType
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(className),
			Listeners: []gatewayv1.Listener{{
				Name:     gatewayv1.SectionName("tcp"),
				Port:     80,
				Protocol: gatewayv1.ProtocolType("TCP"),
			}},
		},
		Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{{
				Type:  &addressType,
				Value: vip,
			}},
		},
	}
}
