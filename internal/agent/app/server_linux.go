//go:build linux

package app

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"syscall"

	"github.com/grosskur/clusterip-gw/internal/apputil"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayclientset "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gatewayinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"

	proxynft "github.com/grosskur/clusterip-gw/internal/agent/nftables"
	"github.com/grosskur/clusterip-gw/internal/kube/clientconfig"
)

// Run starts clusterip-gw-agent and blocks until shutdown or error.
func (o *Options) Run(parent context.Context) error {
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	restConfig, err := clientconfig.Build(o.clientConfig(), o.Master, rest.InClusterConfig)
	if err != nil {
		return err
	}
	client, err := clientset.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}
	gatewayClient, err := gatewayclientset.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create gateway client: %w", err)
	}

	proxier, err := proxynft.NewProxier(proxynft.Options{
		TableName:     o.NFTablesTableName,
		SyncPeriod:    o.NFTablesSyncPeriod,
		MinSyncPeriod: o.NFTablesMinSyncPeriod,
		ApplyRules:    o.ApplyRules,
	})
	if err != nil {
		return err
	}

	errCh := make(chan error, 2)
	startHTTPServers(ctx, o.HealthzBindAddress, o.MetricsBindAddress, proxier, errCh)

	coreFactory := informers.NewSharedInformerFactoryWithOptions(client, o.ConfigSyncPeriod)
	gatewayFactory := gatewayinformers.NewSharedInformerFactory(gatewayClient, o.ConfigSyncPeriod)
	gatewayInformer := gatewayFactory.Gateway().V1().Gateways().Informer()
	endpointSliceInformer := coreFactory.Discovery().V1().EndpointSlices().Informer()

	registerHandlers(gatewayInformer, endpointSliceInformer, proxier)

	coreFactory.Start(ctx.Done())
	gatewayFactory.Start(ctx.Done())
	if !cache.WaitForCacheSync(ctx.Done(), gatewayInformer.HasSynced, endpointSliceInformer.HasSynced) {
		return fmt.Errorf("timed out waiting for informer cache sync")
	}

	proxier.OnGatewaySynced()
	proxier.OnEndpointSlicesSynced()

	go proxier.SyncLoop(ctx)

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func registerHandlers(gatewayInformer, endpointSliceInformer cache.SharedIndexInformer, proxier *proxynft.Proxier) {
	_, _ = gatewayInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			proxier.OnGatewayAdd(mustGateway(obj))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			proxier.OnGatewayUpdate(mustGateway(oldObj), mustGateway(newObj))
		},
		DeleteFunc: func(obj interface{}) {
			proxier.OnGatewayDelete(mustGateway(obj))
		},
	})

	_, _ = endpointSliceInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			proxier.OnEndpointSliceAdd(mustEndpointSlice(obj))
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			proxier.OnEndpointSliceUpdate(mustEndpointSlice(oldObj), mustEndpointSlice(newObj))
		},
		DeleteFunc: func(obj interface{}) {
			proxier.OnEndpointSliceDelete(mustEndpointSlice(obj))
		},
	})
}

func mustGateway(obj interface{}) *gatewayv1.Gateway {
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

func mustEndpointSlice(obj interface{}) *discoveryv1.EndpointSlice {
	switch t := obj.(type) {
	case *discoveryv1.EndpointSlice:
		return t
	case cache.DeletedFinalStateUnknown:
		slice, ok := t.Obj.(*discoveryv1.EndpointSlice)
		if !ok {
			panic(fmt.Sprintf("unexpected EndpointSlice tombstone object type %T", t.Obj))
		}
		return slice
	default:
		panic(fmt.Sprintf("unexpected EndpointSlice object type %T", obj))
	}
}

func startHTTPServers(ctx context.Context, healthzAddr, metricsAddr string, proxier *proxynft.Proxier, errCh chan<- error) {
	if healthzAddr != "" {
		apputil.StartHTTPServer(ctx, healthzAddr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if !proxier.Ready() {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("ok"))
		}), errCh)
	}

	if metricsAddr == "" {
		return
	}

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/proxyMode", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("nftables"))
	})
	apputil.StartHTTPServer(ctx, metricsAddr, mux, errCh)
}

func init() {
	klog.InitFlags(nil)
}
