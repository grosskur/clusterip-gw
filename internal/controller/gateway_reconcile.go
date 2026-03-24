package controller

import (
	"context"
	"fmt"

	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func (c *Controller) reconcileGatewayClass(ctx context.Context, key string) error {
	gatewayClass, err := c.gatewayClasses.Get(key)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}

	if !shouldManageGatewayClass(gatewayClass) {
		return nil
	}

	accepted := gatewayClass.Name == gatewaymeta.GatewayClassName && gatewayClass.Spec.ControllerName == controllerName
	updated := gatewayClass.DeepCopy()
	updated.Status.Conditions = mergeConditions(updated.Status.Conditions, condition(
		string(gatewayv1.GatewayClassConditionStatusAccepted),
		boolToConditionStatus(accepted),
		conditionReason(accepted, string(gatewayv1.GatewayClassReasonAccepted), string(gatewayv1.GatewayClassReasonUnsupported)),
		gatewayClassAcceptedMessage(gatewayClass, accepted),
		gatewayClass.Generation,
	))
	if accepted {
		updated.Status.Conditions = mergeConditions(updated.Status.Conditions, condition(
			string(gatewayv1.GatewayClassConditionStatusSupportedVersion),
			metav1.ConditionTrue,
			string(gatewayv1.GatewayClassReasonSupportedVersion),
			"Gateway API versions used by this controller are supported.",
			gatewayClass.Generation,
		))
	}

	if gatewayClassStatusEqual(gatewayClass, updated) {
		return nil
	}

	_, err = c.gatewayClient.GatewayV1().GatewayClasses().UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) reconcileGateway(ctx context.Context, key string) error {
	gateway, err := getGateway(c, key)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return err
	}

	if gateway.Spec.GatewayClassName != gatewayv1.ObjectName(gatewaymeta.GatewayClassName) {
		previouslyManaged, err := c.gatewayPreviouslyManaged(gateway)
		if err != nil {
			return err
		}
		if previouslyManaged {
			if err := c.clearManagedGatewayStatus(ctx, gateway); err != nil {
				return err
			}
		}
		return c.cleanupGatewayResources(ctx, gateway)
	}

	if gateway.DeletionTimestamp != nil {
		return c.cleanupGatewayResources(ctx, gateway)
	}

	finalizerAdded, err := c.ensureGatewayFinalizer(ctx, gateway)
	if err != nil {
		return err
	}
	if finalizerAdded {
		return nil
	}

	gatewayClass, err := c.gatewayClasses.Get(gatewaymeta.GatewayClassName)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	attachedRoutes, err := c.listRoutesForGateway(types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name})
	if err != nil {
		return err
	}

	evaluation := c.evaluateGateway(gateway, gatewayClass, attachedRoutes)
	if !evaluation.valid {
		if err := c.releaseGatewayIPAddresses(ctx, gateway); err != nil {
			return err
		}
		return c.updateGatewayStatus(ctx, gateway, evaluation)
	}

	vip, err := c.ensureGatewayVIP(ctx, gateway)
	if err != nil {
		evaluation.needsVIP = false
		evaluation.selectedVIP = ""
		evaluation.programmedReason = string(gatewayv1.GatewayReasonAddressNotAssigned)
		evaluation.programmedMessage = err.Error()
		evaluation.listeners = setListenerProgrammedCondition(
			evaluation.listeners,
			metav1.ConditionFalse,
			string(gatewayv1.GatewayReasonAddressNotAssigned),
			err.Error(),
			gateway.Generation,
		)
	} else {
		evaluation.selectedVIP = vip
		evaluation.programmedReason = string(gatewayv1.GatewayReasonPending)
		evaluation.programmedMessage = "VIP reserved; dataplane programming is not implemented in this phase."
		evaluation.listeners = setListenerProgrammedCondition(
			evaluation.listeners,
			metav1.ConditionFalse,
			string(gatewayv1.ListenerReasonPending),
			"VIP reserved; dataplane programming is pending.",
			gateway.Generation,
		)
	}

	return c.updateGatewayStatus(ctx, gateway, evaluation)
}

func shouldManageGatewayClass(gatewayClass *gatewayv1.GatewayClass) bool {
	if gatewayClass == nil {
		return false
	}
	return gatewayClass.Name == gatewaymeta.GatewayClassName || gatewayClass.Spec.ControllerName == controllerName
}

func gatewayClassAcceptedMessage(gatewayClass *gatewayv1.GatewayClass, accepted bool) string {
	if accepted {
		return "GatewayClass accepted by clusterip-gw-controller."
	}
	if gatewayClass.Name != gatewaymeta.GatewayClassName {
		return fmt.Sprintf("Only GatewayClass %q is supported by this controller.", gatewaymeta.GatewayClassName)
	}
	return fmt.Sprintf("GatewayClass %q must set spec.controllerName to %q.", gatewaymeta.GatewayClassName, controllerName)
}

func (c *Controller) evaluateGateway(gateway *gatewayv1.Gateway, gatewayClass *gatewayv1.GatewayClass, attachedRoutes []*gatewayv1alpha2.TCPRoute) gatewayEvaluation {
	evaluation := gatewayEvaluation{
		acceptedStatus:    metav1.ConditionFalse,
		acceptedReason:    string(gatewayv1.GatewayReasonPending),
		acceptedMessage:   "Waiting for GatewayClass acceptance.",
		programmedReason:  string(gatewayv1.GatewayReasonPending),
		programmedMessage: "Waiting for controller reconciliation.",
	}

	if gatewayClass == nil {
		evaluation.acceptedReason = string(gatewayv1.GatewayReasonInvalidParameters)
		evaluation.acceptedMessage = fmt.Sprintf("GatewayClass %q was not found.", gatewaymeta.GatewayClassName)
		evaluation.programmedReason = string(gatewayv1.GatewayReasonInvalid)
		evaluation.programmedMessage = evaluation.acceptedMessage
		return evaluation
	}
	if gatewayClass.Spec.ControllerName != controllerName {
		evaluation.acceptedReason = string(gatewayv1.GatewayReasonInvalidParameters)
		evaluation.acceptedMessage = fmt.Sprintf("GatewayClass %q is controlled by %q, not %q.", gatewayClass.Name, gatewayClass.Spec.ControllerName, controllerName)
		evaluation.programmedReason = string(gatewayv1.GatewayReasonInvalid)
		evaluation.programmedMessage = evaluation.acceptedMessage
		return evaluation
	}

	validation := validateGatewayListeners(gateway)
	evaluation.listeners = validation.statuses
	if !validation.valid {
		evaluation.acceptedReason = string(gatewayv1.GatewayReasonListenersNotValid)
		evaluation.acceptedMessage = validation.message
		evaluation.programmedReason = string(gatewayv1.GatewayReasonInvalid)
		evaluation.programmedMessage = validation.message
		return evaluation
	}
	if len(gateway.Spec.Addresses) > 0 {
		evaluation.acceptedReason = string(gatewayv1.GatewayReasonUnsupportedAddress)
		evaluation.acceptedMessage = fmt.Sprintf("Spec.addresses is not supported for %s Gateways in this phase.", gatewaymeta.GatewayClassName)
		evaluation.programmedReason = string(gatewayv1.GatewayReasonInvalid)
		evaluation.programmedMessage = evaluation.acceptedMessage
		evaluation.listeners = listenerStatusesForGatewayMessage(
			gateway.Spec.Listeners,
			evaluation.acceptedMessage,
			gateway.Generation,
		)
		return evaluation
	}

	statuses := make([]gatewayv1.ListenerStatus, 0, len(validation.listeners))
	firstError := ""
	usableListeners := 0
	for _, listener := range validation.listeners {
		attached := attachedRoutesForListener(gateway, listener, attachedRoutes)
		switch len(attached) {
		case 0:
			message := fmt.Sprintf("Listener %q requires exactly one attached TCPRoute.", listener.Name)
			statuses = append(statuses, listenerStatusForOutcome(
				listener,
				0,
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonUnsupportedValue),
				message,
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonUnsupportedValue),
				message,
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonInvalid),
				message,
				gateway.Generation,
			))
			if firstError == "" {
				firstError = message
			}
			continue
		case 1:
		default:
			message := fmt.Sprintf("Listener %q supports exactly one attached TCPRoute; multiple attached TCPRoutes were found.", listener.Name)
			statuses = append(statuses, listenerStatusForOutcome(
				listener,
				int32(len(attached)),
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonUnsupportedValue),
				message,
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonUnsupportedValue),
				message,
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonInvalid),
				message,
				gateway.Generation,
			))
			if firstError == "" {
				firstError = message
			}
			continue
		}

		route := attached[0]
		routeEval := c.evaluateRoute(route, matchingParentRef(route, gateway), gateway, gatewayClass, attachedRoutes)
		if routeEval.acceptedStatus != metav1.ConditionTrue || routeEval.resolvedStatus != metav1.ConditionTrue {
			message := fmt.Sprintf("Attached TCPRoute %s/%s is not supported: %s; %s", route.Namespace, route.Name, routeEval.acceptedMessage, routeEval.resolvedMessage)
			statuses = append(statuses, listenerStatusForOutcome(
				listener,
				1,
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonUnsupportedValue),
				message,
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonUnsupportedValue),
				message,
				metav1.ConditionFalse,
				string(gatewayv1.ListenerReasonInvalid),
				message,
				gateway.Generation,
			))
			if firstError == "" {
				firstError = message
			}
			continue
		}

		statuses = append(statuses, listenerStatusForOutcome(
			listener,
			1,
			metav1.ConditionTrue,
			string(gatewayv1.ListenerReasonAccepted),
			"Listener accepted for TCPRoute attachment.",
			metav1.ConditionTrue,
			string(gatewayv1.ListenerReasonResolvedRefs),
			"Listener references resolved.",
			metav1.ConditionFalse,
			string(gatewayv1.ListenerReasonPending),
			"Waiting for VIP allocation.",
			gateway.Generation,
		))
		usableListeners++
	}
	evaluation.listeners = statuses
	if usableListeners == 0 {
		evaluation.acceptedReason = string(gatewayv1.GatewayReasonInvalid)
		evaluation.acceptedMessage = firstError
		evaluation.programmedReason = string(gatewayv1.GatewayReasonInvalid)
		evaluation.programmedMessage = firstError
		return evaluation
	}

	evaluation.valid = true
	evaluation.needsVIP = true
	evaluation.acceptedStatus = metav1.ConditionTrue
	evaluation.acceptedReason = string(gatewayv1.GatewayReasonAccepted)
	if usableListeners == len(validation.listeners) {
		evaluation.acceptedMessage = "Gateway accepted by clusterip-gw-controller."
	} else {
		evaluation.acceptedMessage = "Gateway accepted by clusterip-gw-controller; some listeners are not supported."
	}
	evaluation.programmedReason = string(gatewayv1.GatewayReasonPending)
	evaluation.programmedMessage = "Waiting for VIP allocation."
	return evaluation
}

func validateGatewayListeners(gateway *gatewayv1.Gateway) gatewayListenersValidation {
	result := gatewayListenersValidation{
		valid:   true,
		message: "Listeners accepted.",
	}
	if gateway == nil {
		result.valid = false
		result.message = "Gateway is required."
		return result
	}
	if len(gateway.Spec.Listeners) == 0 {
		result.valid = false
		result.message = "At least one listener is required."
		return result
	}

	listeners := gateway.Spec.Listeners
	nameCounts := make(map[gatewayv1.SectionName]int, len(listeners))
	portCounts := make(map[gatewayv1.PortNumber]int, len(listeners))
	for i := range listeners {
		listener := listeners[i]
		nameCounts[listener.Name]++
		portCounts[listener.Port]++
	}

	if len(listeners) > gatewaymeta.MaxSupportedGatewayListeners {
		result.valid = false
		result.message = fmt.Sprintf("At most %d listeners are supported.", gatewaymeta.MaxSupportedGatewayListeners)
	}

	result.listeners = make([]gatewayv1.Listener, 0, len(listeners))
	result.statuses = make([]gatewayv1.ListenerStatus, 0, len(listeners))
	for i := range listeners {
		listener := listeners[i]
		status := listenerStatusForOutcome(
			listener,
			0,
			metav1.ConditionTrue,
			string(gatewayv1.ListenerReasonAccepted),
			"Listener accepted for TCPRoute attachment.",
			metav1.ConditionTrue,
			string(gatewayv1.ListenerReasonResolvedRefs),
			"Listener references resolved.",
			metav1.ConditionFalse,
			string(gatewayv1.ListenerReasonPending),
			"Waiting for VIP allocation.",
			gateway.Generation,
		)
		message := ""
		acceptedReason := string(gatewayv1.ListenerReasonUnsupportedValue)
		resolvedReason := string(gatewayv1.ListenerReasonUnsupportedValue)
		programmedReason := string(gatewayv1.ListenerReasonInvalid)

		switch {
		case len(listeners) > gatewaymeta.MaxSupportedGatewayListeners:
			message = result.message
		case listener.Name == "":
			message = "Listener name must not be empty."
		case listener.Port == 0:
			message = "Listener port must be non-zero."
		case listener.Protocol != gatewayv1.ProtocolType("TCP"):
			message = "Only TCP listeners are supported."
			acceptedReason = string(gatewayv1.ListenerReasonUnsupportedProtocol)
		case !allowedRoutesSupported(listener.AllowedRoutes):
			message = "listener.allowedRoutes is not supported in this phase."
		case nameCounts[listener.Name] > 1:
			message = fmt.Sprintf("Listener name %q must be unique.", listener.Name)
		case portCounts[listener.Port] > 1:
			message = fmt.Sprintf("Listener port %d must be unique.", listener.Port)
		}

		if message != "" {
			status = listenerStatusForOutcome(
				listener,
				0,
				metav1.ConditionFalse,
				acceptedReason,
				message,
				metav1.ConditionFalse,
				resolvedReason,
				message,
				metav1.ConditionFalse,
				programmedReason,
				message,
				gateway.Generation,
			)
			if result.valid {
				result.valid = false
				result.message = message
			}
			result.statuses = append(result.statuses, status)
			continue
		}

		result.listeners = append(result.listeners, listener)
		result.statuses = append(result.statuses, status)
	}
	return result
}

func allowedRoutesSupported(allowedRoutes *gatewayv1.AllowedRoutes) bool {
	if allowedRoutes == nil {
		return true
	}
	if len(allowedRoutes.Kinds) != 0 {
		return false
	}
	if allowedRoutes.Namespaces == nil {
		return true
	}
	if allowedRoutes.Namespaces.Selector != nil {
		return false
	}
	if allowedRoutes.Namespaces.From == nil {
		return true
	}
	return *allowedRoutes.Namespaces.From == gatewayv1.NamespacesFromSame
}

func listenerStatusForOutcome(
	listener gatewayv1.Listener,
	attachedRoutes int32,
	acceptedStatus metav1.ConditionStatus,
	acceptedReason, acceptedMessage string,
	resolvedStatus metav1.ConditionStatus,
	resolvedReason, resolvedMessage string,
	programmedStatus metav1.ConditionStatus,
	programmedReason, programmedMessage string,
	observedGeneration int64,
) gatewayv1.ListenerStatus {
	supportedKind := gatewayv1.Kind("TCPRoute")
	group := gatewayv1.Group(gatewayAPIGroup)
	kind := gatewayv1.RouteGroupKind{
		Group: &group,
		Kind:  supportedKind,
	}

	return gatewayv1.ListenerStatus{
		Name:           listener.Name,
		SupportedKinds: []gatewayv1.RouteGroupKind{kind},
		AttachedRoutes: attachedRoutes,
		Conditions: mergeConditions(nil,
			condition(string(gatewayv1.ListenerConditionAccepted), acceptedStatus, acceptedReason, acceptedMessage, observedGeneration),
			condition(string(gatewayv1.ListenerConditionResolvedRefs), resolvedStatus, resolvedReason, resolvedMessage, observedGeneration),
			condition(string(gatewayv1.ListenerConditionProgrammed), programmedStatus, programmedReason, programmedMessage, observedGeneration),
		),
	}
}

func listenerStatusesForGatewayMessage(listeners []gatewayv1.Listener, message string, observedGeneration int64) []gatewayv1.ListenerStatus {
	out := make([]gatewayv1.ListenerStatus, 0, len(listeners))
	for i := range listeners {
		out = append(out, listenerStatusForOutcome(
			listeners[i],
			0,
			metav1.ConditionFalse,
			string(gatewayv1.ListenerReasonUnsupportedValue),
			message,
			metav1.ConditionFalse,
			string(gatewayv1.ListenerReasonUnsupportedValue),
			message,
			metav1.ConditionFalse,
			string(gatewayv1.ListenerReasonInvalid),
			message,
			observedGeneration,
		))
	}
	return out
}

func setListenerProgrammedCondition(
	listeners []gatewayv1.ListenerStatus,
	status metav1.ConditionStatus,
	reason, message string,
	observedGeneration int64,
) []gatewayv1.ListenerStatus {
	out := make([]gatewayv1.ListenerStatus, len(listeners))
	for i := range listeners {
		out[i] = listeners[i]
		accepted := apimeta.FindStatusCondition(out[i].Conditions, string(gatewayv1.ListenerConditionAccepted))
		if accepted == nil || accepted.Status != metav1.ConditionTrue {
			continue
		}
		out[i].Conditions = mergeConditions(out[i].Conditions, condition(
			string(gatewayv1.ListenerConditionProgrammed),
			status,
			reason,
			message,
			observedGeneration,
		))
	}
	return out
}
