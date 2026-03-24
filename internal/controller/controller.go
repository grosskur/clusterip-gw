// Package controller reconciles Gateway API resources into controller-managed state.
package controller

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corelisters "k8s.io/client-go/listers/core/v1"
	discoverylisters "k8s.io/client-go/listers/discovery/v1"
	networkinglisters "k8s.io/client-go/listers/networking/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayclientset "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gatewayinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"
	gatewaylistersv1 "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1"
	gatewaylistersv1alpha2 "sigs.k8s.io/gateway-api/pkg/client/listers/apis/v1alpha2"
)

const (
	gatewayAPIGroup          = "gateway.networking.k8s.io"
	endpointSelectorAPIGroup = "gateway.networking.x-k8s.io"
	endpointSelectorVersion  = "v1alpha1"
	endpointSelectorKind     = "XEndpointSelector"
	controllerNameValue      = "grosskur.github.io/clusterip-gw"
	controllerFinalizer      = "grosskur.github.io/clusterip-gw-ip-address-protection"
	managedByLabelKey        = "ipaddress.kubernetes.io/managed-by"
	managedByLabelValue      = "gateway.networking.x-k8s.io"
	ipFamilyLabelKey         = "ipaddress.kubernetes.io/ip-family"
	ipFamilyIPv4Value        = "IPv4"

	gatewayByClassIndex                  = "gatewayByClass"
	routeByGatewayIndex                  = "routeByGateway"
	routeByBackendEndpointSelectorIndex  = "routeByBackendEndpointSelector"
	ipAddressByGatewayIndex              = "ipAddressByGateway"
	endpointSliceByEndpointSelectorIndex = "endpointSliceByEndpointSelector"
	gatewayClassQueueName                = "clusterip-gw-controller-gatewayclass"
	gatewayQueueName                     = "clusterip-gw-controller-gateway"
	routeQueueName                       = "clusterip-gw-controller-tcproute"
	endpointSelectorQueueName            = "clusterip-gw-controller-endpointselector"
	controllerQueueResyncPeriod          = time.Second
)

var (
	controllerName      = gatewayv1.GatewayController(controllerNameValue)
	endpointSelectorGVR = schema.GroupVersionResource{
		Group:    endpointSelectorAPIGroup,
		Version:  endpointSelectorVersion,
		Resource: "xendpointselectors",
	}
)

// Controller reconciles clusterip-gw GatewayClasses, Gateways, and TCPRoutes.
type Controller struct {
	coreClient    kubernetes.Interface
	dynamicClient dynamic.Interface
	gatewayClient gatewayclientset.Interface

	coreFactory    informers.SharedInformerFactory
	dynamicFactory dynamicinformer.DynamicSharedInformerFactory
	gatewayFactory gatewayinformers.SharedInformerFactory

	gatewayClasses gatewaylistersv1.GatewayClassLister
	gateways       gatewaylistersv1.GatewayLister
	routes         gatewaylistersv1alpha2.TCPRouteLister
	pods           corelisters.PodLister
	endpointSlices discoverylisters.EndpointSliceLister
	ipAddresses    networkinglisters.IPAddressLister
	serviceCIDRs   networkinglisters.ServiceCIDRLister

	gatewayClassInformer     cache.SharedIndexInformer
	gatewayInformer          cache.SharedIndexInformer
	routeInformer            cache.SharedIndexInformer
	endpointSelectorInformer cache.SharedIndexInformer
	podInformer              cache.SharedIndexInformer
	endpointSliceInformer    cache.SharedIndexInformer
	ipAddressInformer        cache.SharedIndexInformer
	serviceCIDRInformer      cache.SharedIndexInformer

	gatewayClassQueue     workqueue.TypedRateLimitingInterface[string]
	gatewayQueue          workqueue.TypedRateLimitingInterface[string]
	routeQueue            workqueue.TypedRateLimitingInterface[string]
	endpointSelectorQueue workqueue.TypedRateLimitingInterface[string]

	ready atomic.Bool
}

// New returns a new controller instance.
func New(coreClient kubernetes.Interface, dynamicClient dynamic.Interface, gatewayClient gatewayclientset.Interface, syncPeriod time.Duration) (*Controller, error) {
	coreFactory := informers.NewSharedInformerFactoryWithOptions(coreClient, syncPeriod)
	dynamicFactory := dynamicinformer.NewDynamicSharedInformerFactory(dynamicClient, syncPeriod)
	gatewayFactory := gatewayinformers.NewSharedInformerFactory(gatewayClient, syncPeriod)

	gatewayClassInformer := gatewayFactory.Gateway().V1().GatewayClasses()
	gatewayInformer := gatewayFactory.Gateway().V1().Gateways()
	routeInformer := gatewayFactory.Gateway().V1alpha2().TCPRoutes()
	endpointSelectorInformer := dynamicFactory.ForResource(endpointSelectorGVR)
	podInformer := coreFactory.Core().V1().Pods()
	endpointSliceInformer := coreFactory.Discovery().V1().EndpointSlices()
	ipAddressInformer := coreFactory.Networking().V1().IPAddresses()
	serviceCIDRInformer := coreFactory.Networking().V1().ServiceCIDRs()

	if err := gatewayInformer.Informer().AddIndexers(cache.Indexers{
		gatewayByClassIndex: indexGatewayByClass,
	}); err != nil {
		return nil, fmt.Errorf("add gateway indexers: %w", err)
	}
	if err := routeInformer.Informer().AddIndexers(cache.Indexers{
		routeByGatewayIndex:                 indexRouteByGateway,
		routeByBackendEndpointSelectorIndex: indexRouteByBackendEndpointSelector,
	}); err != nil {
		return nil, fmt.Errorf("add route indexers: %w", err)
	}
	if err := endpointSliceInformer.Informer().AddIndexers(cache.Indexers{
		endpointSliceByEndpointSelectorIndex: indexEndpointSliceByEndpointSelector,
	}); err != nil {
		return nil, fmt.Errorf("add endpointslice indexers: %w", err)
	}
	if err := ipAddressInformer.Informer().AddIndexers(cache.Indexers{
		ipAddressByGatewayIndex: indexIPAddressByGateway,
	}); err != nil {
		return nil, fmt.Errorf("add ipaddress indexers: %w", err)
	}

	c := &Controller{
		coreClient:    coreClient,
		dynamicClient: dynamicClient,
		gatewayClient: gatewayClient,

		coreFactory:    coreFactory,
		dynamicFactory: dynamicFactory,
		gatewayFactory: gatewayFactory,

		gatewayClasses: gatewayClassInformer.Lister(),
		gateways:       gatewayInformer.Lister(),
		routes:         routeInformer.Lister(),
		pods:           podInformer.Lister(),
		endpointSlices: endpointSliceInformer.Lister(),
		ipAddresses:    ipAddressInformer.Lister(),
		serviceCIDRs:   serviceCIDRInformer.Lister(),

		gatewayClassInformer:     gatewayClassInformer.Informer(),
		gatewayInformer:          gatewayInformer.Informer(),
		routeInformer:            routeInformer.Informer(),
		endpointSelectorInformer: endpointSelectorInformer.Informer(),
		podInformer:              podInformer.Informer(),
		endpointSliceInformer:    endpointSliceInformer.Informer(),
		ipAddressInformer:        ipAddressInformer.Informer(),
		serviceCIDRInformer:      serviceCIDRInformer.Informer(),

		gatewayClassQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: gatewayClassQueueName},
		),
		gatewayQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: gatewayQueueName},
		),
		routeQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: routeQueueName},
		),
		endpointSelectorQueue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.DefaultTypedControllerRateLimiter[string](),
			workqueue.TypedRateLimitingQueueConfig[string]{Name: endpointSelectorQueueName},
		),
	}

	c.registerHandlers()

	return c, nil
}

// Run starts the controller.
func (c *Controller) Run(ctx context.Context) error {
	defer c.gatewayClassQueue.ShutDown()
	defer c.gatewayQueue.ShutDown()
	defer c.routeQueue.ShutDown()
	defer c.endpointSelectorQueue.ShutDown()

	c.coreFactory.Start(ctx.Done())
	c.dynamicFactory.Start(ctx.Done())
	c.gatewayFactory.Start(ctx.Done())

	if !cache.WaitForCacheSync(
		ctx.Done(),
		c.gatewayClassInformer.HasSynced,
		c.gatewayInformer.HasSynced,
		c.routeInformer.HasSynced,
		c.endpointSelectorInformer.HasSynced,
		c.podInformer.HasSynced,
		c.endpointSliceInformer.HasSynced,
		c.ipAddressInformer.HasSynced,
		c.serviceCIDRInformer.HasSynced,
	) {
		return fmt.Errorf("timed out waiting for informer cache sync")
	}

	c.ready.Store(true)

	go wait.UntilWithContext(ctx, c.runGatewayClassWorker, controllerQueueResyncPeriod)
	go wait.UntilWithContext(ctx, c.runGatewayWorker, controllerQueueResyncPeriod)
	go wait.UntilWithContext(ctx, c.runGatewayWorker, controllerQueueResyncPeriod)
	go wait.UntilWithContext(ctx, c.runRouteWorker, controllerQueueResyncPeriod)
	go wait.UntilWithContext(ctx, c.runEndpointSelectorWorker, controllerQueueResyncPeriod)

	<-ctx.Done()
	return nil
}

// Ready reports whether the controller has completed its startup sync.
func (c *Controller) Ready() bool {
	return c.ready.Load()
}
