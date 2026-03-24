package controller

import (
	"context"

	"k8s.io/klog/v2"
)

func (c *Controller) runGatewayClassWorker(ctx context.Context) {
	for c.processNextGatewayClass(ctx) {
	}
}

func (c *Controller) runGatewayWorker(ctx context.Context) {
	for c.processNextGateway(ctx) {
	}
}

func (c *Controller) runRouteWorker(ctx context.Context) {
	for c.processNextRoute(ctx) {
	}
}

func (c *Controller) runEndpointSelectorWorker(ctx context.Context) {
	for c.processNextEndpointSelector(ctx) {
	}
}

func (c *Controller) processNextGatewayClass(ctx context.Context) bool {
	key, shutdown := c.gatewayClassQueue.Get()
	if shutdown {
		return false
	}
	defer c.gatewayClassQueue.Done(key)

	if err := c.reconcileGatewayClass(ctx, key); err != nil {
		klog.ErrorS(err, "Reconcile GatewayClass failed", "gatewayClass", key)
		c.gatewayClassQueue.AddRateLimited(key)
		return true
	}

	c.gatewayClassQueue.Forget(key)
	return true
}

func (c *Controller) processNextGateway(ctx context.Context) bool {
	key, shutdown := c.gatewayQueue.Get()
	if shutdown {
		return false
	}
	defer c.gatewayQueue.Done(key)

	if err := c.reconcileGateway(ctx, key); err != nil {
		klog.ErrorS(err, "Reconcile Gateway failed", "gateway", key)
		c.gatewayQueue.AddRateLimited(key)
		return true
	}

	c.gatewayQueue.Forget(key)
	return true
}

func (c *Controller) processNextRoute(ctx context.Context) bool {
	key, shutdown := c.routeQueue.Get()
	if shutdown {
		return false
	}
	defer c.routeQueue.Done(key)

	if err := c.reconcileRoute(ctx, key); err != nil {
		klog.ErrorS(err, "Reconcile TCPRoute failed", "route", key)
		c.routeQueue.AddRateLimited(key)
		return true
	}

	c.routeQueue.Forget(key)
	return true
}

func (c *Controller) processNextEndpointSelector(ctx context.Context) bool {
	key, shutdown := c.endpointSelectorQueue.Get()
	if shutdown {
		return false
	}
	defer c.endpointSelectorQueue.Done(key)

	if err := c.reconcileEndpointSelector(ctx, key); err != nil {
		klog.ErrorS(err, "Reconcile XEndpointSelector failed", "endpointSelector", key)
		c.endpointSelectorQueue.AddRateLimited(key)
		return true
	}

	c.endpointSelectorQueue.Forget(key)
	return true
}
