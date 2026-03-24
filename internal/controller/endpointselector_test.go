package controller

import (
	"context"
	"strings"
	"testing"

	apiv1alpha1 "github.com/grosskur/clusterip-gw/apis/v1alpha1"
	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func TestReconcileRouteRejectsServiceBackendRef(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	route := testServiceBackendRoute()

	controller := newTestController(t,
		nil,
		[]runtime.Object{gatewayClass, gateway, route},
		nil,
		[]runtime.Object{gatewayClass, gateway, route},
	)

	key := route.Namespace + "/" + route.Name
	if err := controller.reconcileRoute(context.Background(), key); err != nil {
		t.Fatalf("reconcileRoute returned error: %v", err)
	}

	liveRoute, err := controller.gatewayClient.GatewayV1alpha2().TCPRoutes(route.Namespace).Get(context.Background(), route.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get route after reconcile: %v", err)
	}
	parent := liveRoute.Status.Parents[0]
	if got := testConditionReason(parent.Conditions, string(gatewayv1.RouteConditionResolvedRefs)); got != string(gatewayv1.RouteReasonInvalidKind) {
		t.Fatalf("expected ResolvedRefs reason InvalidKind, got %#v", parent.Conditions)
	}
}

func TestReconcileEndpointSelectorSetsAcceptedCondition(t *testing.T) {
	selector := testEndpointSelector()

	controller := newTestController(t,
		nil,
		nil,
		[]runtime.Object{selector},
		[]runtime.Object{selector},
	)

	if err := controller.reconcileEndpointSelector(context.Background(), selector.Namespace+"/"+selector.Name); err != nil {
		t.Fatalf("reconcileEndpointSelector returned error: %v", err)
	}

	if err := syncEndpointSelectorStore(t, controller, selector.Namespace, selector.Name); err != nil {
		t.Fatalf("sync XEndpointSelector store: %v", err)
	}

	liveSelector, err := controller.getEndpointSelector(types.NamespacedName{Namespace: selector.Namespace, Name: selector.Name})
	if err != nil {
		t.Fatalf("get XEndpointSelector after sync: %v", err)
	}
	if len(liveSelector.Status.Conditions) != 1 {
		t.Fatalf("expected exactly one status condition, got %#v", liveSelector.Status.Conditions)
	}
	condition := liveSelector.Status.Conditions[0]
	if condition.Type != apiv1alpha1.EndpointSelectorConditionAccepted {
		t.Fatalf("expected Accepted condition, got %#v", condition)
	}
	if condition.Status != metav1.ConditionTrue {
		t.Fatalf("expected Accepted=True, got %#v", condition)
	}
	if condition.ObservedGeneration != selector.Generation {
		t.Fatalf("expected observedGeneration %d, got %#v", selector.Generation, condition)
	}
}

func TestReconcileEndpointSelectorCreatesEndpointSliceForReadyMatchingPods(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	route := testRoute()
	selector := testEndpointSelector()
	readyPod := testPod("ready-pod", map[string]string{"app": "demo"}, true, "10.0.0.5")
	unreadyPod := testPod("unready-pod", map[string]string{"app": "demo"}, false, "10.0.0.6")
	otherPod := testPod("other-pod", map[string]string{"app": "other"}, true, "10.0.0.7")

	controller := newTestController(t,
		[]runtime.Object{readyPod, unreadyPod, otherPod},
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{readyPod, unreadyPod, otherPod, gatewayClass, gateway, route, selector},
	)

	if err := controller.reconcileEndpointSelector(context.Background(), selector.Namespace+"/"+selector.Name); err != nil {
		t.Fatalf("reconcileEndpointSelector returned error: %v", err)
	}

	slices, err := controller.coreClient.DiscoveryV1().EndpointSlices(selector.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list EndpointSlices: %v", err)
	}
	if len(slices.Items) != 1 {
		t.Fatalf("expected one EndpointSlice, got %#v", slices.Items)
	}
	endpointSlice := slices.Items[0]
	if endpointSlice.Labels[discoveryv1.LabelManagedBy] != gatewaymeta.ManagedByValue {
		t.Fatalf("expected managed-by label %q, got %#v", gatewaymeta.ManagedByValue, endpointSlice.Labels)
	}
	if endpointSlice.Labels[gatewaymeta.EndpointSelectorNamespaceLabelKey] != selector.Namespace ||
		endpointSlice.Labels[gatewaymeta.EndpointSelectorNameLabelKey] != selector.Name {
		t.Fatalf("expected selector labels for %s/%s, got %#v", selector.Namespace, selector.Name, endpointSlice.Labels)
	}
	if endpointSlice.Labels[gatewaymeta.GatewayNamespaceLabelKey] != gateway.Namespace ||
		endpointSlice.Labels[gatewaymeta.GatewayNameLabelKey] != gateway.Name ||
		endpointSlice.Labels[gatewaymeta.GatewayListenerLabelKey] != "tcp" {
		t.Fatalf("expected gateway labels for %s/%s listener tcp, got %#v", gateway.Namespace, gateway.Name, endpointSlice.Labels)
	}
	controllerRef := metav1.GetControllerOfNoCopy(&endpointSlice)
	if controllerRef == nil || controllerRef.APIVersion != gatewayv1.GroupVersion.String() || controllerRef.Kind != "Gateway" || controllerRef.Name != gateway.Name {
		t.Fatalf("expected Gateway controller owner reference for %q, got %#v", gateway.Name, endpointSlice.OwnerReferences)
	}
	if len(endpointSlice.Ports) != 1 || endpointSlice.Ports[0].Port == nil || *endpointSlice.Ports[0].Port != 80 {
		t.Fatalf("expected single TCP port 80, got %#v", endpointSlice.Ports)
	}
	if len(endpointSlice.Endpoints) != 1 {
		t.Fatalf("expected one ready endpoint, got %#v", endpointSlice.Endpoints)
	}
	if got := endpointSlice.Endpoints[0].Addresses[0]; got != readyPod.Status.PodIP {
		t.Fatalf("expected ready pod IP %q, got %q", readyPod.Status.PodIP, got)
	}
}

func TestReconcileEndpointSelectorCreatesEndpointSlicesForMultipleListenerBindings(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGatewayWithListeners(
		testTCPListener("tcp-80", 80),
		testTCPListener("tcp-81", 81),
	)
	route80 := testRouteForListener("demo-route-80", "demo-selector", "tcp-80", 8080)
	route81 := testRouteForListener("demo-route-81", "demo-selector", "tcp-81", 8081)
	selector := testEndpointSelector()
	readyPod := testPod("ready-pod", map[string]string{"app": "demo"}, true, "10.0.0.5")

	controller := newTestController(t,
		[]runtime.Object{readyPod},
		[]runtime.Object{gatewayClass, gateway, route80, route81},
		[]runtime.Object{selector},
		[]runtime.Object{readyPod, gatewayClass, gateway, route80, route81, selector},
	)

	if err := controller.reconcileEndpointSelector(context.Background(), selector.Namespace+"/"+selector.Name); err != nil {
		t.Fatalf("reconcileEndpointSelector returned error: %v", err)
	}

	slices, err := controller.coreClient.DiscoveryV1().EndpointSlices(selector.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list EndpointSlices: %v", err)
	}
	if len(slices.Items) != 2 {
		t.Fatalf("expected two EndpointSlices, got %#v", slices.Items)
	}

	listeners := map[string]int32{}
	for _, endpointSlice := range slices.Items {
		if len(endpointSlice.Ports) != 1 || endpointSlice.Ports[0].Port == nil {
			t.Fatalf("expected single endpoint port, got %#v", endpointSlice.Ports)
		}
		listeners[endpointSlice.Labels[gatewaymeta.GatewayListenerLabelKey]] = *endpointSlice.Ports[0].Port
	}
	if got := listeners["tcp-80"]; got != 8080 {
		t.Fatalf("expected tcp-80 slice to use backend port 8080, got %d", got)
	}
	if got := listeners["tcp-81"]; got != 8081 {
		t.Fatalf("expected tcp-81 slice to use backend port 8081, got %d", got)
	}
}

func TestReconcileEndpointSelectorSkipsInvalidListenerBindingsOnPartiallyAcceptedGateway(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGatewayWithListeners(
		testTCPListener("tcp-80", 80),
		testTCPListener("tcp-81", 81),
	)
	route80 := testRouteForListener("demo-route-80", "demo-selector", "tcp-80", 8080)
	selector := testEndpointSelector()
	readyPod := testPod("ready-pod", map[string]string{"app": "demo"}, true, "10.0.0.5")

	controller := newTestController(t,
		[]runtime.Object{readyPod},
		[]runtime.Object{gatewayClass, gateway, route80},
		[]runtime.Object{selector},
		[]runtime.Object{readyPod, gatewayClass, gateway, route80, selector},
	)

	if err := controller.reconcileEndpointSelector(context.Background(), selector.Namespace+"/"+selector.Name); err != nil {
		t.Fatalf("reconcileEndpointSelector returned error: %v", err)
	}

	slices, err := controller.coreClient.DiscoveryV1().EndpointSlices(selector.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list EndpointSlices: %v", err)
	}
	if len(slices.Items) != 1 {
		t.Fatalf("expected one EndpointSlice for the valid listener binding, got %#v", slices.Items)
	}

	endpointSlice := slices.Items[0]
	if endpointSlice.Labels[gatewaymeta.GatewayListenerLabelKey] != "tcp-80" {
		t.Fatalf("expected only tcp-80 slice to be created, got %#v", endpointSlice.Labels)
	}
	if len(endpointSlice.Ports) != 1 || endpointSlice.Ports[0].Port == nil || *endpointSlice.Ports[0].Port != 8080 {
		t.Fatalf("expected tcp-80 slice to use backend port 8080, got %#v", endpointSlice.Ports)
	}
}

func TestReconcileEndpointSelectorDeletesManagedEndpointSlicesWhenUnreferenced(t *testing.T) {
	selector := testEndpointSelector()
	gateway := testGateway()
	existing := desiredEndpointSlice(selector, gateway, "tcp", 80, nil)

	controller := newTestController(t,
		[]runtime.Object{existing},
		nil,
		[]runtime.Object{selector},
		[]runtime.Object{existing, selector},
	)

	if err := controller.reconcileEndpointSelector(context.Background(), selector.Namespace+"/"+selector.Name); err != nil {
		t.Fatalf("reconcileEndpointSelector returned error: %v", err)
	}

	slices, err := controller.coreClient.DiscoveryV1().EndpointSlices(selector.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list EndpointSlices: %v", err)
	}
	if len(slices.Items) != 0 {
		t.Fatalf("expected managed EndpointSlices to be deleted, got %#v", slices.Items)
	}
}

func TestReconcileEndpointSelectorCreatesEndpointSliceForLongSelectorName(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	route := testRoute()
	selector := testEndpointSelector()
	selector.Name = strings.Repeat("a", 64)
	route.Spec.Rules[0].BackendRefs[0].Name = gatewayv1.ObjectName(selector.Name)
	readyPod := testPod("ready-pod", map[string]string{"app": "demo"}, true, "10.0.0.5")

	controller := newTestController(t,
		[]runtime.Object{readyPod},
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{readyPod, gatewayClass, gateway, route, selector},
	)

	if err := controller.reconcileEndpointSelector(context.Background(), selector.Namespace+"/"+selector.Name); err != nil {
		t.Fatalf("reconcileEndpointSelector returned error: %v", err)
	}

	slices, err := controller.coreClient.DiscoveryV1().EndpointSlices(selector.Namespace).List(context.Background(), metav1.ListOptions{})
	if err != nil {
		t.Fatalf("list EndpointSlices: %v", err)
	}
	if len(slices.Items) != 1 {
		t.Fatalf("expected one EndpointSlice, got %#v", slices.Items)
	}
	endpointSlice := slices.Items[0]
	controllerRef := metav1.GetControllerOfNoCopy(&endpointSlice)
	if controllerRef == nil || controllerRef.APIVersion != gatewayv1.GroupVersion.String() || controllerRef.Kind != "Gateway" || controllerRef.Name != gateway.Name {
		t.Fatalf("expected Gateway controller owner reference for %q, got %#v", gateway.Name, endpointSlice.OwnerReferences)
	}
	if got := endpointSelectorKeyForEndpointSlice(&endpointSlice); got != selector.Namespace+"/"+selector.Name {
		t.Fatalf("expected EndpointSlice key %q, got %q", selector.Namespace+"/"+selector.Name, got)
	}
}

func TestEndpointSelectorKeyForEndpointSliceUsesSelectorLabels(t *testing.T) {
	selector := testEndpointSelector()
	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: selector.Namespace,
			Labels: map[string]string{
				discoveryv1.LabelManagedBy:                    gatewaymeta.ManagedByValue,
				gatewaymeta.EndpointSelectorNamespaceLabelKey: selector.Namespace,
				gatewaymeta.EndpointSelectorNameLabelKey:      selector.Name,
			},
		},
	}

	if got := endpointSelectorKeyForEndpointSlice(endpointSlice); got != selector.Namespace+"/"+selector.Name {
		t.Fatalf("expected key %q, got %q", selector.Namespace+"/"+selector.Name, got)
	}
}

func TestEndpointSelectorKeyForEndpointSliceRequiresSelectorNameLabel(t *testing.T) {
	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Labels: map[string]string{
				discoveryv1.LabelManagedBy: gatewaymeta.ManagedByValue,
			},
		},
	}

	if got := endpointSelectorKeyForEndpointSlice(endpointSlice); got != "" {
		t.Fatalf("expected empty key without controller owner reference, got %q", got)
	}
}

func testServiceBackendRoute() *gatewayv1alpha2.TCPRoute {
	port := gatewayv1.PortNumber(80)
	return &gatewayv1alpha2.TCPRoute{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gatewayv1alpha2.GroupVersion.String(),
			Kind:       "TCPRoute",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "service-backend-route",
		},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{{
					Name: gatewayv1.ObjectName("demo"),
				}},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Name: gatewayv1.ObjectName("demo-service"),
						Port: &port,
					},
				}},
			}},
		},
	}
}

func testPod(name string, labelsMap map[string]string, ready bool, podIP string) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
			Labels:    labelsMap,
		},
		Spec: corev1.PodSpec{
			NodeName: "worker-a",
		},
		Status: corev1.PodStatus{
			PodIP: podIP,
			Conditions: []corev1.PodCondition{{
				Type:   corev1.PodReady,
				Status: status,
			}},
		},
	}
}

func syncEndpointSelectorStore(t *testing.T, controller *Controller, namespace, name string) error {
	t.Helper()

	liveSelectorObj, err := controller.dynamicClient.Resource(endpointSelectorGVR).Namespace(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	return controller.endpointSelectorInformer.GetStore().Update(liveSelectorObj)
}
