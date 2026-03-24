package k8sclusteripgw

import (
	"context"
	"fmt"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayclientset "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
	gatewayinformers "sigs.k8s.io/gateway-api/pkg/client/informers/externalversions"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
)

type informerGatewayStore struct {
	informer cache.SharedIndexInformer
}

func newInformerGatewayStore(config *rest.Config) (*informerGatewayStore, error) {
	client, err := gatewayclientset.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("build Gateway API client: %w", err)
	}

	factory := gatewayinformers.NewSharedInformerFactory(client, 0)
	return &informerGatewayStore{
		informer: factory.Gateway().V1().Gateways().Informer(),
	}, nil
}

func (s *informerGatewayStore) HasSynced() bool {
	return s.informer.HasSynced()
}

func (s *informerGatewayStore) GetByKey(key string) (*gatewayv1.Gateway, bool, error) {
	obj, exists, err := s.informer.GetStore().GetByKey(key)
	if err != nil {
		return nil, false, err
	}
	if !exists {
		return nil, false, nil
	}
	gateway, ok := obj.(*gatewayv1.Gateway)
	if !ok {
		panic(fmt.Sprintf("unexpected Gateway cache object type %T", obj))
	}
	return gateway.DeepCopy(), true, nil
}

func (s *informerGatewayStore) Run(ctx context.Context) {
	s.informer.Run(ctx.Done())
}
