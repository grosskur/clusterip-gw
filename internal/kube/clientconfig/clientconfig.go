// Package clientconfig builds shared client-go REST configurations for repo binaries.
package clientconfig

import (
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Options contains the shared client-go connection settings used by repo binaries.
type Options struct {
	Kubeconfig         string
	AcceptContentTypes string
	ContentType        string
	QPS                float32
	Burst              int32
}

// Build returns a rest.Config from either in-cluster config or kubeconfig loading rules.
func Build(options Options, masterOverride string, inClusterConfig func() (*rest.Config, error)) (*rest.Config, error) {
	overrides := &clientcmd.ConfigOverrides{ClusterInfo: clientcmdapi.Cluster{Server: masterOverride}}
	if options.Kubeconfig == "" {
		restConfig, err := inClusterConfig()
		if err == nil {
			restConfig = rest.CopyConfig(restConfig)
			if masterOverride != "" {
				restConfig.Host = masterOverride
			}
			restConfig.AcceptContentTypes = options.AcceptContentTypes
			restConfig.ContentType = options.ContentType
			restConfig.QPS = options.QPS
			restConfig.Burst = int(options.Burst)
			return restConfig, nil
		}
		if masterOverride == "" {
			return nil, err
		}
		restConfig, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(),
			overrides,
		).ClientConfig()
		if err != nil {
			return nil, err
		}
		restConfig.AcceptContentTypes = options.AcceptContentTypes
		restConfig.ContentType = options.ContentType
		restConfig.QPS = options.QPS
		restConfig.Burst = int(options.Burst)
		return restConfig, nil
	}

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		&clientcmd.ClientConfigLoadingRules{ExplicitPath: options.Kubeconfig},
		overrides,
	).ClientConfig()
	if err != nil {
		return nil, err
	}

	restConfig.AcceptContentTypes = options.AcceptContentTypes
	restConfig.ContentType = options.ContentType
	restConfig.QPS = options.QPS
	restConfig.Burst = int(options.Burst)
	return restConfig, nil
}
