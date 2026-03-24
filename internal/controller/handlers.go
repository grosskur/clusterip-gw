package controller

import (
	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	gatewayv1alpha2 "sigs.k8s.io/gateway-api/apis/v1alpha2"
)

func (c *Controller) registerHandlers() {
	_, _ = c.gatewayClassInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			gatewayClass := gatewayClassFromObject(obj)
			c.gatewayClassQueue.Add(gatewayClass.Name)
			c.enqueueGatewaysForClass(gatewayClass.Name)
		},
		UpdateFunc: func(_, newObj interface{}) {
			gatewayClass := gatewayClassFromObject(newObj)
			c.gatewayClassQueue.Add(gatewayClass.Name)
			c.enqueueGatewaysForClass(gatewayClass.Name)
		},
		DeleteFunc: func(obj interface{}) {
			gatewayClass := gatewayClassFromObject(obj)
			c.gatewayClassQueue.Add(gatewayClass.Name)
			c.enqueueGatewaysForClass(gatewayClass.Name)
		},
	})

	_, _ = c.gatewayInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			gateway := gatewayFromObject(obj)
			key := types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}
			c.enqueueGatewayNamespacedName(key)
			c.enqueueRoutesForGateway(key)
		},
		UpdateFunc: func(_, newObj interface{}) {
			gateway := gatewayFromObject(newObj)
			key := types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}
			c.enqueueGatewayNamespacedName(key)
			c.enqueueRoutesForGateway(key)
		},
		DeleteFunc: func(obj interface{}) {
			gateway := gatewayFromObject(obj)
			key := types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}
			c.enqueueGatewayNamespacedName(key)
			c.enqueueRoutesForGateway(key)
		},
	})

	_, _ = c.routeInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			route := routeFromObject(obj)
			c.enqueueRouteNamespacedName(types.NamespacedName{Namespace: route.Namespace, Name: route.Name})
			c.enqueueGatewayParentsForRoute(route)
			c.enqueueEndpointSelectorsForRoute(route)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			oldRoute := routeFromObject(oldObj)
			c.enqueueGatewayParentsForRoute(oldRoute)
			c.enqueueEndpointSelectorsForRoute(oldRoute)

			route := routeFromObject(newObj)
			c.enqueueRouteNamespacedName(types.NamespacedName{Namespace: route.Namespace, Name: route.Name})
			c.enqueueGatewayParentsForRoute(route)
			c.enqueueEndpointSelectorsForRoute(route)
		},
		DeleteFunc: func(obj interface{}) {
			route := routeFromObject(obj)
			c.enqueueRouteNamespacedName(types.NamespacedName{Namespace: route.Namespace, Name: route.Name})
			c.enqueueGatewayParentsForRoute(route)
			c.enqueueEndpointSelectorsForRoute(route)
		},
	})

	_, _ = c.endpointSelectorInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueSelectorAndRelatedRoutes,
		UpdateFunc: func(_, newObj interface{}) { c.enqueueSelectorAndRelatedRoutes(newObj) },
		DeleteFunc: c.enqueueSelectorAndRelatedRoutes,
	})

	_, _ = c.podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueEndpointSelectorsForPodObject,
		UpdateFunc: func(_, newObj interface{}) { c.enqueueEndpointSelectorsForPodObject(newObj) },
		DeleteFunc: c.enqueueEndpointSelectorsForPodObject,
	})

	_, _ = c.endpointSliceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueEndpointSelectorForEndpointSliceObject,
		UpdateFunc: func(_, newObj interface{}) { c.enqueueEndpointSelectorForEndpointSliceObject(newObj) },
		DeleteFunc: c.enqueueEndpointSelectorForEndpointSliceObject,
	})

	_, _ = c.ipAddressInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    c.enqueueGatewayForIPAddressObject,
		UpdateFunc: func(_, newObj interface{}) { c.enqueueGatewayForIPAddressObject(newObj) },
		DeleteFunc: c.enqueueGatewayForIPAddressObject,
	})

	_, _ = c.serviceCIDRInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(interface{}) { c.enqueueGatewaysForClass(gatewaymeta.GatewayClassName) },
		UpdateFunc: func(_, _ interface{}) { c.enqueueGatewaysForClass(gatewaymeta.GatewayClassName) },
		DeleteFunc: func(interface{}) { c.enqueueGatewaysForClass(gatewaymeta.GatewayClassName) },
	})
}

func (c *Controller) enqueueSelectorAndRelatedRoutes(obj interface{}) {
	selector := endpointSelectorFromObject(obj)
	key := types.NamespacedName{Namespace: selector.GetNamespace(), Name: selector.GetName()}
	c.enqueueEndpointSelectorNamespacedName(key)

	items, err := c.routeInformer.GetIndexer().ByIndex(routeByBackendEndpointSelectorIndex, namespacedKey(key))
	if err != nil {
		klog.ErrorS(err, "List routes by backend XEndpointSelector index", "namespace", selector.GetNamespace(), "name", selector.GetName())
		return
	}

	for _, item := range items {
		route := routeFromObject(item)
		c.enqueueRouteNamespacedName(types.NamespacedName{Namespace: route.Namespace, Name: route.Name})
		c.enqueueGatewayParentsForRoute(route)
	}
}

func (c *Controller) enqueueEndpointSelectorsForPodObject(obj interface{}) {
	pod := podFromObject(obj)
	if pod.Namespace == "" {
		return
	}

	items, err := c.endpointSelectorInformer.GetIndexer().ByIndex(cache.NamespaceIndex, pod.Namespace)
	if err != nil {
		klog.ErrorS(err, "List XEndpointSelectors by namespace", "namespace", pod.Namespace)
		return
	}

	for _, item := range items {
		selector := endpointSelectorFromObject(item)
		c.enqueueEndpointSelectorNamespacedName(types.NamespacedName{Namespace: selector.GetNamespace(), Name: selector.GetName()})
	}
}

func (c *Controller) enqueueEndpointSelectorForEndpointSliceObject(obj interface{}) {
	endpointSlice := endpointSliceFromObject(obj)
	if key := endpointSelectorKeyForEndpointSlice(endpointSlice); key != "" {
		c.endpointSelectorQueue.Add(key)
	}
}

func (c *Controller) enqueueGatewayForIPAddressObject(obj interface{}) {
	ipAddress := ipAddressFromObject(obj)
	if key := gatewayKeyForIPAddress(ipAddress); key != "" {
		c.gatewayQueue.Add(key)
	}
}

func (c *Controller) enqueueGatewayParentsForRoute(route *gatewayv1alpha2.TCPRoute) {
	for _, key := range routeGatewayParentKeys(route) {
		c.gatewayQueue.Add(key)
	}
}

func (c *Controller) enqueueEndpointSelectorsForRoute(route *gatewayv1alpha2.TCPRoute) {
	for _, key := range routeBackendEndpointSelectorKeys(route) {
		c.endpointSelectorQueue.Add(key)
	}
}

func (c *Controller) enqueueGatewaysForClass(className string) {
	items, err := c.gatewayInformer.GetIndexer().ByIndex(gatewayByClassIndex, className)
	if err != nil {
		klog.ErrorS(err, "List gateways by class index", "gatewayClass", className)
		return
	}

	for _, item := range items {
		gateway := gatewayFromObject(item)
		key := types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name}
		c.enqueueGatewayNamespacedName(key)
		c.enqueueRoutesForGateway(key)
	}
}

func (c *Controller) enqueueRoutesForGateway(key types.NamespacedName) {
	items, err := c.routeInformer.GetIndexer().ByIndex(routeByGatewayIndex, namespacedKey(key))
	if err != nil {
		klog.ErrorS(err, "List routes by gateway index", "namespace", key.Namespace, "name", key.Name)
		return
	}

	for _, item := range items {
		route := routeFromObject(item)
		c.enqueueRouteNamespacedName(types.NamespacedName{Namespace: route.Namespace, Name: route.Name})
		c.enqueueEndpointSelectorsForRoute(route)
	}
}

func (c *Controller) enqueueGatewayNamespacedName(key types.NamespacedName) {
	if key.Namespace == "" || key.Name == "" {
		return
	}
	c.gatewayQueue.Add(namespacedKey(key))
}

func (c *Controller) enqueueRouteNamespacedName(key types.NamespacedName) {
	if key.Namespace == "" || key.Name == "" {
		return
	}
	c.routeQueue.Add(namespacedKey(key))
}

func (c *Controller) enqueueEndpointSelectorNamespacedName(key types.NamespacedName) {
	if key.Namespace == "" || key.Name == "" {
		return
	}
	c.endpointSelectorQueue.Add(namespacedKey(key))
}
