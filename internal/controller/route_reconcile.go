package controller

import (
	"context"
	"fmt"
	"slices"

	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func (c *Controller) reconcileRoute(ctx context.Context, key string) error {
	route, err := getRoute(c, key)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}

	desired := make([]gatewayv1.RouteParentStatus, 0, len(route.Spec.ParentRefs))
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
			return err
		}
		if gateway.Spec.GatewayClassName != gatewayv1.ObjectName(gatewaymeta.GatewayClassName) {
			continue
		}

		gatewayClass, err := c.gatewayClasses.Get(gatewaymeta.GatewayClassName)
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}

		attachedRoutes, err := c.listRoutesForGateway(types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name})
		if err != nil {
			return err
		}

		evaluation := c.evaluateRoute(route, parentRef, gateway, gatewayClass, attachedRoutes)
		desired = append(desired, gatewayv1.RouteParentStatus{
			ParentRef:      parentRef,
			ControllerName: controllerName,
			Conditions: mergeConditions(nil,
				condition(string(gatewayv1.RouteConditionAccepted), evaluation.acceptedStatus, evaluation.acceptedReason, evaluation.acceptedMessage, route.Generation),
				condition(string(gatewayv1.RouteConditionResolvedRefs), evaluation.resolvedStatus, evaluation.resolvedReason, evaluation.resolvedMessage, route.Generation),
			),
		})
	}

	updated := route.DeepCopy()
	updated.Status.Parents = mergeRouteParents(updated.Status.Parents, desired)
	if routeStatusEqual(route, updated) {
		return nil
	}

	_, err = c.gatewayClient.GatewayV1alpha2().TCPRoutes(route.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) evaluateRoute(
	route *gatewayv1alpha2.TCPRoute,
	parentRef gatewayv1.ParentReference,
	gateway *gatewayv1.Gateway,
	gatewayClass *gatewayv1.GatewayClass,
	attachedRoutes []*gatewayv1alpha2.TCPRoute,
) routeEvaluation {
	evaluation := routeEvaluation{
		acceptedStatus:  metav1.ConditionFalse,
		acceptedReason:  string(gatewayv1.RouteReasonPending),
		acceptedMessage: "Waiting for Gateway reconciliation.",
		resolvedStatus:  metav1.ConditionTrue,
		resolvedReason:  string(gatewayv1.RouteReasonResolvedRefs),
		resolvedMessage: "Backend references resolved.",
	}

	if gatewayClass == nil || gatewayClass.Spec.ControllerName != controllerName {
		evaluation.acceptedMessage = fmt.Sprintf("GatewayClass %q is not accepted by this controller.", gatewaymeta.GatewayClassName)
		return evaluation
	}

	if route.Namespace != gateway.Namespace {
		evaluation.acceptedReason = string(gatewayv1.RouteReasonNotAllowedByListeners)
		evaluation.acceptedMessage = "Cross-namespace TCPRoute attachment is not supported in this phase."
	} else if len(route.Spec.ParentRefs) != 1 {
		evaluation.acceptedReason = string(gatewayv1.RouteReasonUnsupportedValue)
		evaluation.acceptedMessage = "Exactly one parentRef is supported."
	} else {
		listener, err := resolveGatewayListener(gateway, parentRef)
		if err != nil {
			evaluation.acceptedReason = string(gatewayv1.RouteReasonUnsupportedValue)
			evaluation.acceptedMessage = err.Error()
		} else if countAttachedRoutesForListener(gateway, listener, attachedRoutes) != 1 {
			evaluation.acceptedReason = string(gatewayv1.RouteReasonUnsupportedValue)
			evaluation.acceptedMessage = fmt.Sprintf("Listener %q supports exactly one attached TCPRoute.", listener.Name)
		} else {
			evaluation.acceptedStatus = metav1.ConditionTrue
			evaluation.acceptedReason = string(gatewayv1.RouteReasonAccepted)
			evaluation.acceptedMessage = "TCPRoute accepted by clusterip-gw-controller."
		}
	}

	if len(route.Spec.Rules) != 1 {
		evaluation.resolvedStatus = metav1.ConditionFalse
		evaluation.resolvedReason = string(gatewayv1.RouteReasonUnsupportedValue)
		evaluation.resolvedMessage = "Exactly one TCPRoute rule is supported."
		return evaluation
	}

	if len(route.Spec.Rules[0].BackendRefs) != 1 {
		evaluation.resolvedStatus = metav1.ConditionFalse
		evaluation.resolvedReason = string(gatewayv1.RouteReasonUnsupportedValue)
		evaluation.resolvedMessage = "Exactly one backendRef is supported."
		return evaluation
	}

	backend := route.Spec.Rules[0].BackendRefs[0]
	group := ""
	if backend.Group != nil {
		group = string(*backend.Group)
	}
	kind := endpointSelectorKind
	if backend.Kind != nil && *backend.Kind != "" {
		kind = string(*backend.Kind)
	}
	if group != endpointSelectorAPIGroup || kind != endpointSelectorKind {
		evaluation.resolvedStatus = metav1.ConditionFalse
		evaluation.resolvedReason = string(gatewayv1.RouteReasonInvalidKind)
		evaluation.resolvedMessage = "Only gateway.networking.x-k8s.io XEndpointSelector backendRefs are supported."
		return evaluation
	}

	backendNamespace := route.Namespace
	if backend.Namespace != nil && *backend.Namespace != "" {
		backendNamespace = string(*backend.Namespace)
	}
	if backendNamespace != route.Namespace {
		evaluation.resolvedStatus = metav1.ConditionFalse
		evaluation.resolvedReason = string(gatewayv1.RouteReasonRefNotPermitted)
		evaluation.resolvedMessage = "Cross-namespace XEndpointSelector backendRefs are not supported in this phase."
		return evaluation
	}
	if backend.Port == nil {
		evaluation.resolvedStatus = metav1.ConditionFalse
		evaluation.resolvedReason = string(gatewayv1.RouteReasonBackendNotFound)
		evaluation.resolvedMessage = "XEndpointSelector backendRefs must set port."
		return evaluation
	}

	selector, err := c.getEndpointSelector(types.NamespacedName{Namespace: backendNamespace, Name: string(backend.Name)})
	if err != nil {
		if apierrors.IsNotFound(err) {
			evaluation.resolvedStatus = metav1.ConditionFalse
			evaluation.resolvedReason = string(gatewayv1.RouteReasonBackendNotFound)
			evaluation.resolvedMessage = fmt.Sprintf("Backend XEndpointSelector %s/%s was not found.", backendNamespace, backend.Name)
			return evaluation
		}
		evaluation.resolvedStatus = metav1.ConditionFalse
		evaluation.resolvedReason = string(gatewayv1.RouteReasonBackendNotFound)
		evaluation.resolvedMessage = err.Error()
		return evaluation
	}

	acceptedStatus, acceptedReason, acceptedMessage := evaluateEndpointSelector(selector)
	if acceptedStatus != metav1.ConditionTrue {
		evaluation.resolvedStatus = metav1.ConditionFalse
		evaluation.resolvedReason = string(gatewayv1.RouteReasonBackendNotFound)
		evaluation.resolvedMessage = fmt.Sprintf("Backend XEndpointSelector %s/%s is not accepted: %s: %s", backendNamespace, backend.Name, acceptedReason, acceptedMessage)
		return evaluation
	}

	return evaluation
}

func matchingParentRef(route *gatewayv1alpha2.TCPRoute, gateway *gatewayv1.Gateway) gatewayv1.ParentReference {
	for i := range route.Spec.ParentRefs {
		parent := route.Spec.ParentRefs[i]
		if !isGatewayParentRef(parent) {
			continue
		}
		namespace := route.Namespace
		if parent.Namespace != nil && *parent.Namespace != "" {
			namespace = string(*parent.Namespace)
		}
		if namespace == gateway.Namespace && string(parent.Name) == gateway.Name {
			return parent
		}
	}
	return gatewayv1.ParentReference{Name: gatewayv1.ObjectName(gateway.Name)}
}

func resolveGatewayListener(gateway *gatewayv1.Gateway, parent gatewayv1.ParentReference) (gatewayv1.Listener, error) {
	validation := validateGatewayListeners(gateway)
	if !validation.valid {
		return gatewayv1.Listener{}, fmt.Errorf("%s", validation.message)
	}

	matches := make([]gatewayv1.Listener, 0, len(validation.listeners))
	for i := range validation.listeners {
		listener := validation.listeners[i]
		if parentMatchesListener(parent, listener) {
			matches = append(matches, listener)
		}
	}

	switch len(matches) {
	case 0:
		return gatewayv1.Listener{}, fmt.Errorf("ParentRef does not select a Gateway listener")
	case 1:
		return matches[0], nil
	default:
		return gatewayv1.Listener{}, fmt.Errorf("ParentRef must select exactly one Gateway listener by sectionName or port")
	}
}

func parentMatchesListener(parent gatewayv1.ParentReference, listener gatewayv1.Listener) bool {
	if parent.SectionName != nil && *parent.SectionName != listener.Name {
		return false
	}
	if parent.Port != nil && *parent.Port != listener.Port {
		return false
	}
	return true
}

func attachedRoutesForListener(gateway *gatewayv1.Gateway, listener gatewayv1.Listener, routes []*gatewayv1alpha2.TCPRoute) []*gatewayv1alpha2.TCPRoute {
	out := make([]*gatewayv1alpha2.TCPRoute, 0, len(routes))
	for _, route := range routes {
		if route.Namespace != gateway.Namespace {
			continue
		}
		for i := range route.Spec.ParentRefs {
			parent := route.Spec.ParentRefs[i]
			if !isGatewayParentRef(parent) {
				continue
			}
			namespace := route.Namespace
			if parent.Namespace != nil && *parent.Namespace != "" {
				namespace = string(*parent.Namespace)
			}
			if namespace != gateway.Namespace || string(parent.Name) != gateway.Name {
				continue
			}
			if !parentMatchesListener(parent, listener) {
				continue
			}
			out = append(out, route)
			break
		}
	}
	slices.SortFunc(out, func(a, b *gatewayv1alpha2.TCPRoute) int {
		if a.Namespace == b.Namespace {
			switch {
			case a.Name < b.Name:
				return -1
			case a.Name > b.Name:
				return 1
			default:
				return 0
			}
		}
		if a.Namespace < b.Namespace {
			return -1
		}
		return 1
	})
	return out
}

func countAttachedRoutesForListener(gateway *gatewayv1.Gateway, listener gatewayv1.Listener, routes []*gatewayv1alpha2.TCPRoute) int {
	return len(attachedRoutesForListener(gateway, listener, routes))
}

func (c *Controller) listRoutesForGateway(key types.NamespacedName) ([]*gatewayv1alpha2.TCPRoute, error) {
	items, err := c.routeInformer.GetIndexer().ByIndex(routeByGatewayIndex, namespacedKey(key))
	if err != nil {
		return nil, err
	}
	out := make([]*gatewayv1alpha2.TCPRoute, 0, len(items))
	for _, item := range items {
		route, ok := item.(*gatewayv1alpha2.TCPRoute)
		if !ok {
			continue
		}
		out = append(out, route)
	}
	return out, nil
}
