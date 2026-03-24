//go:build linux

package e2e

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayclientset "sigs.k8s.io/gateway-api/pkg/client/clientset/versioned"
)

const (
	runE2EEnv            = "CLUSTERIP_GW_RUN_E2E"
	kindClusterEnv       = "CLUSTERIP_GW_KIND_CLUSTER"
	kindE2ETestTimeout   = 15 * time.Minute
	kubeSystemNamespace  = "kube-system"
	stockCoreDNSName     = "coredns"
	customCoreDNSName    = "clusterip-gw-coredns"
	controllerDeployment = "clusterip-gw-controller"
	agentDaemonSet       = "clusterip-gw-agent"
	gatewayClassName     = "clusterip-gw"
	controllerNameValue  = "grosskur.github.io/clusterip-gw"
	managedByLabelKey    = "ipaddress.kubernetes.io/managed-by"
	managedByLabelValue  = "gateway.networking.x-k8s.io"
	ipFamilyLabelKey     = "ipaddress.kubernetes.io/ip-family"
	ipFamilyIPv4Value    = "IPv4"
	gatewayAPIGroup      = "gateway.networking.k8s.io"
	gatewayIPAddressType = gatewayv1.IPAddressType
	proxyTableName       = "clusterip-gw"
	testNamespace        = "test-000"
	testGatewayName      = "server"
	testClientDeployment = "client"
	testServerDeployment = "server"
)

var (
	controllerName = gatewayv1.GatewayController(controllerNameValue)
	ipv4Pattern    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	testListeners  = []testListenerSpec{
		{
			listenerName: "tcp-80",
			routeName:    "server-80",
			frontendPort: 80,
			expectedPort: 8080,
		},
		{
			listenerName: "tcp-81",
			routeName:    "server-81",
			frontendPort: 81,
			expectedPort: 8181,
		},
	}
)

type testListenerSpec struct {
	listenerName string
	routeName    string
	frontendPort int32
	expectedPort int32
}

var gatewayAPICRDManifests = []struct {
	name string
	path string
}{
	{
		name: "gatewayclasses.gateway.networking.k8s.io",
		path: "config/crd/standard/gateway.networking.k8s.io_gatewayclasses.yaml",
	},
	{
		name: "gateways.gateway.networking.k8s.io",
		path: "config/crd/standard/gateway.networking.k8s.io_gateways.yaml",
	},
	{
		name: "tcproutes.gateway.networking.k8s.io",
		path: "config/crd/experimental/gateway.networking.k8s.io_tcproutes.yaml",
	},
}

func TestKindClusterTest000E2E(t *testing.T) {
	if os.Getenv(runE2EEnv) != "1" {
		t.Skipf("set %s=1 to run the live kind-cluster end-to-end test", runE2EEnv)
	}

	ctx, cancel := context.WithTimeout(context.Background(), kindE2ETestTimeout)
	defer cancel()

	env, err := newKindE2EEnv(t)
	if err != nil {
		t.Fatalf("build e2e environment: %v", err)
	}

	t.Logf("using kube context %q against kind cluster %q", env.currentContext, env.clusterName)

	originalCoreDNSReplicas, err := env.currentDeploymentReplicas(ctx, kubeSystemNamespace, stockCoreDNSName)
	if err != nil {
		t.Fatalf("get stock CoreDNS replicas: %v", err)
	}

	createdCRDs, err := env.ensureGatewayAPICRDs(ctx)
	if err != nil {
		t.Fatalf("ensure Gateway API CRDs: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 6*time.Minute)
		defer cleanupCancel()

		if err := env.cleanupManagedResources(cleanupCtx); err != nil {
			t.Errorf("cleanup managed resources: %v", err)
		}
		if err := env.scaleDeployment(cleanupCtx, kubeSystemNamespace, stockCoreDNSName, originalCoreDNSReplicas); err != nil {
			t.Errorf("restore stock CoreDNS replicas to %d: %v", originalCoreDNSReplicas, err)
		} else if err := env.waitForDeploymentReady(cleanupCtx, kubeSystemNamespace, stockCoreDNSName, originalCoreDNSReplicas); err != nil {
			t.Errorf("wait for stock CoreDNS recovery: %v", err)
		}
		if len(createdCRDs) > 0 {
			if err := env.deleteGatewayAPICRDs(cleanupCtx, createdCRDs); err != nil {
				t.Errorf("delete Gateway API CRDs: %v", err)
			}
		}
	})

	if err := env.cleanupManagedResources(ctx); err != nil {
		t.Fatalf("cleanup stale resources: %v", err)
	}
	if err := env.ensureImageSources(ctx); err != nil {
		t.Fatalf("inspect local images: %v", err)
	}
	if err := env.loadImages(ctx); err != nil {
		t.Fatalf("load images into kind: %v", err)
	}

	if err := env.scaleDeployment(ctx, kubeSystemNamespace, stockCoreDNSName, 0); err != nil {
		t.Fatalf("scale stock CoreDNS to zero: %v", err)
	}
	if err := env.waitForDeploymentReady(ctx, kubeSystemNamespace, stockCoreDNSName, 0); err != nil {
		t.Fatalf("wait for stock CoreDNS scale-down: %v", err)
	}
	if err := env.waitForPodsGone(ctx, kubeSystemNamespace, labels.Set{"k8s-app": "kube-dns"}.String()); err != nil {
		t.Fatalf("wait for stock CoreDNS pods to terminate: %v", err)
	}

	if err := env.applyKustomization(ctx, "manifests"); err != nil {
		t.Fatalf("apply clusterip-gw manifests: %v", err)
	}
	if err := env.waitForDeploymentReady(ctx, kubeSystemNamespace, controllerDeployment, 1); err != nil {
		t.Fatalf("wait for controller rollout: %v", err)
	}
	if err := env.waitForDaemonSetReady(ctx, kubeSystemNamespace, agentDaemonSet); err != nil {
		t.Fatalf("wait for agent rollout: %v", err)
	}
	if err := env.waitForDeploymentReady(ctx, kubeSystemNamespace, customCoreDNSName, 2); err != nil {
		t.Fatalf("wait for custom CoreDNS rollout: %v", err)
	}
	if err := env.waitForServiceEndpoints(ctx, kubeSystemNamespace, "kube-dns", 1); err != nil {
		t.Fatalf("wait for kube-dns Service endpoints: %v", err)
	}

	if err := env.applyKustomization(ctx, "manifests/test-000"); err != nil {
		t.Fatalf("apply test-000 manifests: %v", err)
	}
	if err := env.waitForDeploymentReady(ctx, testNamespace, testServerDeployment, 3); err != nil {
		t.Fatalf("wait for test server rollout: %v", err)
	}
	if err := env.waitForDeploymentReady(ctx, testNamespace, testClientDeployment, 1); err != nil {
		t.Fatalf("wait for test client rollout: %v", err)
	}

	clientPodName, err := env.waitForReadyPod(ctx, testNamespace, labels.Set{"app": "client"}.String())
	if err != nil {
		t.Fatalf("wait for ready client pod: %v", err)
	}
	if err := env.waitForGatewayClassAccepted(ctx); err != nil {
		t.Fatalf("wait for GatewayClass acceptance: %v", err)
	}
	for _, listener := range testListeners {
		if err := env.waitForRouteAccepted(ctx, testNamespace, listener.routeName); err != nil {
			t.Fatalf("wait for TCPRoute %q acceptance: %v", listener.routeName, err)
		}
	}

	gateway, ipAddress, err := env.waitForGatewayAddress(ctx, testNamespace, testGatewayName)
	if err != nil {
		t.Fatalf("wait for Gateway VIP allocation: %v", err)
	}
	if ipAddress.Labels[managedByLabelKey] != managedByLabelValue {
		t.Fatalf("expected IPAddress managed-by label %q, got %#v", managedByLabelValue, ipAddress.Labels)
	}
	if ipAddress.Labels[ipFamilyLabelKey] != ipFamilyIPv4Value {
		t.Fatalf("expected IPAddress family label %q, got %#v", ipFamilyIPv4Value, ipAddress.Labels)
	}
	if ipAddress.Spec.ParentRef == nil {
		t.Fatalf("expected IPAddress parentRef to be set, got %#v", ipAddress.Spec.ParentRef)
	}
	if ipAddress.Spec.ParentRef.Group != gatewayAPIGroup ||
		ipAddress.Spec.ParentRef.Resource != "gateways" ||
		ipAddress.Spec.ParentRef.Namespace != testNamespace ||
		ipAddress.Spec.ParentRef.Name != testGatewayName {
		t.Fatalf("expected IPAddress parentRef to point at %s/%s, got %#v", testNamespace, testGatewayName, ipAddress.Spec.ParentRef)
	}

	vip := gateway.Status.Addresses[0].Value
	fqdn := fmt.Sprintf("%s.%s.gw.cluster.local", testGatewayName, testNamespace)
	resolvedIP, lookupOutput, err := env.waitForDNSResolution(ctx, testNamespace, clientPodName, fqdn)
	if err != nil {
		t.Fatalf("wait for DNS resolution of %q: %v", fqdn, err)
	}
	if resolvedIP != vip {
		t.Fatalf("expected %q to resolve to VIP %q, got %q from:\n%s", fqdn, vip, resolvedIP, lookupOutput)
	}

	for _, listener := range testListeners {
		backendIP, backendPort, err := env.waitForGatewayBackend(ctx, testNamespace, testGatewayName, listener.listenerName)
		if err != nil {
			t.Fatalf("wait for controller-managed EndpointSlice backend for %q: %v", listener.listenerName, err)
		}
		if backendPort != listener.expectedPort {
			t.Fatalf("expected backend port %d for listener %q, got %d", listener.expectedPort, listener.listenerName, backendPort)
		}

		ruleset, err := env.waitForRuleset(ctx, vip, listener.frontendPort, backendIP, backendPort)
		if err != nil {
			t.Fatalf("wait for nftables programming for listener %q: %v", listener.listenerName, err)
		}
		if !strings.Contains(ruleset, fmt.Sprintf("ip daddr %s tcp dport %d jump", vip, listener.frontendPort)) {
			t.Fatalf("expected gateway jump rule for VIP %s port %d in ruleset:\n%s", vip, listener.frontendPort, ruleset)
		}

		if _, err := env.waitForHTTPGet(ctx, testNamespace, clientPodName, fmt.Sprintf("http://%s:%d", backendIP, backendPort)); err != nil {
			t.Fatalf("wait for direct backend HTTP from client pod for listener %q: %v", listener.listenerName, err)
		}
		if _, err := env.waitForHTTPGet(ctx, testNamespace, clientPodName, fmt.Sprintf("http://%s:%d", vip, listener.frontendPort)); err != nil {
			t.Fatalf("wait for VIP HTTP from client pod for listener %q: %v", listener.listenerName, err)
		}
		body, err := env.waitForHTTPGet(ctx, testNamespace, clientPodName, fmt.Sprintf("http://%s:%d", fqdn, listener.frontendPort))
		if err != nil {
			t.Fatalf("wait for DNS-name HTTP from client pod for listener %q: %v", listener.listenerName, err)
		}
		if strings.TrimSpace(body) == "" {
			t.Fatalf("expected non-empty HTTP response body from %q port %d", fqdn, listener.frontendPort)
		}
	}
}

type kindE2EEnv struct {
	t                *testing.T
	repoRoot         string
	gatewayAPIModule string
	currentContext   string
	clusterName      string
	controlPlaneNode string
	client           clientset.Interface
	gatewayClient    gatewayclientset.Interface
}

func newKindE2EEnv(t *testing.T) (*kindE2EEnv, error) {
	t.Helper()

	for _, tool := range []string{"kind", "docker", "kubectl"} {
		if _, err := exec.LookPath(tool); err != nil {
			return nil, fmt.Errorf("%s binary not available: %w", tool, err)
		}
	}

	repoRoot, err := repoRootFromCaller()
	if err != nil {
		return nil, err
	}

	rawConfig, restConfig, err := currentKubeConfig()
	if err != nil {
		return nil, err
	}

	client, err := clientset.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("build kube client: %w", err)
	}
	gatewayClient, err := gatewayclientset.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("build Gateway API client: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	clusterName, controlPlaneNode, err := detectKindCluster(ctx, client, rawConfig.CurrentContext)
	if err != nil {
		return nil, err
	}
	if override := strings.TrimSpace(os.Getenv(kindClusterEnv)); override != "" {
		clusterName = override
	}

	gatewayAPIModule, err := gatewayAPIModuleDir(ctx, repoRoot)
	if err != nil {
		return nil, err
	}

	return &kindE2EEnv{
		t:                t,
		repoRoot:         repoRoot,
		gatewayAPIModule: gatewayAPIModule,
		currentContext:   rawConfig.CurrentContext,
		clusterName:      clusterName,
		controlPlaneNode: controlPlaneNode,
		client:           client,
		gatewayClient:    gatewayClient,
	}, nil
}

func (e *kindE2EEnv) ensureImageSources(ctx context.Context) error {
	for _, image := range []string{"clusterip-gw-agent:latest", "clusterip-gw-controller:latest", "clusterip-gw-coredns:latest"} {
		if _, err := runCommand(ctx, "docker", "image", "inspect", image); err != nil {
			return fmt.Errorf("inspect local image %q: %w", image, err)
		}
	}
	return nil
}

func (e *kindE2EEnv) loadImages(ctx context.Context) error {
	for _, image := range []string{"clusterip-gw-agent:latest", "clusterip-gw-controller:latest", "clusterip-gw-coredns:latest"} {
		if _, err := runCommand(ctx, "kind", "load", "docker-image", image, "--name", e.clusterName); err != nil {
			return fmt.Errorf("load image %q into kind cluster %q: %w", image, e.clusterName, err)
		}
	}
	return nil
}

func (e *kindE2EEnv) ensureGatewayAPICRDs(ctx context.Context) ([]string, error) {
	created := make([]string, 0, len(gatewayAPICRDManifests))
	for _, manifest := range gatewayAPICRDManifests {
		manifestPath := filepath.Join(e.gatewayAPIModule, manifest.path)
		if _, err := runCommand(ctx, "kubectl", "get", "crd", manifest.name, "-o", "name"); err != nil {
			if _, createErr := runCommand(ctx, "kubectl", "create", "-f", manifestPath); createErr != nil {
				return nil, fmt.Errorf("create Gateway API CRD %s: %w", manifest.name, createErr)
			}
			created = append(created, manifest.name)
		}
		if err := e.waitForCRDEstablished(ctx, manifest.name); err != nil {
			return nil, err
		}
	}
	return created, nil
}

func (e *kindE2EEnv) waitForCRDEstablished(ctx context.Context, name string) error {
	_, err := runCommand(ctx, "kubectl", "wait", "--for=condition=Established", "crd/"+name, "--timeout=90s")
	if err != nil {
		return fmt.Errorf("wait for CRD %s to become Established: %w", name, err)
	}
	return nil
}

func (e *kindE2EEnv) deleteGatewayAPICRDs(ctx context.Context, names []string) error {
	for _, name := range names {
		if _, err := runCommand(ctx, "kubectl", "delete", "crd", name, "--ignore-not-found=true"); err != nil {
			return fmt.Errorf("delete Gateway API CRD %s: %w", name, err)
		}
	}
	return nil
}

func (e *kindE2EEnv) cleanupManagedResources(ctx context.Context) error {
	var errs []error

	if _, err := runCommand(ctx, "kubectl", "delete", "namespace", testNamespace, "--ignore-not-found=true"); err != nil {
		errs = append(errs, fmt.Errorf("delete namespace %s: %w", testNamespace, err))
	}
	if err := e.waitForNamespaceDeleted(ctx, testNamespace); err != nil {
		errs = append(errs, fmt.Errorf("wait for namespace %s deletion: %w", testNamespace, err))
	}
	if _, err := runCommand(ctx, "kubectl", "delete", "gatewayclass", gatewayClassName, "--ignore-not-found=true"); err != nil {
		errs = append(errs, fmt.Errorf("delete GatewayClass %s: %w", gatewayClassName, err))
	}

	if err := e.deleteKustomization(ctx, "manifests"); err != nil {
		errs = append(errs, err)
	}
	if err := e.waitForDeploymentDeleted(ctx, kubeSystemNamespace, customCoreDNSName); err != nil {
		errs = append(errs, fmt.Errorf("wait for %s/%s deletion: %w", kubeSystemNamespace, customCoreDNSName, err))
	}
	if err := e.waitForPodsGone(ctx, kubeSystemNamespace, labels.Set{"app": customCoreDNSName}.String()); err != nil {
		errs = append(errs, fmt.Errorf("wait for %s pods to disappear: %w", customCoreDNSName, err))
	}
	if err := e.waitForDeploymentDeleted(ctx, kubeSystemNamespace, controllerDeployment); err != nil {
		errs = append(errs, fmt.Errorf("wait for %s/%s deletion: %w", kubeSystemNamespace, controllerDeployment, err))
	}
	if err := e.waitForDaemonSetDeleted(ctx, kubeSystemNamespace, agentDaemonSet); err != nil {
		errs = append(errs, fmt.Errorf("wait for %s/%s deletion: %w", kubeSystemNamespace, agentDaemonSet, err))
	}

	return errors.Join(errs...)
}

func (e *kindE2EEnv) applyKustomization(ctx context.Context, relativePath string) error {
	_, err := runCommand(ctx, "kubectl", "apply", "-k", filepath.Join(e.repoRoot, relativePath))
	if err != nil {
		return fmt.Errorf("kubectl apply -k %s: %w", relativePath, err)
	}
	return nil
}

func (e *kindE2EEnv) deleteKustomization(ctx context.Context, relativePath string) error {
	_, err := runCommand(ctx, "kubectl", "delete", "-k", filepath.Join(e.repoRoot, relativePath), "--ignore-not-found=true")
	if err != nil {
		return fmt.Errorf("kubectl delete -k %s: %w", relativePath, err)
	}
	return nil
}

func (e *kindE2EEnv) currentDeploymentReplicas(ctx context.Context, namespace, name string) (int32, error) {
	deployment, err := e.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return 0, fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	if deployment.Spec.Replicas == nil {
		return 1, nil
	}
	return *deployment.Spec.Replicas, nil
}

func (e *kindE2EEnv) scaleDeployment(ctx context.Context, namespace, name string, replicas int32) error {
	deployment, err := e.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	updated := deployment.DeepCopy()
	updated.Spec.Replicas = &replicas
	if _, err := e.client.AppsV1().Deployments(namespace).Update(ctx, updated, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update deployment %s/%s replicas to %d: %w", namespace, name, replicas, err)
	}
	return nil
}

func (e *kindE2EEnv) waitForDeploymentReady(ctx context.Context, namespace, name string, desiredReplicas int32) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		deployment, err := e.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return desiredReplicas == 0, nil
		}
		if err != nil {
			return false, err
		}
		if deployment.Status.ObservedGeneration < deployment.Generation {
			return false, nil
		}
		if desiredReplicas == 0 {
			return deployment.Status.Replicas == 0 &&
				deployment.Status.ReadyReplicas == 0 &&
				deployment.Status.AvailableReplicas == 0 &&
				deployment.Status.UpdatedReplicas == 0, nil
		}
		return deployment.Status.UpdatedReplicas == desiredReplicas &&
			deployment.Status.ReadyReplicas == desiredReplicas &&
			deployment.Status.AvailableReplicas == desiredReplicas, nil
	})
}

func (e *kindE2EEnv) waitForDeploymentDeleted(ctx context.Context, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := e.client.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

func (e *kindE2EEnv) waitForDaemonSetReady(ctx context.Context, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 3*time.Minute, true, func(ctx context.Context) (bool, error) {
		ds, err := e.client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if ds.Status.ObservedGeneration < ds.Generation {
			return false, nil
		}
		desired := ds.Status.DesiredNumberScheduled
		return desired > 0 &&
			ds.Status.CurrentNumberScheduled == desired &&
			ds.Status.UpdatedNumberScheduled == desired &&
			ds.Status.NumberReady == desired &&
			ds.Status.NumberAvailable == desired, nil
	})
}

func (e *kindE2EEnv) waitForDaemonSetDeleted(ctx context.Context, namespace, name string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := e.client.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

func (e *kindE2EEnv) waitForNamespaceDeleted(ctx context.Context, namespace string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		_, err := e.client.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	})
}

func (e *kindE2EEnv) waitForPodsGone(ctx context.Context, namespace, selector string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		pods, err := e.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == 0, nil
	})
}

func (e *kindE2EEnv) waitForServiceEndpoints(ctx context.Context, namespace, name string, minAddresses int) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		endpoints, err := e.client.CoreV1().Endpoints(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		addresses := 0
		for _, subset := range endpoints.Subsets {
			addresses += len(subset.Addresses)
		}
		return addresses >= minAddresses, nil
	})
}

func (e *kindE2EEnv) waitForReadyPod(ctx context.Context, namespace, selector string) (string, error) {
	var podName string

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		pods, err := e.client.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector})
		if err != nil {
			return false, err
		}

		names := make([]string, 0, len(pods.Items))
		ready := make(map[string]struct{}, len(pods.Items))
		for i := range pods.Items {
			pod := &pods.Items[i]
			if pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning {
				continue
			}

			isReady := true
			for _, status := range pod.Status.ContainerStatuses {
				if !status.Ready {
					isReady = false
					break
				}
			}
			if !isReady {
				continue
			}

			names = append(names, pod.Name)
			ready[pod.Name] = struct{}{}
		}
		if len(names) == 0 {
			return false, nil
		}

		sort.Strings(names)
		podName = names[0]
		_, ok := ready[podName]
		return ok, nil
	})
	if err != nil {
		return "", fmt.Errorf("wait for ready pod in %s matching %q: %w", namespace, selector, err)
	}

	return podName, nil
}

func (e *kindE2EEnv) waitForGatewayClassAccepted(ctx context.Context) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		gatewayClass, err := e.gatewayClient.GatewayV1().GatewayClasses().Get(ctx, gatewayClassName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return conditionStatus(gatewayClass.Status.Conditions, string(gatewayv1.GatewayClassConditionStatusAccepted)) == metav1.ConditionTrue, nil
	})
}

func (e *kindE2EEnv) waitForRouteAccepted(ctx context.Context, namespace, routeName string) error {
	return wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		route, err := e.gatewayClient.GatewayV1alpha2().TCPRoutes(namespace).Get(ctx, routeName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		if len(route.Status.Parents) != 1 {
			return false, nil
		}
		parent := route.Status.Parents[0]
		return parent.ControllerName == controllerName &&
			conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionAccepted)) == metav1.ConditionTrue &&
			conditionStatus(parent.Conditions, string(gatewayv1.RouteConditionResolvedRefs)) == metav1.ConditionTrue, nil
	})
}

func (e *kindE2EEnv) waitForGatewayAddress(ctx context.Context, namespace, gatewayName string) (*gatewayv1.Gateway, *networkingv1.IPAddress, error) {
	var (
		gateway   *gatewayv1.Gateway
		ipAddress *networkingv1.IPAddress
	)

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		currentGateway, err := e.gatewayClient.GatewayV1().Gateways(namespace).Get(ctx, gatewayName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		gateway = currentGateway

		if len(currentGateway.Status.Addresses) != 1 {
			return false, nil
		}
		if currentGateway.Status.Addresses[0].Type == nil || *currentGateway.Status.Addresses[0].Type != gatewayIPAddressType {
			return false, nil
		}
		if conditionStatus(currentGateway.Status.Conditions, string(gatewayv1.GatewayConditionAccepted)) != metav1.ConditionTrue {
			return false, nil
		}
		if conditionStatus(currentGateway.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) != metav1.ConditionFalse {
			return false, nil
		}
		if conditionReason(currentGateway.Status.Conditions, string(gatewayv1.GatewayConditionProgrammed)) != string(gatewayv1.GatewayReasonPending) {
			return false, nil
		}

		currentIPAddress, err := e.findGatewayIPAddress(ctx, namespace, gatewayName)
		if err != nil {
			return false, err
		}
		if currentIPAddress == nil {
			return false, nil
		}
		ipAddress = currentIPAddress
		return currentGateway.Status.Addresses[0].Value == currentIPAddress.Name, nil
	})
	if err != nil {
		return nil, nil, err
	}

	return gateway, ipAddress, nil
}

func (e *kindE2EEnv) findGatewayIPAddress(ctx context.Context, namespace, gatewayName string) (*networkingv1.IPAddress, error) {
	ipAddresses, err := e.client.NetworkingV1().IPAddresses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list IPAddresses: %w", err)
	}
	for i := range ipAddresses.Items {
		ipAddress := &ipAddresses.Items[i]
		if ipAddress.Labels[managedByLabelKey] != managedByLabelValue {
			continue
		}
		parent := ipAddress.Spec.ParentRef
		if parent == nil || parent.Group != gatewayAPIGroup || parent.Resource != "gateways" {
			continue
		}
		if parent.Namespace != namespace || parent.Name != gatewayName {
			continue
		}
		return ipAddress.DeepCopy(), nil
	}
	return nil, nil
}

func (e *kindE2EEnv) waitForGatewayBackend(ctx context.Context, namespace, gatewayName, listenerName string) (string, int32, error) {
	var (
		backendIP string
		port      int32
	)

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 2*time.Minute, true, func(ctx context.Context) (bool, error) {
		slices, err := e.client.DiscoveryV1().EndpointSlices(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labels.Set{
				discoveryv1.LabelManagedBy:           gatewaymeta.ManagedByValue,
				gatewaymeta.GatewayNamespaceLabelKey: namespace,
				gatewaymeta.GatewayNameLabelKey:      gatewayName,
				gatewaymeta.GatewayListenerLabelKey:  listenerName,
			}.String(),
		})
		if err != nil {
			return false, err
		}
		if len(slices.Items) != 1 {
			return false, nil
		}

		slice := &slices.Items[0]
		if slice.AddressType != discoveryv1.AddressTypeIPv4 {
			return false, nil
		}

		for _, endpointPort := range slice.Ports {
			if endpointPort.Port == nil {
				continue
			}
			if endpointPort.Protocol != nil && *endpointPort.Protocol != corev1.ProtocolTCP {
				continue
			}
			for _, endpoint := range slice.Endpoints {
				if endpoint.Conditions.Ready != nil && !*endpoint.Conditions.Ready {
					continue
				}
				if len(endpoint.Addresses) == 0 {
					continue
				}
				backendIP = endpoint.Addresses[0]
				port = *endpointPort.Port
				return true, nil
			}
		}

		return false, nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("wait for ready controller-managed EndpointSlice for %s/%s: %w", namespace, gatewayName, err)
	}

	return backendIP, port, nil
}

func (e *kindE2EEnv) waitForRuleset(ctx context.Context, vip string, frontendPort int32, backendIP string, backendPort int32) (string, error) {
	var ruleset string

	err := wait.PollUntilContextTimeout(ctx, 500*time.Millisecond, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		out, err := e.nftListTable(ctx)
		if err != nil {
			if isMissingTableError(err) {
				return false, nil
			}
			return false, err
		}

		ruleset = out
		return strings.Contains(out, "table ip "+proxyTableName) &&
			strings.Contains(out, fmt.Sprintf("ip daddr %s tcp dport %d jump", vip, frontendPort)) &&
			strings.Contains(out, fmt.Sprintf("dnat to %s:%d", backendIP, backendPort)), nil
	})
	if err != nil {
		return "", fmt.Errorf("wait for nftables ruleset for VIP %s: %w", vip, err)
	}

	return ruleset, nil
}

func (e *kindE2EEnv) nftListTable(ctx context.Context) (string, error) {
	return runCommand(ctx, "docker", "exec", e.controlPlaneNode, "nft", "-nn", "list", "table", "ip", proxyTableName)
}

func (e *kindE2EEnv) waitForDNSResolution(ctx context.Context, namespace, podName, fqdn string) (string, string, error) {
	var (
		resolvedIP string
		output     string
	)

	err := wait.PollUntilContextTimeout(ctx, time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		ip, out, err := e.lookupHost(ctx, namespace, podName, fqdn)
		if err != nil {
			return false, nil
		}
		resolvedIP = ip
		output = out
		return resolvedIP != "", nil
	})
	if err != nil {
		return "", "", fmt.Errorf("wait for %s DNS resolution from pod %s/%s: %w", fqdn, namespace, podName, err)
	}

	return resolvedIP, output, nil
}

func (e *kindE2EEnv) lookupHost(ctx context.Context, namespace, podName, fqdn string) (string, string, error) {
	commands := [][]string{
		{"getent", "ahostsv4", fqdn},
		{"getent", "hosts", fqdn},
		{"nslookup", fqdn},
		{"busybox", "nslookup", fqdn},
	}

	var lastErr error
	for _, cmd := range commands {
		out, err := e.execInPod(ctx, namespace, podName, cmd...)
		if err != nil {
			lastErr = err
			continue
		}

		ip := lastIPv4(out)
		if ip != "" {
			return ip, out, nil
		}
		lastErr = fmt.Errorf("no IPv4 address in output of %q:\n%s", strings.Join(cmd, " "), out)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no DNS lookup command succeeded")
	}
	return "", "", lastErr
}

func (e *kindE2EEnv) waitForHTTPGet(ctx context.Context, namespace, podName, url string) (string, error) {
	var body string

	err := wait.PollUntilContextTimeout(ctx, time.Second, 90*time.Second, true, func(ctx context.Context) (bool, error) {
		out, err := e.httpGet(ctx, namespace, podName, url)
		if err != nil {
			return false, nil
		}
		if strings.TrimSpace(out) == "" {
			return false, nil
		}
		body = out
		return true, nil
	})
	if err != nil {
		return "", fmt.Errorf("wait for HTTP GET %s from pod %s/%s: %w", url, namespace, podName, err)
	}

	return body, nil
}

func (e *kindE2EEnv) httpGet(ctx context.Context, namespace, podName, url string) (string, error) {
	commands := [][]string{
		{"curl", "-fsS", "-m", "5", url},
		{"/bin/curl", "-fsS", "-m", "5", url},
		{"wget", "-qO-", url},
		{"busybox", "wget", "-qO-", url},
	}

	var lastErr error
	for _, cmd := range commands {
		out, err := e.execInPod(ctx, namespace, podName, cmd...)
		if err == nil {
			return out, nil
		}
		lastErr = err
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no HTTP client command succeeded")
	}
	return "", lastErr
}

func (e *kindE2EEnv) execInPod(ctx context.Context, namespace, podName string, args ...string) (string, error) {
	cmdArgs := []string{"exec", "-n", namespace, podName, "--"}
	cmdArgs = append(cmdArgs, args...)
	return runCommand(ctx, "kubectl", cmdArgs...)
}

func currentKubeConfig() (clientcmdapi.Config, *rest.Config, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	rawConfig, err := loadingRules.Load()
	if err != nil {
		return clientcmdapi.Config{}, nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	if rawConfig.CurrentContext == "" {
		return clientcmdapi.Config{}, nil, fmt.Errorf("kubeconfig has no current context")
	}

	restConfig, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
		loadingRules,
		&clientcmd.ConfigOverrides{CurrentContext: rawConfig.CurrentContext},
	).ClientConfig()
	if err != nil {
		return clientcmdapi.Config{}, nil, fmt.Errorf("build client config for context %q: %w", rawConfig.CurrentContext, err)
	}

	return *rawConfig, restConfig, nil
}

func detectKindCluster(ctx context.Context, client clientset.Interface, currentContext string) (string, string, error) {
	nodes, err := client.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", "", fmt.Errorf("list cluster nodes: %w", err)
	}
	if len(nodes.Items) == 0 {
		return "", "", fmt.Errorf("kind integration test requires at least one node")
	}

	controlPlaneNode := ""
	for i := range nodes.Items {
		if _, ok := nodes.Items[i].Labels["node-role.kubernetes.io/control-plane"]; ok {
			controlPlaneNode = nodes.Items[i].Name
			break
		}
	}
	if controlPlaneNode == "" {
		return "", "", fmt.Errorf("current context %q does not look like a kind cluster: no control-plane node label found", currentContext)
	}

	clusterName := strings.TrimPrefix(currentContext, "kind-")
	if clusterName == currentContext {
		clusterName = strings.TrimSuffix(controlPlaneNode, "-control-plane")
	}
	if clusterName == "" || clusterName == controlPlaneNode {
		return "", "", fmt.Errorf("could not derive kind cluster name from context %q or control-plane node %q; set %s explicitly", currentContext, controlPlaneNode, kindClusterEnv)
	}

	return clusterName, controlPlaneNode, nil
}

func gatewayAPIModuleDir(ctx context.Context, repoRoot string) (string, error) {
	out, err := runCommandInDir(ctx, repoRoot, "go", "list", "-m", "-f", "{{.Dir}}", "sigs.k8s.io/gateway-api")
	if err != nil {
		return "", fmt.Errorf("resolve gateway-api module directory: %w", err)
	}
	dir := strings.TrimSpace(out)
	if dir == "" {
		return "", fmt.Errorf("gateway-api module directory was empty")
	}
	return dir, nil
}

func conditionStatus(conditions []metav1.Condition, conditionType string) metav1.ConditionStatus {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return conditions[i].Status
		}
	}
	return metav1.ConditionUnknown
}

func conditionReason(conditions []metav1.Condition, conditionType string) string {
	for i := range conditions {
		if conditions[i].Type == conditionType {
			return conditions[i].Reason
		}
	}
	return ""
}

func lastIPv4(out string) string {
	matches := ipv4Pattern.FindAllString(out, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func repoRootFromCaller() (string, error) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("resolve caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..")), nil
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	return runCommandInDir(ctx, "", name, args...)
}

func runCommandInDir(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("%s %s: timed out", name, strings.Join(args, " "))
		}
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}

func isMissingTableError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "No such file or directory")
}
