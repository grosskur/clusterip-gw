package clientconfig

import (
	"errors"
	"testing"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func TestBuildPreservesInClusterCredentialsWithMasterOverride(t *testing.T) {
	options := Options{
		AcceptContentTypes: "application/json",
		ContentType:        "application/vnd.kubernetes.protobuf",
		QPS:                7,
		Burst:              13,
	}

	base := &rest.Config{
		Host:        "https://kubernetes.default.svc",
		BearerToken: "token",
		TLSClientConfig: rest.TLSClientConfig{
			CAData: []byte("ca-data"),
		},
	}

	restConfig, err := Build(options, "https://api.example.test", func() (*rest.Config, error) {
		return base, nil
	})
	if err != nil {
		t.Fatalf("build rest config: %v", err)
	}

	if restConfig.Host != "https://api.example.test" {
		t.Fatalf("expected host override to be applied, got %q", restConfig.Host)
	}
	if restConfig.BearerToken != "token" {
		t.Fatalf("expected in-cluster bearer token to be preserved, got %q", restConfig.BearerToken)
	}
	if string(restConfig.CAData) != "ca-data" {
		t.Fatalf("expected in-cluster CA data to be preserved, got %q", string(restConfig.CAData))
	}
	if restConfig.AcceptContentTypes != options.AcceptContentTypes {
		t.Fatalf("expected AcceptContentTypes %q, got %q", options.AcceptContentTypes, restConfig.AcceptContentTypes)
	}
	if restConfig.ContentType != options.ContentType {
		t.Fatalf("expected ContentType %q, got %q", options.ContentType, restConfig.ContentType)
	}
	if restConfig.QPS != options.QPS {
		t.Fatalf("expected QPS %v, got %v", options.QPS, restConfig.QPS)
	}
	if restConfig.Burst != int(options.Burst) {
		t.Fatalf("expected Burst %d, got %d", options.Burst, restConfig.Burst)
	}
	if base.Host != "https://kubernetes.default.svc" {
		t.Fatalf("expected base config to remain unchanged, got %q", base.Host)
	}
}

func TestBuildUsesKubeconfigWhenProvided(t *testing.T) {
	path := t.TempDir() + "/kubeconfig"
	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"test-cluster": {
				Server:                   "https://cluster.example.test",
				CertificateAuthorityData: []byte("cluster-ca"),
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"test-user": {
				Token: "cluster-token",
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"test-context": {
				Cluster:  "test-cluster",
				AuthInfo: "test-user",
			},
		},
		CurrentContext: "test-context",
	}
	if err := clientcmd.WriteToFile(kubeconfig, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}

	options := Options{
		Kubeconfig: path,
	}

	restConfig, err := Build(options, "https://override.example.test", func() (*rest.Config, error) {
		t.Fatal("expected kubeconfig path to bypass in-cluster config")
		return nil, nil
	})
	if err != nil {
		t.Fatalf("build rest config: %v", err)
	}

	if restConfig.Host != "https://override.example.test" {
		t.Fatalf("expected kubeconfig server override to be applied, got %q", restConfig.Host)
	}
	if restConfig.BearerToken != "cluster-token" {
		t.Fatalf("expected kubeconfig credentials to be preserved, got %q", restConfig.BearerToken)
	}
	if string(restConfig.CAData) != "cluster-ca" {
		t.Fatalf("expected kubeconfig CA data to be preserved, got %q", string(restConfig.CAData))
	}
}

func TestBuildFallsBackToDefaultLoadingRulesWithMasterOverride(t *testing.T) {
	path := t.TempDir() + "/kubeconfig"
	kubeconfig := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"test-cluster": {
				Server: "https://cluster.example.test",
			},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"test-user": {
				Token: "cluster-token",
			},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"test-context": {
				Cluster:  "test-cluster",
				AuthInfo: "test-user",
			},
		},
		CurrentContext: "test-context",
	}
	if err := clientcmd.WriteToFile(kubeconfig, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", path)

	restConfig, err := Build(Options{}, "https://override.example.test", func() (*rest.Config, error) {
		return nil, errors.New("not running in a cluster")
	})
	if err != nil {
		t.Fatalf("build rest config: %v", err)
	}

	if restConfig.Host != "https://override.example.test" {
		t.Fatalf("expected server override to be applied, got %q", restConfig.Host)
	}
	if restConfig.BearerToken != "cluster-token" {
		t.Fatalf("expected fallback kubeconfig credentials to be preserved, got %q", restConfig.BearerToken)
	}
}
