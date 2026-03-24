package controller

import (
	"fmt"
	"strings"

	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func splitNamespacedKey(key string) (types.NamespacedName, error) {
	parts := strings.Split(key, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return types.NamespacedName{}, fmt.Errorf("invalid namespaced key %q", key)
	}
	return types.NamespacedName{Namespace: parts[0], Name: parts[1]}, nil
}

func namespacedKey(key types.NamespacedName) string {
	return key.Namespace + "/" + key.Name
}

func gatewayClassFromObject(obj interface{}) *gatewayv1.GatewayClass {
	switch t := obj.(type) {
	case *gatewayv1.GatewayClass:
		return t
	case cache.DeletedFinalStateUnknown:
		gatewayClass, ok := t.Obj.(*gatewayv1.GatewayClass)
		if !ok {
			panic(fmt.Sprintf("unexpected GatewayClass tombstone object type %T", t.Obj))
		}
		return gatewayClass
	default:
		panic(fmt.Sprintf("unexpected GatewayClass object type %T", obj))
	}
}

func gatewayFromObject(obj interface{}) *gatewayv1.Gateway {
	switch t := obj.(type) {
	case *gatewayv1.Gateway:
		return t
	case cache.DeletedFinalStateUnknown:
		gateway, ok := t.Obj.(*gatewayv1.Gateway)
		if !ok {
			panic(fmt.Sprintf("unexpected Gateway tombstone object type %T", t.Obj))
		}
		return gateway
	default:
		panic(fmt.Sprintf("unexpected Gateway object type %T", obj))
	}
}

func routeFromObject(obj interface{}) *gatewayv1alpha2.TCPRoute {
	switch t := obj.(type) {
	case *gatewayv1alpha2.TCPRoute:
		return t
	case cache.DeletedFinalStateUnknown:
		route, ok := t.Obj.(*gatewayv1alpha2.TCPRoute)
		if !ok {
			panic(fmt.Sprintf("unexpected TCPRoute tombstone object type %T", t.Obj))
		}
		return route
	default:
		panic(fmt.Sprintf("unexpected TCPRoute object type %T", obj))
	}
}

func podFromObject(obj interface{}) *corev1.Pod {
	switch t := obj.(type) {
	case *corev1.Pod:
		return t
	case cache.DeletedFinalStateUnknown:
		pod, ok := t.Obj.(*corev1.Pod)
		if !ok {
			panic(fmt.Sprintf("unexpected Pod tombstone object type %T", t.Obj))
		}
		return pod
	default:
		panic(fmt.Sprintf("unexpected Pod object type %T", obj))
	}
}

func endpointSelectorFromObject(obj interface{}) *unstructured.Unstructured {
	switch t := obj.(type) {
	case *unstructured.Unstructured:
		return t
	case cache.DeletedFinalStateUnknown:
		selector, ok := t.Obj.(*unstructured.Unstructured)
		if !ok {
			panic(fmt.Sprintf("unexpected XEndpointSelector tombstone object type %T", t.Obj))
		}
		return selector
	default:
		panic(fmt.Sprintf("unexpected XEndpointSelector object type %T", obj))
	}
}

func endpointSliceFromObject(obj interface{}) *discoveryv1.EndpointSlice {
	switch t := obj.(type) {
	case *discoveryv1.EndpointSlice:
		return t
	case cache.DeletedFinalStateUnknown:
		endpointSlice, ok := t.Obj.(*discoveryv1.EndpointSlice)
		if !ok {
			panic(fmt.Sprintf("unexpected EndpointSlice tombstone object type %T", t.Obj))
		}
		return endpointSlice
	default:
		panic(fmt.Sprintf("unexpected EndpointSlice object type %T", obj))
	}
}

func ipAddressFromObject(obj interface{}) *networkingv1.IPAddress {
	switch t := obj.(type) {
	case *networkingv1.IPAddress:
		return t
	case cache.DeletedFinalStateUnknown:
		ipAddress, ok := t.Obj.(*networkingv1.IPAddress)
		if !ok {
			panic(fmt.Sprintf("unexpected IPAddress tombstone object type %T", t.Obj))
		}
		return ipAddress
	default:
		panic(fmt.Sprintf("unexpected IPAddress object type %T", obj))
	}
}

func gatewayKeyForIPAddress(ipAddress *networkingv1.IPAddress) string {
	if ipAddress.Labels[managedByLabelKey] != managedByLabelValue {
		return ""
	}
	parent := ipAddress.Spec.ParentRef
	if parent == nil || parent.Group != gatewayAPIGroup || parent.Resource != "gateways" || parent.Namespace == "" || parent.Name == "" {
		return ""
	}
	return namespacedKey(types.NamespacedName{Namespace: parent.Namespace, Name: parent.Name})
}

func indexGatewayByClass(obj interface{}) ([]string, error) {
	gateway, ok := obj.(*gatewayv1.Gateway)
	if !ok {
		panic(fmt.Sprintf("unexpected Gateway index object type %T", obj))
	}
	return []string{string(gateway.Spec.GatewayClassName)}, nil
}

func indexRouteByGateway(obj interface{}) ([]string, error) {
	route, ok := obj.(*gatewayv1alpha2.TCPRoute)
	if !ok {
		panic(fmt.Sprintf("unexpected TCPRoute index object type %T", obj))
	}
	return routeGatewayParentKeys(route), nil
}

func indexRouteByBackendEndpointSelector(obj interface{}) ([]string, error) {
	route, ok := obj.(*gatewayv1alpha2.TCPRoute)
	if !ok {
		panic(fmt.Sprintf("unexpected TCPRoute index object type %T", obj))
	}
	return routeBackendEndpointSelectorKeys(route), nil
}

func indexEndpointSliceByEndpointSelector(obj interface{}) ([]string, error) {
	endpointSlice, ok := obj.(*discoveryv1.EndpointSlice)
	if !ok {
		panic(fmt.Sprintf("unexpected EndpointSlice index object type %T", obj))
	}
	if key := endpointSelectorKeyForEndpointSlice(endpointSlice); key != "" {
		return []string{key}, nil
	}
	return nil, nil
}

func indexIPAddressByGateway(obj interface{}) ([]string, error) {
	ipAddress, ok := obj.(*networkingv1.IPAddress)
	if !ok {
		panic(fmt.Sprintf("unexpected IPAddress index object type %T", obj))
	}
	if key := gatewayKeyForIPAddress(ipAddress); key != "" {
		return []string{key}, nil
	}
	return nil, nil
}

func routeGatewayParentKeys(route *gatewayv1alpha2.TCPRoute) []string {
	keys := make([]string, 0, len(route.Spec.ParentRefs))
	seen := make(map[string]struct{})
	for i := range route.Spec.ParentRefs {
		parent := route.Spec.ParentRefs[i]
		if !isGatewayParentRef(parent) {
			continue
		}
		namespace := route.Namespace
		if parent.Namespace != nil && *parent.Namespace != "" {
			namespace = string(*parent.Namespace)
		}
		key := namespacedKey(types.NamespacedName{Namespace: namespace, Name: string(parent.Name)})
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	return keys
}

func routeBackendEndpointSelectorKeys(route *gatewayv1alpha2.TCPRoute) []string {
	seen := make(map[string]struct{})
	keys := make([]string, 0)
	for i := range route.Spec.Rules {
		rule := route.Spec.Rules[i]
		for j := range rule.BackendRefs {
			backend := rule.BackendRefs[j]
			if !isEndpointSelectorBackendRef(route.Namespace, backend.BackendObjectReference) {
				continue
			}
			namespace := route.Namespace
			if backend.Namespace != nil && *backend.Namespace != "" {
				namespace = string(*backend.Namespace)
			}
			key := namespacedKey(types.NamespacedName{Namespace: namespace, Name: string(backend.Name)})
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}
	return keys
}

func isGatewayParentRef(parent gatewayv1.ParentReference) bool {
	group := gatewayAPIGroup
	if parent.Group != nil && *parent.Group != "" {
		group = string(*parent.Group)
	}
	kind := "Gateway"
	if parent.Kind != nil && *parent.Kind != "" {
		kind = string(*parent.Kind)
	}
	return group == gatewayAPIGroup && kind == "Gateway"
}

func isEndpointSelectorBackendRef(routeNamespace string, backend gatewayv1.BackendObjectReference) bool {
	if backend.Group == nil || *backend.Group == "" {
		return false
	}
	group := string(*backend.Group)
	kind := endpointSelectorKind
	if backend.Kind != nil && *backend.Kind != "" {
		kind = string(*backend.Kind)
	}
	if group != endpointSelectorAPIGroup || kind != endpointSelectorKind {
		return false
	}
	namespace := routeNamespace
	if backend.Namespace != nil && *backend.Namespace != "" {
		namespace = string(*backend.Namespace)
	}
	return namespace != "" && backend.Name != ""
}

func endpointSelectorKeyForEndpointSlice(endpointSlice *discoveryv1.EndpointSlice) string {
	if endpointSlice.Labels[discoveryv1.LabelManagedBy] != gatewaymeta.ManagedByValue {
		return ""
	}
	selectorName := endpointSlice.Labels[gatewaymeta.EndpointSelectorNameLabelKey]
	if selectorName == "" {
		return ""
	}
	namespace := endpointSlice.Labels[gatewaymeta.EndpointSelectorNamespaceLabelKey]
	if namespace == "" {
		namespace = endpointSlice.Namespace
	}
	if namespace == "" {
		return ""
	}
	return namespacedKey(types.NamespacedName{Namespace: namespace, Name: selectorName})
}

func getGateway(c *Controller, key string) (*gatewayv1.Gateway, error) {
	namespacedName, err := splitNamespacedKey(key)
	if err != nil {
		return nil, err
	}
	return c.gateways.Gateways(namespacedName.Namespace).Get(namespacedName.Name)
}

func getRoute(c *Controller, key string) (*gatewayv1alpha2.TCPRoute, error) {
	namespacedName, err := splitNamespacedKey(key)
	if err != nil {
		return nil, err
	}
	return c.routes.TCPRoutes(namespacedName.Namespace).Get(namespacedName.Name)
}

func isNotFound(err error) bool {
	return err != nil && apierrors.IsNotFound(err)
}
