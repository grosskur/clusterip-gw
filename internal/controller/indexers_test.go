package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/tools/cache"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestGatewayFromObjectPanicsOnUnexpectedType(t *testing.T) {
	expectPanic(t, func() {
		gatewayFromObject(struct{}{})
	})
}

func TestEndpointSelectorFromObjectPanicsOnUnexpectedTombstoneType(t *testing.T) {
	expectPanic(t, func() {
		endpointSelectorFromObject(cache.DeletedFinalStateUnknown{Obj: &corev1.Pod{}})
	})
}

func TestEndpointSelectorFromObjectReturnsUnstructuredObject(t *testing.T) {
	obj := &unstructured.Unstructured{}
	if got := endpointSelectorFromObject(obj); got != obj {
		t.Fatalf("endpointSelectorFromObject() = %p, want %p", got, obj)
	}
}

func TestGatewayFromObjectReturnsGatewayFromTombstone(t *testing.T) {
	gateway := &gatewayv1.Gateway{}
	got := gatewayFromObject(cache.DeletedFinalStateUnknown{Obj: gateway})
	if got != gateway {
		t.Fatalf("gatewayFromObject() = %p, want %p", got, gateway)
	}
}

func expectPanic(t *testing.T, fn func()) {
	t.Helper()

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()

	fn()
}
