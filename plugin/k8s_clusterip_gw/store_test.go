package k8sclusteripgw

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/cache"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestInformerGatewayStoreGetByKeyPanicsOnUnexpectedCacheType(t *testing.T) {
	informer := cache.NewSharedIndexInformer(&cache.ListWatch{}, &gatewayv1.Gateway{}, 0, cache.Indexers{})
	if err := informer.GetStore().Add(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "demo"}}); err != nil {
		t.Fatalf("add pod to store: %v", err)
	}

	store := &informerGatewayStore{informer: informer}
	expectPanic(t, func() {
		_, _, _ = store.GetByKey("default/demo")
	})
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
