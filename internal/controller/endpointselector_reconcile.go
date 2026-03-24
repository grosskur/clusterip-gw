package controller

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"maps"
	"net"
	"sort"

	apiv1alpha1 "github.com/grosskur/clusterip-gw/apis/v1alpha1"
	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func (c *Controller) reconcileEndpointSelector(ctx context.Context, key string) error {
	namespacedName, err := splitNamespacedKey(key)
	if err != nil {
		return err
	}

	selector, err := c.getEndpointSelector(namespacedName)
	if err != nil {
		if isNotFound(err) {
			return c.deleteManagedEndpointSlices(ctx, namespacedName)
		}
		return err
	}

	acceptedStatus, acceptedReason, acceptedMessage := evaluateEndpointSelector(selector)
	if err := c.updateEndpointSelectorStatus(ctx, selector, acceptedStatus, acceptedReason, acceptedMessage); err != nil {
		return err
	}
	if acceptedStatus != metav1.ConditionTrue {
		return c.deleteManagedEndpointSlices(ctx, namespacedName)
	}

	bindings, err := c.listReferencedGatewayBindings(selector)
	if err != nil {
		return err
	}
	if len(bindings) == 0 {
		return c.deleteManagedEndpointSlices(ctx, namespacedName)
	}

	endpoints, err := c.buildEndpointSliceEndpoints(selector)
	if err != nil {
		return err
	}

	desired := make(map[string]*discoveryv1.EndpointSlice, len(bindings))
	for _, binding := range bindings {
		slice := desiredEndpointSlice(selector, binding.gateway, binding.listenerName, binding.backendPort, endpoints)
		desired[slice.Name] = slice
	}

	existing, err := c.listManagedEndpointSlices(namespacedName)
	if err != nil {
		return err
	}

	for _, endpointSlice := range existing {
		desiredSlice, ok := desired[endpointSlice.Name]
		if !ok {
			if err := c.coreClient.DiscoveryV1().EndpointSlices(endpointSlice.Namespace).Delete(ctx, endpointSlice.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			continue
		}

		if endpointSliceEqual(endpointSlice, desiredSlice) {
			delete(desired, endpointSlice.Name)
			continue
		}

		updated := endpointSlice.DeepCopy()
		updated.Labels = maps.Clone(desiredSlice.Labels)
		updated.OwnerReferences = append([]metav1.OwnerReference(nil), desiredSlice.OwnerReferences...)
		updated.AddressType = desiredSlice.AddressType
		updated.Ports = append([]discoveryv1.EndpointPort(nil), desiredSlice.Ports...)
		updated.Endpoints = append([]discoveryv1.Endpoint(nil), desiredSlice.Endpoints...)

		if _, err := c.coreClient.DiscoveryV1().EndpointSlices(updated.Namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
			return err
		}
		delete(desired, endpointSlice.Name)
	}

	for _, endpointSlice := range desired {
		if _, err := c.coreClient.DiscoveryV1().EndpointSlices(endpointSlice.Namespace).Create(ctx, endpointSlice, metav1.CreateOptions{}); err != nil {
			return err
		}
	}

	return nil
}

func evaluateEndpointSelector(selector *apiv1alpha1.XEndpointSelector) (metav1.ConditionStatus, string, string) {
	if _, err := metav1.LabelSelectorAsSelector(&selector.Spec.Selector); err != nil {
		return metav1.ConditionFalse, apiv1alpha1.EndpointSelectorReasonInvalid, fmt.Sprintf("Spec.selector is invalid: %v", err)
	}

	return metav1.ConditionTrue, apiv1alpha1.EndpointSelectorReasonAccepted, "XEndpointSelector accepted by clusterip-gw-controller."
}

func (c *Controller) getEndpointSelector(key types.NamespacedName) (*apiv1alpha1.XEndpointSelector, error) {
	obj, exists, err := c.endpointSelectorInformer.GetStore().GetByKey(namespacedKey(key))
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, apierrors.NewNotFound(apiv1alpha1.Resource("xendpointselectors"), key.Name)
	}
	return endpointSelectorFromUnstructured(endpointSelectorFromObject(obj))
}

func endpointSelectorFromUnstructured(obj *unstructured.Unstructured) (*apiv1alpha1.XEndpointSelector, error) {
	out := &apiv1alpha1.XEndpointSelector{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, out); err != nil {
		return nil, err
	}
	return out, nil
}

func endpointSelectorToUnstructured(selector *apiv1alpha1.XEndpointSelector) (*unstructured.Unstructured, error) {
	selector = selector.DeepCopy()
	if selector.APIVersion == "" {
		selector.APIVersion = apiv1alpha1.SchemeGroupVersion.String()
	}
	if selector.Kind == "" {
		selector.Kind = endpointSelectorKind
	}

	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(selector)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: obj}, nil
}

func (c *Controller) updateEndpointSelectorStatus(
	ctx context.Context,
	selector *apiv1alpha1.XEndpointSelector,
	status metav1.ConditionStatus,
	reason, message string,
) error {
	updated := selector.DeepCopy()
	updated.Status.Conditions = mergeConditions(nil, condition(
		apiv1alpha1.EndpointSelectorConditionAccepted,
		status,
		reason,
		message,
		selector.Generation,
	))

	if endpointSelectorStatusEqual(selector, updated) {
		return nil
	}

	unstructuredSelector, err := endpointSelectorToUnstructured(updated)
	if err != nil {
		return err
	}

	_, err = c.dynamicClient.Resource(endpointSelectorGVR).Namespace(updated.Namespace).UpdateStatus(ctx, unstructuredSelector, metav1.UpdateOptions{})
	return err
}

func endpointSelectorStatusEqual(oldObj, newObj *apiv1alpha1.XEndpointSelector) bool {
	return conditionsEqual(oldObj.Status.Conditions, newObj.Status.Conditions)
}

type gatewayBinding struct {
	gateway      *gatewayv1.Gateway
	listenerName string
	backendPort  int32
}

func (c *Controller) listReferencedGatewayBindings(selector *apiv1alpha1.XEndpointSelector) ([]gatewayBinding, error) {
	items, err := c.routeInformer.GetIndexer().ByIndex(routeByBackendEndpointSelectorIndex, namespacedKey(types.NamespacedName{
		Namespace: selector.Namespace,
		Name:      selector.Name,
	}))
	if err != nil {
		return nil, err
	}

	seen := sets.New[string]()
	bindings := make([]gatewayBinding, 0, len(items))
	for _, item := range items {
		route := routeFromObject(item)
		if route == nil {
			continue
		}

		binding, used, err := c.gatewayBindingForRoute(selector, route)
		if err != nil {
			return nil, err
		}
		if !used {
			continue
		}
		key := namespacedKey(types.NamespacedName{
			Namespace: binding.gateway.Namespace,
			Name:      binding.gateway.Name,
		}) + ":" + binding.listenerName
		if seen.Has(key) {
			continue
		}
		seen.Insert(key)
		bindings = append(bindings, binding)
	}

	sort.Slice(bindings, func(i, j int) bool {
		left := namespacedKey(types.NamespacedName{
			Namespace: bindings[i].gateway.Namespace,
			Name:      bindings[i].gateway.Name,
		}) + ":" + bindings[i].listenerName
		right := namespacedKey(types.NamespacedName{
			Namespace: bindings[j].gateway.Namespace,
			Name:      bindings[j].gateway.Name,
		}) + ":" + bindings[j].listenerName
		return left < right
	})
	return bindings, nil
}

func (c *Controller) gatewayBindingForRoute(selector *apiv1alpha1.XEndpointSelector, route *gatewayv1alpha2.TCPRoute) (gatewayBinding, bool, error) {
	if len(route.Spec.Rules) != 1 || len(route.Spec.Rules[0].BackendRefs) != 1 {
		return gatewayBinding{}, false, nil
	}

	backend := route.Spec.Rules[0].BackendRefs[0]
	if !isEndpointSelectorBackendRef(route.Namespace, backend.BackendObjectReference) {
		return gatewayBinding{}, false, nil
	}

	backendNamespace := route.Namespace
	if backend.Namespace != nil && *backend.Namespace != "" {
		backendNamespace = string(*backend.Namespace)
	}
	if backendNamespace != selector.Namespace || string(backend.Name) != selector.Name || backend.Port == nil {
		return gatewayBinding{}, false, nil
	}

	for i := range route.Spec.ParentRefs {
		parentRef := route.Spec.ParentRefs[i]
		if !isGatewayParentRef(parentRef) {
			continue
		}

		gatewayNamespace := route.Namespace
		if parentRef.Namespace != nil && *parentRef.Namespace != "" {
			gatewayNamespace = string(*parentRef.Namespace)
		}
		gateway, err := c.gateways.Gateways(gatewayNamespace).Get(string(parentRef.Name))
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return gatewayBinding{}, false, err
		}
		if gateway.Spec.GatewayClassName != gatewayv1.ObjectName(gatewaymeta.GatewayClassName) {
			continue
		}

		gatewayClass, err := c.gatewayClasses.Get(gatewaymeta.GatewayClassName)
		if err != nil && !apierrors.IsNotFound(err) {
			return gatewayBinding{}, false, err
		}

		attachedRoutes, err := c.listRoutesForGateway(types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name})
		if err != nil {
			return gatewayBinding{}, false, err
		}

		evaluation := c.evaluateRoute(route, parentRef, gateway, gatewayClass, attachedRoutes)
		if evaluation.acceptedStatus != metav1.ConditionTrue || evaluation.resolvedStatus != metav1.ConditionTrue {
			continue
		}

		listener, err := resolveGatewayListener(gateway, parentRef)
		if err != nil {
			continue
		}

		return gatewayBinding{
			gateway:      gateway,
			listenerName: string(listener.Name),
			backendPort:  int32(*backend.Port),
		}, true, nil
	}

	return gatewayBinding{}, false, nil
}

func (c *Controller) buildEndpointSliceEndpoints(selector *apiv1alpha1.XEndpointSelector) ([]discoveryv1.Endpoint, error) {
	labelSelector, err := metav1.LabelSelectorAsSelector(&selector.Spec.Selector)
	if err != nil {
		return nil, err
	}

	pods, err := c.pods.Pods(selector.Namespace).List(labels.Everything())
	if err != nil {
		return nil, err
	}

	endpoints := make([]discoveryv1.Endpoint, 0)
	for _, pod := range pods {
		if !labelSelector.Matches(labels.Set(pod.Labels)) {
			continue
		}
		endpoint, ok := endpointForPod(pod)
		if !ok {
			continue
		}
		endpoints = append(endpoints, endpoint)
	}

	sort.Slice(endpoints, func(i, j int) bool {
		a := endpoints[i].Addresses[0]
		b := endpoints[j].Addresses[0]
		if a == b && endpoints[i].TargetRef != nil && endpoints[j].TargetRef != nil {
			return endpoints[i].TargetRef.Name < endpoints[j].TargetRef.Name
		}
		return a < b
	})
	return endpoints, nil
}

func endpointForPod(pod *corev1.Pod) (discoveryv1.Endpoint, bool) {
	if pod.Namespace == "" || pod.Name == "" || pod.DeletionTimestamp != nil {
		return discoveryv1.Endpoint{}, false
	}
	if !podReady(pod) || !isIPv4String(pod.Status.PodIP) {
		return discoveryv1.Endpoint{}, false
	}

	ready := true
	endpoint := discoveryv1.Endpoint{
		Addresses: []string{pod.Status.PodIP},
		Conditions: discoveryv1.EndpointConditions{
			Ready: &ready,
		},
		TargetRef: &corev1.ObjectReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  pod.Namespace,
			Name:       pod.Name,
			UID:        pod.UID,
		},
	}
	if pod.Spec.NodeName != "" {
		endpoint.NodeName = &pod.Spec.NodeName
	}
	return endpoint, true
}

func podReady(pod *corev1.Pod) bool {
	for i := range pod.Status.Conditions {
		condition := pod.Status.Conditions[i]
		if condition.Type == corev1.PodReady {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return false
}

func isIPv4String(value string) bool {
	return net.ParseIP(value).To4() != nil
}

func desiredEndpointSlice(
	selector *apiv1alpha1.XEndpointSelector,
	gateway *gatewayv1.Gateway,
	listenerName string,
	port int32,
	endpoints []discoveryv1.Endpoint,
) *discoveryv1.EndpointSlice {
	protocol := corev1.ProtocolTCP
	addressType := discoveryv1.AddressTypeIPv4

	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: gateway.Namespace,
			Name:      endpointSliceName(gateway.Name, listenerName),
			Labels: map[string]string{
				discoveryv1.LabelManagedBy:                    gatewaymeta.ManagedByValue,
				gatewaymeta.EndpointSelectorNamespaceLabelKey: selector.Namespace,
				gatewaymeta.EndpointSelectorNameLabelKey:      selector.Name,
				gatewaymeta.GatewayNamespaceLabelKey:          gateway.Namespace,
				gatewaymeta.GatewayNameLabelKey:               gateway.Name,
				gatewaymeta.GatewayListenerLabelKey:           listenerName,
			},
			OwnerReferences: []metav1.OwnerReference{
				*metav1.NewControllerRef(gateway, schema.GroupVersion{
					Group:   gatewayv1.GroupVersion.Group,
					Version: gatewayv1.GroupVersion.Version,
				}.WithKind("Gateway")),
			},
		},
		AddressType: addressType,
		Ports: []discoveryv1.EndpointPort{{
			Protocol: &protocol,
			Port:     &port,
		}},
		Endpoints: append([]discoveryv1.Endpoint(nil), endpoints...),
	}
}

func endpointSliceName(gatewayName, listenerName string) string {
	name := gatewayName
	if listenerName != "" {
		name += "-" + listenerName
	}
	if len(name) <= 63 {
		return name
	}

	sum := sha1.Sum([]byte(name))
	suffix := "-" + hex.EncodeToString(sum[:4])
	maxPrefixLen := 63 - len(suffix)
	return name[:maxPrefixLen] + suffix
}

func (c *Controller) listManagedEndpointSlices(key types.NamespacedName) ([]*discoveryv1.EndpointSlice, error) {
	items, err := c.endpointSliceInformer.GetIndexer().ByIndex(endpointSliceByEndpointSelectorIndex, namespacedKey(key))
	if err != nil {
		return nil, err
	}

	out := make([]*discoveryv1.EndpointSlice, 0, len(items))
	for _, item := range items {
		out = append(out, endpointSliceFromObject(item))
	}
	return out, nil
}

func (c *Controller) deleteManagedEndpointSlices(ctx context.Context, key types.NamespacedName) error {
	existing, err := c.listManagedEndpointSlices(key)
	if err != nil {
		return err
	}
	for _, endpointSlice := range existing {
		if err := c.coreClient.DiscoveryV1().EndpointSlices(endpointSlice.Namespace).Delete(ctx, endpointSlice.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func endpointSliceEqual(a, b *discoveryv1.EndpointSlice) bool {
	return apiequality.Semantic.DeepEqual(a.Labels, b.Labels) &&
		apiequality.Semantic.DeepEqual(a.OwnerReferences, b.OwnerReferences) &&
		a.AddressType == b.AddressType &&
		apiequality.Semantic.DeepEqual(a.Ports, b.Ports) &&
		apiequality.Semantic.DeepEqual(a.Endpoints, b.Endpoints)
}
