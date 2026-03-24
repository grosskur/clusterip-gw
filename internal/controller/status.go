package controller

import (
	"context"
	"fmt"
	"sort"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func (c *Controller) updateGatewayStatus(ctx context.Context, gateway *gatewayv1.Gateway, evaluation gatewayEvaluation) error {
	updated := gateway.DeepCopy()
	if evaluation.selectedVIP != "" {
		addressType := gatewayv1.AddressType("IPAddress")
		updated.Status.Addresses = []gatewayv1.GatewayStatusAddress{{
			Type:  &addressType,
			Value: evaluation.selectedVIP,
		}}
	} else {
		updated.Status.Addresses = nil
	}
	updated.Status.Conditions = mergeConditions(updated.Status.Conditions,
		condition(string(gatewayv1.GatewayConditionAccepted), evaluation.acceptedStatus, evaluation.acceptedReason, evaluation.acceptedMessage, gateway.Generation),
		condition(string(gatewayv1.GatewayConditionProgrammed), metav1.ConditionFalse, evaluation.programmedReason, evaluation.programmedMessage, gateway.Generation),
	)
	if len(evaluation.listeners) > 0 {
		updated.Status.Listeners = evaluation.listeners
	} else {
		updated.Status.Listeners = nil
	}

	if gatewayStatusEqual(gateway, updated) {
		return nil
	}

	_, err := c.gatewayClient.GatewayV1().Gateways(updated.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	return err
}

func mergeConditions(existing []metav1.Condition, desired ...metav1.Condition) []metav1.Condition {
	out := make([]metav1.Condition, len(existing))
	copy(out, existing)
	for _, c := range desired {
		apimeta.SetStatusCondition(&out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Type < out[j].Type
	})
	return out
}

func removeConditions(existing []metav1.Condition, conditionTypes ...string) []metav1.Condition {
	if len(existing) == 0 {
		return nil
	}

	remove := sets.New[string](conditionTypes...)
	out := make([]metav1.Condition, 0, len(existing))
	for _, condition := range existing {
		if remove.Has(condition.Type) {
			continue
		}
		out = append(out, condition)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func condition(conditionType string, status metav1.ConditionStatus, reason, message string, observedGeneration int64) metav1.Condition {
	return metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: observedGeneration,
		LastTransitionTime: metav1.Now(),
	}
}

func boolToConditionStatus(value bool) metav1.ConditionStatus {
	if value {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}

func conditionReason(condition bool, trueReason, falseReason string) string {
	if condition {
		return trueReason
	}
	return falseReason
}

func gatewayClassStatusEqual(oldObj, newObj *gatewayv1.GatewayClass) bool {
	return conditionsEqual(oldObj.Status.Conditions, newObj.Status.Conditions)
}

func gatewayStatusEqual(oldObj, newObj *gatewayv1.Gateway) bool {
	return conditionsEqual(oldObj.Status.Conditions, newObj.Status.Conditions) &&
		gatewayAddressesEqual(oldObj.Status.Addresses, newObj.Status.Addresses) &&
		listenerStatusesEqual(oldObj.Status.Listeners, newObj.Status.Listeners)
}

func routeStatusEqual(oldObj, newObj *gatewayv1alpha2.TCPRoute) bool {
	return routeParentsEqual(oldObj.Status.Parents, newObj.Status.Parents)
}

func conditionsEqual(a, b []metav1.Condition) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Type != b[i].Type ||
			a[i].Status != b[i].Status ||
			a[i].Reason != b[i].Reason ||
			a[i].Message != b[i].Message ||
			a[i].ObservedGeneration != b[i].ObservedGeneration {
			return false
		}
	}
	return true
}

func gatewayAddressesEqual(a, b []gatewayv1.GatewayStatusAddress) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Value != b[i].Value {
			return false
		}
		typeA := ""
		if a[i].Type != nil {
			typeA = string(*a[i].Type)
		}
		typeB := ""
		if b[i].Type != nil {
			typeB = string(*b[i].Type)
		}
		if typeA != typeB {
			return false
		}
	}
	return true
}

func listenerStatusesEqual(a, b []gatewayv1.ListenerStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].AttachedRoutes != b[i].AttachedRoutes {
			return false
		}
		if len(a[i].SupportedKinds) != len(b[i].SupportedKinds) {
			return false
		}
		for j := range a[i].SupportedKinds {
			if a[i].SupportedKinds[j].Kind != b[i].SupportedKinds[j].Kind {
				return false
			}
		}
		if !conditionsEqual(a[i].Conditions, b[i].Conditions) {
			return false
		}
	}
	return true
}

func mergeRouteParents(existing, desired []gatewayv1.RouteParentStatus) []gatewayv1.RouteParentStatus {
	out := make([]gatewayv1.RouteParentStatus, 0, len(existing)+len(desired))
	for _, parent := range existing {
		if parent.ControllerName == controllerName {
			continue
		}
		out = append(out, parent)
	}
	out = append(out, desired...)
	sort.Slice(out, func(i, j int) bool {
		return routeParentSortKey(out[i]) < routeParentSortKey(out[j])
	})
	return out
}

func routeParentsEqual(a, b []gatewayv1.RouteParentStatus) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if routeParentSortKey(a[i]) != routeParentSortKey(b[i]) {
			return false
		}
		if !conditionsEqual(a[i].Conditions, b[i].Conditions) {
			return false
		}
	}
	return true
}

func routeParentSortKey(parent gatewayv1.RouteParentStatus) string {
	group := ""
	if parent.ParentRef.Group != nil {
		group = string(*parent.ParentRef.Group)
	}
	kind := ""
	if parent.ParentRef.Kind != nil {
		kind = string(*parent.ParentRef.Kind)
	}
	namespace := ""
	if parent.ParentRef.Namespace != nil {
		namespace = string(*parent.ParentRef.Namespace)
	}
	sectionName := ""
	if parent.ParentRef.SectionName != nil {
		sectionName = string(*parent.ParentRef.SectionName)
	}
	port := ""
	if parent.ParentRef.Port != nil {
		port = fmt.Sprintf("%d", *parent.ParentRef.Port)
	}
	return fmt.Sprintf("%s/%s/%s/%s/%s/%s/%s", parent.ControllerName, group, kind, namespace, parent.ParentRef.Name, sectionName, port)
}
