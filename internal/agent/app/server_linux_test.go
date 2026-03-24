//go:build linux

package app

import (
	"testing"

	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/tools/cache"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestMustGatewayPanicsOnUnexpectedType(t *testing.T) {
	expectPanic(t, func() {
		mustGateway(struct{}{})
	})
}

func TestMustEndpointSlicePanicsOnUnexpectedTombstoneType(t *testing.T) {
	expectPanic(t, func() {
		mustEndpointSlice(cache.DeletedFinalStateUnknown{Obj: &gatewayv1.Gateway{}})
	})
}

func TestMustEndpointSliceAcceptsEndpointSlice(t *testing.T) {
	slice := &discoveryv1.EndpointSlice{}
	if got := mustEndpointSlice(slice); got != slice {
		t.Fatalf("mustEndpointSlice() = %p, want %p", got, slice)
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
