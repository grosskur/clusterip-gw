package controller

import (
	"context"
	"fmt"
	"testing"
	"time"

	apiv1alpha1 "github.com/grosskur/clusterip-gw/apis/v1alpha1"
	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
	gatewayfake "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned/fake"
)

func TestEnsureGatewayVIPSkipsTakenIPAddress(t *testing.T) {
	gateway := testGateway()
	serviceCIDR := testServiceCIDR("10.96.0.0/29")
	taken := testIPAddress("10.96.0.1", gateway.Namespace, "other")

	controller := newTestController(t,
		[]runtime.Object{serviceCIDR, taken},
		[]runtime.Object{gateway},
		nil,
		[]runtime.Object{serviceCIDR, taken, gateway},
	)

	vip, err := controller.ensureGatewayVIP(context.Background(), gateway)
	if err != nil {
		t.Fatalf("ensureGatewayVIP returned error: %v", err)
	}
	if vip != "10.96.0.2" {
		t.Fatalf("expected second allocatable IP to be used, got %q", vip)
	}
}

func TestManagedByLabelValueIsValidLabelValue(t *testing.T) {
	if managedByLabelValue != "gateway.networking.x-k8s.io" {
		t.Fatalf("expected managed-by label value %q, got %q", "gateway.networking.x-k8s.io", managedByLabelValue)
	}
	if errs := validation.IsValidLabelValue(managedByLabelValue); len(errs) != 0 {
		t.Fatalf("expected managed-by label value %q to be valid, got %v", managedByLabelValue, errs)
	}
}

func TestReconcileGatewayAddsFinalizerThenAllocatesVIPAndStatus(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	route := testRoute()
	selector := testEndpointSelector()
	serviceCIDR := testServiceCIDR("10.96.0.0/29")

	controller := newTestController(t,
		[]runtime.Object{serviceCIDR},
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{serviceCIDR, gatewayClass, gateway, route, selector},
	)

	liveGateway := reconcileGatewayToSteadyState(t, controller, gateway)
	if len(liveGateway.Status.Addresses) != 1 || liveGateway.Status.Addresses[0].Value != "10.96.0.1" {
		t.Fatalf("expected allocated VIP 10.96.0.1 in gateway status, got %#v", liveGateway.Status.Addresses)
	}
	if conditionStatus(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)) != metav1.ConditionTrue {
		t.Fatalf("expected Accepted condition to be true, got %#v", liveGateway.Status.Conditions)
	}
	if testConditionReason(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) != string(gatewayv1.GatewayReasonPending) {
		t.Fatalf("expected Programmed reason Pending, got %#v", liveGateway.Status.Conditions)
	}

	ipAddress, err := controller.coreClient.NetworkingV1().IPAddresses().Get(context.Background(), "10.96.0.1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get allocated IPAddress: %v", err)
	}
	if ipAddress.Spec.ParentRef == nil || ipAddress.Spec.ParentRef.Name != gateway.Name {
		t.Fatalf("expected IPAddress parentRef to point at gateway, got %#v", ipAddress.Spec.ParentRef)
	}
}

func TestReconcileGatewayClearsStatusWhenGatewayLeavesManagedClass(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	route := testRoute()
	selector := testEndpointSelector()
	serviceCIDR := testServiceCIDR("10.96.0.0/29")

	controller := newTestController(t,
		[]runtime.Object{serviceCIDR},
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{serviceCIDR, gatewayClass, gateway, route, selector},
	)

	liveGateway := reconcileGatewayToSteadyState(t, controller, gateway)
	liveGateway.Spec.GatewayClassName = gatewayv1.ObjectName("other")
	updatedGateway, err := controller.gatewayClient.GatewayV1().Gateways(liveGateway.Namespace).Update(context.Background(), liveGateway, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("update gateway class name: %v", err)
	}
	syncGatewayStore(t, controller, updatedGateway)

	if err := controller.reconcileGateway(context.Background(), gateway.Namespace+"/"+gateway.Name); err != nil {
		t.Fatalf("reconcileGateway after class change returned error: %v", err)
	}

	liveGateway, err = controller.gatewayClient.GatewayV1().Gateways(gateway.Namespace).Get(context.Background(), gateway.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gateway after class change reconcile: %v", err)
	}
	if len(liveGateway.Status.Addresses) != 0 {
		t.Fatalf("expected stale gateway addresses to be cleared, got %#v", liveGateway.Status.Addresses)
	}
	if len(liveGateway.Status.Listeners) != 0 {
		t.Fatalf("expected stale listener status to be cleared, got %#v", liveGateway.Status.Listeners)
	}
	if hasCondition(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)) {
		t.Fatalf("expected Accepted condition to be removed, got %#v", liveGateway.Status.Conditions)
	}
	if hasCondition(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) {
		t.Fatalf("expected Programmed condition to be removed, got %#v", liveGateway.Status.Conditions)
	}
	if containsString(liveGateway.Finalizers, controllerFinalizer) {
		t.Fatalf("expected finalizer to be removed after gateway left managed class, got %v", liveGateway.Finalizers)
	}
	if _, err := controller.coreClient.NetworkingV1().IPAddresses().Get(context.Background(), "10.96.0.1", metav1.GetOptions{}); err == nil {
		t.Fatalf("expected owned IPAddress to be deleted when gateway left managed class")
	}
}

func TestReconcileGatewayAllocationFailureUpdatesListenerProgrammedStatus(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	route := testRoute()
	selector := testEndpointSelector()

	controller := newTestController(t,
		nil,
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{gatewayClass, gateway, route, selector},
	)

	key := gateway.Namespace + "/" + gateway.Name
	if err := controller.reconcileGateway(context.Background(), key); err != nil {
		t.Fatalf("first reconcileGateway returned error: %v", err)
	}

	liveGateway, err := controller.gatewayClient.GatewayV1().Gateways(gateway.Namespace).Get(context.Background(), gateway.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gateway after first reconcile: %v", err)
	}
	syncGatewayStore(t, controller, liveGateway)

	if err := controller.reconcileGateway(context.Background(), key); err != nil {
		t.Fatalf("second reconcileGateway returned error: %v", err)
	}

	liveGateway, err = controller.gatewayClient.GatewayV1().Gateways(gateway.Namespace).Get(context.Background(), gateway.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gateway after failed allocation reconcile: %v", err)
	}
	if conditionStatus(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) != metav1.ConditionFalse {
		t.Fatalf("expected Gateway Programmed to be false, got %#v", liveGateway.Status.Conditions)
	}
	if testConditionReason(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) != string(gatewayv1.GatewayReasonAddressNotAssigned) {
		t.Fatalf("expected Gateway Programmed reason AddressNotAssigned, got %#v", liveGateway.Status.Conditions)
	}
	if len(liveGateway.Status.Listeners) != 1 {
		t.Fatalf("expected one listener status entry, got %#v", liveGateway.Status.Listeners)
	}
	listenerProgrammed := listenerCondition(t, liveGateway.Status.Listeners[0].Conditions, string(gatewayv1.ListenerConditionProgrammed))
	if listenerProgrammed.Status != metav1.ConditionFalse {
		t.Fatalf("expected listener Programmed to be false, got %#v", listenerProgrammed)
	}
	if listenerProgrammed.Reason != string(gatewayv1.GatewayReasonAddressNotAssigned) {
		t.Fatalf("expected listener Programmed reason AddressNotAssigned, got %#v", listenerProgrammed)
	}
	if listenerProgrammed.Message == "VIP reserved; dataplane programming is pending." {
		t.Fatalf("expected listener Programmed message to reflect allocation failure, got %#v", listenerProgrammed)
	}
	if len(liveGateway.Status.Addresses) != 0 {
		t.Fatalf("expected no VIP address on allocation failure, got %#v", liveGateway.Status.Addresses)
	}
}

func TestReconcileGatewayListenerConditionsObserveGatewayGeneration(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	gateway.Generation = 7
	route := testRoute()
	selector := testEndpointSelector()
	serviceCIDR := testServiceCIDR("10.96.0.0/29")

	controller := newTestController(t,
		[]runtime.Object{serviceCIDR},
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{serviceCIDR, gatewayClass, gateway, route, selector},
	)

	liveGateway := reconcileGatewayToSteadyState(t, controller, gateway)
	if len(liveGateway.Status.Listeners) != 1 {
		t.Fatalf("expected one listener status entry, got %#v", liveGateway.Status.Listeners)
	}
	for _, conditionType := range []string{
		string(gatewayv1.ListenerConditionAccepted),
		string(gatewayv1.ListenerConditionResolvedRefs),
		string(gatewayv1.ListenerConditionProgrammed),
	} {
		listenerCondition := listenerCondition(t, liveGateway.Status.Listeners[0].Conditions, conditionType)
		if listenerCondition.ObservedGeneration != gateway.Generation {
			t.Fatalf("expected listener %s observedGeneration %d, got %#v", conditionType, gateway.Generation, listenerCondition)
		}
	}
}

func TestReconcileGatewayAcceptsMultipleListenersAndRoutes(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGatewayWithListeners(
		testTCPListener("tcp-80", 80),
		testTCPListener("tcp-81", 81),
	)
	route80 := testRouteForListener("demo-route-80", "demo-selector", "tcp-80", 8080)
	route81 := testRouteForListener("demo-route-81", "demo-selector", "tcp-81", 8081)
	selector := testEndpointSelector()
	serviceCIDR := testServiceCIDR("10.96.0.0/29")

	controller := newTestController(t,
		[]runtime.Object{serviceCIDR},
		[]runtime.Object{gatewayClass, gateway, route80, route81},
		[]runtime.Object{selector},
		[]runtime.Object{serviceCIDR, gatewayClass, gateway, route80, route81, selector},
	)

	liveGateway := reconcileGatewayToSteadyState(t, controller, gateway)
	if len(liveGateway.Status.Listeners) != 2 {
		t.Fatalf("expected two listener status entries, got %#v", liveGateway.Status.Listeners)
	}
	for _, name := range []gatewayv1.SectionName{"tcp-80", "tcp-81"} {
		status := listenerStatusByName(t, liveGateway.Status.Listeners, name)
		if status.AttachedRoutes != 1 {
			t.Fatalf("expected listener %q to report one attached route, got %#v", name, status)
		}
		if conditionStatus(status.Conditions, string(gatewayv1.ListenerConditionAccepted)) != metav1.ConditionTrue {
			t.Fatalf("expected listener %q Accepted=True, got %#v", name, status.Conditions)
		}
		if conditionStatus(status.Conditions, string(gatewayv1.ListenerConditionResolvedRefs)) != metav1.ConditionTrue {
			t.Fatalf("expected listener %q ResolvedRefs=True, got %#v", name, status.Conditions)
		}
	}
}

func TestReconcileGatewayAcceptsValidListenerWhenAnotherListenerHasNoRoute(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGatewayWithListeners(
		testTCPListener("tcp-80", 80),
		testTCPListener("tcp-81", 81),
	)
	route80 := testRouteForListener("demo-route-80", "demo-selector", "tcp-80", 8080)
	selector := testEndpointSelector()
	serviceCIDR := testServiceCIDR("10.96.0.0/29")

	controller := newTestController(t,
		[]runtime.Object{serviceCIDR},
		[]runtime.Object{gatewayClass, gateway, route80},
		[]runtime.Object{selector},
		[]runtime.Object{serviceCIDR, gatewayClass, gateway, route80, selector},
	)

	liveGateway := reconcileGatewayToSteadyState(t, controller, gateway)
	if conditionStatus(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)) != metav1.ConditionTrue {
		t.Fatalf("expected Gateway Accepted=True, got %#v", liveGateway.Status.Conditions)
	}
	if len(liveGateway.Status.Addresses) != 1 || liveGateway.Status.Addresses[0].Value != "10.96.0.1" {
		t.Fatalf("expected allocated VIP 10.96.0.1 in gateway status, got %#v", liveGateway.Status.Addresses)
	}

	acceptedListener := listenerStatusByName(t, liveGateway.Status.Listeners, "tcp-80")
	if acceptedListener.AttachedRoutes != 1 {
		t.Fatalf("expected tcp-80 to report one attached route, got %#v", acceptedListener)
	}
	if conditionStatus(acceptedListener.Conditions, string(gatewayv1.ListenerConditionAccepted)) != metav1.ConditionTrue {
		t.Fatalf("expected tcp-80 Accepted=True, got %#v", acceptedListener.Conditions)
	}
	if testConditionReason(acceptedListener.Conditions, string(gatewayv1.ListenerConditionProgrammed)) != string(gatewayv1.ListenerReasonPending) {
		t.Fatalf("expected tcp-80 Programmed reason Pending, got %#v", acceptedListener.Conditions)
	}

	rejectedListener := listenerStatusByName(t, liveGateway.Status.Listeners, "tcp-81")
	if rejectedListener.AttachedRoutes != 0 {
		t.Fatalf("expected tcp-81 to report zero attached routes, got %#v", rejectedListener)
	}
	if conditionStatus(rejectedListener.Conditions, string(gatewayv1.ListenerConditionAccepted)) != metav1.ConditionFalse {
		t.Fatalf("expected tcp-81 Accepted=False, got %#v", rejectedListener.Conditions)
	}
	if conditionStatus(rejectedListener.Conditions, string(gatewayv1.ListenerConditionResolvedRefs)) != metav1.ConditionFalse {
		t.Fatalf("expected tcp-81 ResolvedRefs=False, got %#v", rejectedListener.Conditions)
	}
	if testConditionReason(rejectedListener.Conditions, string(gatewayv1.ListenerConditionProgrammed)) != string(gatewayv1.ListenerReasonInvalid) {
		t.Fatalf("expected tcp-81 Programmed reason Invalid, got %#v", rejectedListener.Conditions)
	}
}

func TestReconcileGatewayRejectsMoreThanTenListeners(t *testing.T) {
	gatewayClass := testGatewayClass()
	listeners := make([]gatewayv1.Listener, 0, 11)
	for i := 0; i < 11; i++ {
		listeners = append(listeners, testTCPListener(gatewayv1.SectionName(fmt.Sprintf("tcp-%d", i)), gatewayv1.PortNumber(80+i)))
	}
	gateway := testGatewayWithListeners(listeners...)

	controller := newTestController(t,
		nil,
		[]runtime.Object{gatewayClass, gateway},
		nil,
		[]runtime.Object{gatewayClass, gateway},
	)

	liveGateway := reconcileGatewayToSteadyState(t, controller, gateway)
	if conditionStatus(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)) != metav1.ConditionFalse {
		t.Fatalf("expected Accepted=False, got %#v", liveGateway.Status.Conditions)
	}
	if got := testConditionReason(liveGateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)); got != string(gatewayv1.GatewayReasonListenersNotValid) {
		t.Fatalf("expected Accepted reason ListenersNotValid, got %#v", liveGateway.Status.Conditions)
	}
	if len(liveGateway.Status.Addresses) != 0 {
		t.Fatalf("expected no VIP address for invalid gateway, got %#v", liveGateway.Status.Addresses)
	}
	if len(liveGateway.Status.Listeners) != 11 {
		t.Fatalf("expected listener status entries for all listeners, got %#v", liveGateway.Status.Listeners)
	}
}

func TestValidateGatewayListenersAllowsDefaultSameNamespaceAllowedRoutes(t *testing.T) {
	gateway := testGateway()
	from := gatewayv1.NamespacesFromSame
	gateway.Spec.Listeners[0].AllowedRoutes = &gatewayv1.AllowedRoutes{
		Namespaces: &gatewayv1.RouteNamespaces{
			From: &from,
		},
	}

	validation := validateGatewayListeners(gateway)
	if !validation.valid {
		t.Fatalf("expected default same-namespace allowedRoutes to be accepted, got %#v", validation)
	}
}

func TestValidateGatewayListenersRejectsNonDefaultAllowedRoutes(t *testing.T) {
	gateway := testGateway()
	from := gatewayv1.NamespacesFromAll
	gateway.Spec.Listeners[0].AllowedRoutes = &gatewayv1.AllowedRoutes{
		Namespaces: &gatewayv1.RouteNamespaces{
			From: &from,
		},
	}

	validation := validateGatewayListeners(gateway)
	if validation.valid {
		t.Fatalf("expected non-default allowedRoutes to be rejected")
	}
}

func TestReconcileRouteSetsAcceptedAndResolvedRefs(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	route := testRoute()
	selector := testEndpointSelector()

	controller := newTestController(t,
		nil,
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{gatewayClass, gateway, route, selector},
	)

	key := route.Namespace + "/" + route.Name
	if err := controller.reconcileRoute(context.Background(), key); err != nil {
		t.Fatalf("reconcileRoute returned error: %v", err)
	}

	liveRoute, err := controller.gatewayClient.GatewayV1alpha2().TCPRoutes(route.Namespace).Get(context.Background(), route.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get route after reconcile: %v", err)
	}
	if len(liveRoute.Status.Parents) != 1 {
		t.Fatalf("expected one parent status entry, got %#v", liveRoute.Status.Parents)
	}
	parent := liveRoute.Status.Parents[0]
	if parent.ControllerName != controllerName {
		t.Fatalf("expected controller name %q, got %q", controllerName, parent.ControllerName)
	}
	if conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionAccepted)) != metav1.ConditionTrue {
		t.Fatalf("expected Accepted condition to be true, got %#v", parent.Conditions)
	}
	if conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionResolvedRefs)) != metav1.ConditionTrue {
		t.Fatalf("expected ResolvedRefs condition to be true, got %#v", parent.Conditions)
	}
}

func TestReconcileRouteKeepsAcceptedWhenAnotherGatewayListenerIsInvalid(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGatewayWithListeners(
		testTCPListener("tcp-80", 80),
		testTCPListener("tcp-81", 81),
	)
	route := testRouteForListener("demo-route-80", "demo-selector", "tcp-80", 8080)
	selector := testEndpointSelector()

	controller := newTestController(t,
		nil,
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{gatewayClass, gateway, route, selector},
	)

	key := route.Namespace + "/" + route.Name
	if err := controller.reconcileRoute(context.Background(), key); err != nil {
		t.Fatalf("reconcileRoute returned error: %v", err)
	}

	liveRoute, err := controller.gatewayClient.GatewayV1alpha2().TCPRoutes(route.Namespace).Get(context.Background(), route.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get route after reconcile: %v", err)
	}
	if len(liveRoute.Status.Parents) != 1 {
		t.Fatalf("expected one parent status entry, got %#v", liveRoute.Status.Parents)
	}
	parent := liveRoute.Status.Parents[0]
	if conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionAccepted)) != metav1.ConditionTrue {
		t.Fatalf("expected Accepted condition to stay true, got %#v", parent.Conditions)
	}
	if conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionResolvedRefs)) != metav1.ConditionTrue {
		t.Fatalf("expected ResolvedRefs condition to stay true, got %#v", parent.Conditions)
	}
}

func TestReconcileRouteMarksMissingBackend(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGateway()
	route := testRoute()

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
	if len(liveRoute.Status.Parents) != 1 {
		t.Fatalf("expected one parent status entry, got %#v", liveRoute.Status.Parents)
	}
	parent := liveRoute.Status.Parents[0]
	if conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionAccepted)) != metav1.ConditionTrue {
		t.Fatalf("expected Accepted condition to stay true, got %#v", parent.Conditions)
	}
	if conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionResolvedRefs)) != metav1.ConditionFalse {
		t.Fatalf("expected ResolvedRefs to be false, got %#v", parent.Conditions)
	}
	if testConditionReason(parent.Conditions, string(gatewayv1.RouteConditionResolvedRefs)) != string(gatewayv1.RouteReasonBackendNotFound) {
		t.Fatalf("expected ResolvedRefs reason BackendNotFound, got %#v", parent.Conditions)
	}
}

func TestReconcileRouteRejectsParentRefThatDoesNotUniquelySelectListener(t *testing.T) {
	gatewayClass := testGatewayClass()
	gateway := testGatewayWithListeners(
		testTCPListener("tcp-80", 80),
		testTCPListener("tcp-81", 81),
	)
	route := testRoute()
	selector := testEndpointSelector()

	controller := newTestController(t,
		nil,
		[]runtime.Object{gatewayClass, gateway, route},
		[]runtime.Object{selector},
		[]runtime.Object{gatewayClass, gateway, route, selector},
	)

	key := route.Namespace + "/" + route.Name
	if err := controller.reconcileRoute(context.Background(), key); err != nil {
		t.Fatalf("reconcileRoute returned error: %v", err)
	}

	liveRoute, err := controller.gatewayClient.GatewayV1alpha2().TCPRoutes(route.Namespace).Get(context.Background(), route.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get route after reconcile: %v", err)
	}
	if len(liveRoute.Status.Parents) != 1 {
		t.Fatalf("expected one parent status entry, got %#v", liveRoute.Status.Parents)
	}
	parent := liveRoute.Status.Parents[0]
	if conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionAccepted)) != metav1.ConditionFalse {
		t.Fatalf("expected Accepted=False, got %#v", parent.Conditions)
	}
	if got := testConditionReason(parent.Conditions, string(gatewayv1.RouteConditionAccepted)); got != string(gatewayv1.RouteReasonUnsupportedValue) {
		t.Fatalf("expected Accepted reason UnsupportedValue, got %#v", parent.Conditions)
	}
}

func newTestController(t *testing.T, coreObjects, gatewayObjects, selectorObjects, storeObjects []runtime.Object) *Controller {
	t.Helper()

	coreClient := kubernetesfake.NewSimpleClientset(coreObjects...)
	scheme := runtime.NewScheme()
	if err := apiv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add XEndpointSelector scheme: %v", err)
	}
	dynamicClient := fake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		endpointSelectorGVR: "XEndpointSelectorList",
	})
	for _, obj := range selectorObjects {
		selector, ok := obj.(*apiv1alpha1.XEndpointSelector)
		if !ok {
			t.Fatalf("unsupported selector test object type %T", obj)
		}
		unstructuredSelector, err := endpointSelectorToUnstructured(selector)
		if err != nil {
			t.Fatalf("convert XEndpointSelector client object: %v", err)
		}
		if _, err := dynamicClient.Resource(endpointSelectorGVR).Namespace(selector.Namespace).Create(context.Background(), unstructuredSelector, metav1.CreateOptions{}); err != nil {
			t.Fatalf("seed XEndpointSelector client object: %v", err)
		}
	}
	//nolint:staticcheck // The field-managed Gateway API fake is not behaviorally compatible with these tracker-based unit tests.
	gatewayClient := gatewayfake.NewSimpleClientset()
	for _, obj := range gatewayObjects {
		switch typed := obj.(type) {
		case *gatewayv1.GatewayClass:
			if _, err := gatewayClient.GatewayV1().GatewayClasses().Create(context.Background(), typed, metav1.CreateOptions{}); err != nil {
				t.Fatalf("seed GatewayClass client object: %v", err)
			}
		case *gatewayv1.Gateway:
			if _, err := gatewayClient.GatewayV1().Gateways(typed.Namespace).Create(context.Background(), typed, metav1.CreateOptions{}); err != nil {
				t.Fatalf("seed Gateway client object: %v", err)
			}
		case *gatewayv1alpha2.TCPRoute:
			if _, err := gatewayClient.GatewayV1alpha2().TCPRoutes(typed.Namespace).Create(context.Background(), typed, metav1.CreateOptions{}); err != nil {
				t.Fatalf("seed TCPRoute client object: %v", err)
			}
		default:
			t.Fatalf("unsupported gateway test object type %T", obj)
		}
	}

	controller, err := New(coreClient, dynamicClient, gatewayClient, time.Minute)
	if err != nil {
		t.Fatalf("New controller: %v", err)
	}

	for _, obj := range storeObjects {
		switch typed := obj.(type) {
		case *corev1.Pod:
			if err := controller.podInformer.GetStore().Add(typed); err != nil {
				t.Fatalf("add Pod to informer store: %v", err)
			}
		case *discoveryv1.EndpointSlice:
			if err := controller.endpointSliceInformer.GetStore().Add(typed); err != nil {
				t.Fatalf("add EndpointSlice to informer store: %v", err)
			}
		case *networkingv1.ServiceCIDR:
			if err := controller.serviceCIDRInformer.GetStore().Add(typed); err != nil {
				t.Fatalf("add ServiceCIDR to informer store: %v", err)
			}
		case *networkingv1.IPAddress:
			if err := controller.ipAddressInformer.GetStore().Add(typed); err != nil {
				t.Fatalf("add IPAddress to informer store: %v", err)
			}
		case *gatewayv1.GatewayClass:
			if err := controller.gatewayClassInformer.GetStore().Add(typed); err != nil {
				t.Fatalf("add GatewayClass to informer store: %v", err)
			}
		case *gatewayv1.Gateway:
			if err := controller.gatewayInformer.GetStore().Add(typed); err != nil {
				t.Fatalf("add Gateway to informer store: %v", err)
			}
		case *gatewayv1alpha2.TCPRoute:
			if err := controller.routeInformer.GetStore().Add(typed); err != nil {
				t.Fatalf("add TCPRoute to informer store: %v", err)
			}
		case *apiv1alpha1.XEndpointSelector:
			unstructuredSelector, err := endpointSelectorToUnstructured(typed)
			if err != nil {
				t.Fatalf("convert XEndpointSelector to unstructured: %v", err)
			}
			if err := controller.endpointSelectorInformer.GetStore().Add(unstructuredSelector); err != nil {
				t.Fatalf("add XEndpointSelector to informer store: %v", err)
			}
		default:
			t.Fatalf("unsupported test object type %T", obj)
		}
	}

	return controller
}

func testGatewayClass() *gatewayv1.GatewayClass {
	return &gatewayv1.GatewayClass{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gatewayv1.GroupVersion.String(),
			Kind:       "GatewayClass",
		},
		ObjectMeta: metav1.ObjectMeta{Name: gatewaymeta.GatewayClassName},
		Spec: gatewayv1.GatewayClassSpec{
			ControllerName: controllerName,
		},
	}
}

func testGateway() *gatewayv1.Gateway {
	return testGatewayWithListeners(testTCPListener("tcp", 80))
}

func testGatewayWithListeners(listeners ...gatewayv1.Listener) *gatewayv1.Gateway {
	return &gatewayv1.Gateway{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gatewayv1.GroupVersion.String(),
			Kind:       "Gateway",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "demo",
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(gatewaymeta.GatewayClassName),
			Listeners:        append([]gatewayv1.Listener(nil), listeners...),
		},
	}
}

func testTCPListener(name gatewayv1.SectionName, port gatewayv1.PortNumber) gatewayv1.Listener {
	return gatewayv1.Listener{
		Name:     name,
		Port:     port,
		Protocol: gatewayv1.ProtocolType("TCP"),
	}
}

func testRoute() *gatewayv1alpha2.TCPRoute {
	return testRouteForListener("demo-route", "demo-selector", "", 80)
}

func testRouteForListener(name, selectorName string, listenerName gatewayv1.SectionName, backendPort gatewayv1.PortNumber) *gatewayv1alpha2.TCPRoute {
	group := gatewayv1.Group(endpointSelectorAPIGroup)
	kind := gatewayv1.Kind(endpointSelectorKind)
	parentRef := gatewayv1.ParentReference{
		Name: gatewayv1.ObjectName("demo"),
	}
	if listenerName != "" {
		parentRef.SectionName = ptrTo(listenerName)
	}
	return &gatewayv1alpha2.TCPRoute{
		TypeMeta: metav1.TypeMeta{
			APIVersion: gatewayv1alpha2.GroupVersion.String(),
			Kind:       "TCPRoute",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
		},
		Spec: gatewayv1alpha2.TCPRouteSpec{
			CommonRouteSpec: gatewayv1.CommonRouteSpec{
				ParentRefs: []gatewayv1.ParentReference{parentRef},
			},
			Rules: []gatewayv1alpha2.TCPRouteRule{{
				BackendRefs: []gatewayv1.BackendRef{{
					BackendObjectReference: gatewayv1.BackendObjectReference{
						Group: &group,
						Kind:  &kind,
						Name:  gatewayv1.ObjectName(selectorName),
						Port:  ptrTo(backendPort),
					},
				}},
			}},
		},
	}
}

func testEndpointSelector() *apiv1alpha1.XEndpointSelector {
	return &apiv1alpha1.XEndpointSelector{
		TypeMeta: metav1.TypeMeta{
			APIVersion: apiv1alpha1.SchemeGroupVersion.String(),
			Kind:       endpointSelectorKind,
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "demo-selector",
		},
		Spec: apiv1alpha1.EndpointSelectorSpec{
			Selector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": "demo"},
			},
		},
	}
}

func testServiceCIDR(cidr string) *networkingv1.ServiceCIDR {
	return &networkingv1.ServiceCIDR{
		TypeMeta: metav1.TypeMeta{
			APIVersion: networkingv1.SchemeGroupVersion.String(),
			Kind:       "ServiceCIDR",
		},
		ObjectMeta: metav1.ObjectMeta{Name: "kubernetes"},
		Spec: networkingv1.ServiceCIDRSpec{
			CIDRs: []string{cidr},
		},
	}
}

func testIPAddress(name, namespace, gatewayName string) *networkingv1.IPAddress {
	return &networkingv1.IPAddress{
		TypeMeta: metav1.TypeMeta{
			APIVersion: networkingv1.SchemeGroupVersion.String(),
			Kind:       "IPAddress",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				managedByLabelKey: managedByLabelValue,
				ipFamilyLabelKey:  ipFamilyIPv4Value,
			},
		},
		Spec: networkingv1.IPAddressSpec{
			ParentRef: &networkingv1.ParentReference{
				Group:     gatewayAPIGroup,
				Resource:  "gateways",
				Namespace: namespace,
				Name:      gatewayName,
			},
		},
	}
}

func conditionStatus(conditions []metav1.Condition, conditionType string) metav1.ConditionStatus {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return conditions[i].Status
		}
	}
	return metav1.ConditionUnknown
}

func testConditionReason(conditions []metav1.Condition, conditionType string) string {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return conditions[i].Reason
		}
	}
	return ""
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func hasCondition(conditions []metav1.Condition, conditionType string) bool {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return true
		}
	}
	return false
}

func listenerCondition(t *testing.T, conditions []metav1.Condition, conditionType string) metav1.Condition {
	t.Helper()

	for i := range conditions {
		if conditions[i].Type == conditionType {
			return conditions[i]
		}
	}
	t.Fatalf("condition %q not found in %#v", conditionType, conditions)
	return metav1.Condition{}
}

func listenerStatusByName(t *testing.T, listeners []gatewayv1.ListenerStatus, name gatewayv1.SectionName) gatewayv1.ListenerStatus {
	t.Helper()

	for i := range listeners {
		if listeners[i].Name == name {
			return listeners[i]
		}
	}
	t.Fatalf("listener %q not found in %#v", name, listeners)
	return gatewayv1.ListenerStatus{}
}

func ptrTo[T any](value T) *T {
	return &value
}

func reconcileGatewayToSteadyState(t *testing.T, controller *Controller, gateway *gatewayv1.Gateway) *gatewayv1.Gateway {
	t.Helper()

	key := gateway.Namespace + "/" + gateway.Name
	if err := controller.reconcileGateway(context.Background(), key); err != nil {
		t.Fatalf("first reconcileGateway returned error: %v", err)
	}

	liveGateway, err := controller.gatewayClient.GatewayV1().Gateways(gateway.Namespace).Get(context.Background(), gateway.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gateway after first reconcile: %v", err)
	}
	if !containsString(liveGateway.Finalizers, controllerFinalizer) {
		t.Fatalf("expected finalizer %q to be added, got %v", controllerFinalizer, liveGateway.Finalizers)
	}
	syncGatewayStore(t, controller, liveGateway)

	if err := controller.reconcileGateway(context.Background(), key); err != nil {
		t.Fatalf("second reconcileGateway returned error: %v", err)
	}

	liveGateway, err = controller.gatewayClient.GatewayV1().Gateways(gateway.Namespace).Get(context.Background(), gateway.Name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get gateway after second reconcile: %v", err)
	}
	if len(liveGateway.Status.Addresses) == 1 {
		ipAddress, err := controller.coreClient.NetworkingV1().IPAddresses().Get(context.Background(), liveGateway.Status.Addresses[0].Value, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get allocated IPAddress after steady-state reconcile: %v", err)
		}
		if err := controller.ipAddressInformer.GetStore().Add(ipAddress); err != nil {
			t.Fatalf("add allocated IPAddress to informer store: %v", err)
		}
	}
	return liveGateway
}

func syncGatewayStore(t *testing.T, controller *Controller, gateway *gatewayv1.Gateway) {
	t.Helper()

	if err := controller.gatewayInformer.GetStore().Update(gateway); err != nil {
		t.Fatalf("update gateway informer store: %v", err)
	}
}
