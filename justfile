set shell := ["bash", "-euo", "pipefail", "-c"]

# Show available recipes.
default:
    @just --list

# Build all Go binaries.
build:
    go build ./...

# Run the full test suite.
test:
    go test ./...

# Run golangci-lint with the repo config.
lint:
    golangci-lint run

# Format Go packages and manifests.
fmt: fmt-go fmt-yaml

# Format all Go packages.
fmt-go:
    go fmt ./...

# Format manifests with compact list indentation.
fmt-yaml:
    yamlfmt manifests

# Build all local container images with docker buildx bake.
images:
    docker buildx bake --load

# Generate XEndpointSelector CRD and deepcopy code with controller-gen from $PATH.
generate-crds:
    controller-gen object crd paths=./apis/v1alpha1 output:crd:artifacts:config=manifests/crds

# Create a kind cluster from the repo config.
create-kind cluster_name="kind":
    [[ -n "{{cluster_name}}" ]] || { echo "cluster_name must not be empty" >&2; exit 1; }
    kind create cluster --name "{{cluster_name}}" --config manifests/kind/kind.yaml

# Delete a kind cluster by name.
delete-kind cluster_name="kind":
    [[ -n "{{cluster_name}}" ]] || { echo "cluster_name must not be empty" >&2; exit 1; }
    kind delete cluster --name "{{cluster_name}}"

# Install the Gateway API experimental CRDs into the current cluster.
install-gateway-api-crds:
    kubectl apply --server-side -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.5.0/experimental-install.yaml

# Scale the default CoreDNS Deployment in kube-system to the requested replica count.
scale-default-coredns replicas="0":
    kubectl scale deployment/coredns -n kube-system --replicas={{replicas}}

# Build, load, apply, and roll out the full kind stack.
push-kind cluster_name="kind": images
    [[ -n "{{cluster_name}}" ]] || { echo "cluster_name must not be empty" >&2; exit 1; }
    kind load docker-image clusterip-gw-agent:latest --name "{{cluster_name}}"
    kind load docker-image clusterip-gw-controller:latest --name "{{cluster_name}}"
    kind load docker-image clusterip-gw-coredns:latest --name "{{cluster_name}}"
    kubectl apply -k manifests
    kubectl rollout restart deployment/clusterip-gw-controller -n kube-system
    kubectl rollout restart daemonset/clusterip-gw-agent -n kube-system
    kubectl rollout restart deployment/clusterip-gw-coredns -n kube-system
    kubectl rollout status deployment/clusterip-gw-controller -n kube-system
    kubectl rollout status daemonset/clusterip-gw-agent -n kube-system
    kubectl rollout status deployment/clusterip-gw-coredns -n kube-system

# Apply the test-000 namespace manifests to the current cluster.
apply-test-000:
    kubectl apply -k manifests/test-000/

# Exec into the test client pod and curl the test server DNS name.
curl-test-000 port="80":
    kubectl exec -it deploy/client -n test-000 -- /bin/curl server.test-000.gw.cluster.local:{{port}}

# Run the opt-in existing-kind-cluster end-to-end test.
test-e2e cluster_name="kind": push-kind
    CLUSTERIP_GW_RUN_E2E=1 CLUSTERIP_GW_KIND_CLUSTER="{{cluster_name}}" go test ./test/e2e -count=1 -run TestKindClusterTest000E2E

full-e2e: create-kind install-gateway-api-crds scale-default-coredns push-kind test-e2e delete-kind
