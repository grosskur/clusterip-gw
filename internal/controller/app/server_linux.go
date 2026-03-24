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
	"k8s.io/client-go/dynamic"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
	gatewayclientset "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"

	"github.com/grosskur/clusterip-gw/internal/controller"
	"github.com/grosskur/clusterip-gw/internal/kube/clientconfig"
)

// Run starts clusterip-gw-controller and blocks until shutdown or error.
func (o *Options) Run(parent context.Context) error {
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	restConfig, err := clientconfig.Build(o.clientConfig(), o.Master, rest.InClusterConfig)
	if err != nil {
		return fmt.Errorf("build kube client config: %w", err)
	}

	coreClient, err := clientset.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create kube client: %w", err)
	}
	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create dynamic kube client: %w", err)
	}
	gatewayClient, err := gatewayclientset.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("create gateway client: %w", err)
	}

	ctrl, err := controller.New(coreClient, dynamicClient, gatewayClient, o.ConfigSyncPeriod)
	if err != nil {
		return err
	}

	errCh := make(chan error, 2)
	startHTTPServers(ctx, o.HealthzBindAddress, o.MetricsBindAddress, ctrl, errCh)

	go func() {
		if err := ctrl.Run(ctx); err != nil {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		return nil
	case err := <-errCh:
		return err
	}
}

func startHTTPServers(ctx context.Context, healthzAddr, metricsAddr string, ctrl *controller.Controller, errCh chan<- error) {
	if healthzAddr != "" {
		apputil.StartHTTPServer(ctx, healthzAddr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if !ctrl.Ready() {
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
	apputil.StartHTTPServer(ctx, metricsAddr, mux, errCh)
}

func init() {
	klog.InitFlags(nil)
}
