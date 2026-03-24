package controller

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type gatewayEvaluation struct {
	acceptedStatus  metav1.ConditionStatus
	acceptedReason  string
	acceptedMessage string

	programmedReason  string
	programmedMessage string

	listeners []gatewayv1.ListenerStatus

	valid       bool
	selectedVIP string
	needsVIP    bool
}

type routeEvaluation struct {
	acceptedStatus  metav1.ConditionStatus
	acceptedReason  string
	acceptedMessage string

	resolvedStatus  metav1.ConditionStatus
	resolvedReason  string
	resolvedMessage string
}

type gatewayListenersValidation struct {
	valid     bool
	message   string
	listeners []gatewayv1.Listener
	statuses  []gatewayv1.ListenerStatus
}
