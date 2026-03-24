package controller

import (
	"context"
	"fmt"
	"net/netip"
	"slices"

	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func (c *Controller) cleanupGatewayResources(ctx context.Context, gateway *gatewayv1.Gateway) error {
	if err := c.releaseGatewayIPAddresses(ctx, gateway); err != nil {
		return err
	}
	return c.removeGatewayFinalizer(ctx, gateway)
}

func (c *Controller) gatewayPreviouslyManaged(gateway *gatewayv1.Gateway) (bool, error) {
	if slices.Contains(gateway.Finalizers, controllerFinalizer) {
		return true, nil
	}

	owned, err := c.listOwnedGatewayIPAddresses(types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name})
	if err != nil {
		return false, err
	}
	return len(owned) > 0, nil
}

func (c *Controller) clearManagedGatewayStatus(ctx context.Context, gateway *gatewayv1.Gateway) error {
	updated := gateway.DeepCopy()
	updated.Status.Addresses = nil
	updated.Status.Listeners = nil
	updated.Status.Conditions = removeConditions(
		updated.Status.Conditions,
		string(gatewayv1.GatewayConditionAccepted),
		string(gatewayv1.GatewayConditionProgrammed),
	)

	if gatewayStatusEqual(gateway, updated) {
		return nil
	}

	_, err := c.gatewayClient.GatewayV1().Gateways(updated.Namespace).UpdateStatus(ctx, updated, metav1.UpdateOptions{})
	return err
}

func (c *Controller) ensureGatewayFinalizer(ctx context.Context, gateway *gatewayv1.Gateway) (bool, error) {
	if slices.Contains(gateway.Finalizers, controllerFinalizer) {
		return false, nil
	}
	updated, err := c.gatewayClient.GatewayV1().Gateways(gateway.Namespace).Get(ctx, gateway.Name, metav1.GetOptions{})
	if err != nil {
		return false, err
	}
	if slices.Contains(updated.Finalizers, controllerFinalizer) {
		return false, nil
	}
	updated.Finalizers = append(updated.Finalizers, controllerFinalizer)
	_, err = c.gatewayClient.GatewayV1().Gateways(updated.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
	if err != nil {
		return false, err
	}
	return true, nil
}

func (c *Controller) removeGatewayFinalizer(ctx context.Context, gateway *gatewayv1.Gateway) error {
	current, err := c.gatewayClient.GatewayV1().Gateways(gateway.Namespace).Get(ctx, gateway.Name, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if !slices.Contains(current.Finalizers, controllerFinalizer) {
		return nil
	}
	updated := current.DeepCopy()
	updated.Finalizers = deleteString(updated.Finalizers, controllerFinalizer)
	_, err = c.gatewayClient.GatewayV1().Gateways(updated.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func (c *Controller) ensureGatewayVIP(ctx context.Context, gateway *gatewayv1.Gateway) (string, error) {
	owned, err := c.listOwnedGatewayIPAddresses(types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name})
	if err != nil {
		return "", err
	}
	if len(owned) > 0 {
		slices.SortFunc(owned, func(a, b *networkingv1.IPAddress) int {
			if a.Name < b.Name {
				return -1
			}
			if a.Name > b.Name {
				return 1
			}
			return 0
		})
		for i := 1; i < len(owned); i++ {
			if err := c.coreClient.NetworkingV1().IPAddresses().Delete(ctx, owned[i].Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
				return "", err
			}
		}
		return owned[0].Name, nil
	}

	cidrs, err := c.listIPv4ServiceCIDRs()
	if err != nil {
		return "", err
	}
	if len(cidrs) == 0 {
		return "", fmt.Errorf("no IPv4 ServiceCIDR ranges are available for allocation")
	}

	taken, err := c.listTakenIPs()
	if err != nil {
		return "", err
	}

	for _, prefix := range cidrs {
		for candidate := firstAllocatable(prefix); candidate.IsValid() && prefix.Contains(candidate); candidate = nextAddr(candidate) {
			if isLastAddress(prefix, candidate) {
				break
			}
			if taken.Has(candidate.String()) {
				continue
			}
			ipAddress := &networkingv1.IPAddress{
				ObjectMeta: metav1.ObjectMeta{
					Name: candidate.String(),
					Labels: map[string]string{
						ipFamilyLabelKey:  ipFamilyIPv4Value,
						managedByLabelKey: managedByLabelValue,
					},
				},
				Spec: networkingv1.IPAddressSpec{
					ParentRef: &networkingv1.ParentReference{
						Group:     gatewayAPIGroup,
						Resource:  "gateways",
						Namespace: gateway.Namespace,
						Name:      gateway.Name,
					},
				},
			}
			if _, err := c.coreClient.NetworkingV1().IPAddresses().Create(ctx, ipAddress, metav1.CreateOptions{}); err != nil {
				if apierrors.IsAlreadyExists(err) {
					taken.Insert(candidate.String())
					continue
				}
				return "", err
			}
			return candidate.String(), nil
		}
	}

	return "", fmt.Errorf("all IPv4 ServiceCIDR addresses are allocated")
}

func (c *Controller) releaseGatewayIPAddresses(ctx context.Context, gateway *gatewayv1.Gateway) error {
	owned, err := c.listOwnedGatewayIPAddresses(types.NamespacedName{Namespace: gateway.Namespace, Name: gateway.Name})
	if err != nil {
		return err
	}
	for _, ipAddress := range owned {
		if err := c.coreClient.NetworkingV1().IPAddresses().Delete(ctx, ipAddress.Name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (c *Controller) listOwnedGatewayIPAddresses(key types.NamespacedName) ([]*networkingv1.IPAddress, error) {
	items, err := c.ipAddressInformer.GetIndexer().ByIndex(ipAddressByGatewayIndex, namespacedKey(key))
	if err != nil {
		return nil, err
	}
	out := make([]*networkingv1.IPAddress, 0, len(items))
	for _, item := range items {
		ipAddress, ok := item.(*networkingv1.IPAddress)
		if !ok {
			continue
		}
		if ipAddress.Labels[managedByLabelKey] != managedByLabelValue {
			continue
		}
		out = append(out, ipAddress)
	}
	return out, nil
}

func (c *Controller) listIPv4ServiceCIDRs() ([]netip.Prefix, error) {
	items, err := c.serviceCIDRs.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	out := make([]netip.Prefix, 0)
	for _, item := range items {
		serviceCIDR := item
		if serviceCIDR == nil || serviceCIDR.DeletionTimestamp != nil {
			continue
		}
		if !serviceCIDRReady(serviceCIDR) {
			continue
		}
		for _, raw := range serviceCIDR.Spec.CIDRs {
			prefix, err := netip.ParsePrefix(raw)
			if err != nil || !prefix.Addr().Is4() {
				continue
			}
			out = append(out, prefix.Masked())
		}
	}
	slices.SortFunc(out, func(a, b netip.Prefix) int {
		if a.String() < b.String() {
			return -1
		}
		if a.String() > b.String() {
			return 1
		}
		return 0
	})
	return out, nil
}

func serviceCIDRReady(serviceCIDR *networkingv1.ServiceCIDR) bool {
	if serviceCIDR == nil {
		return false
	}
	if len(serviceCIDR.Status.Conditions) == 0 {
		return true
	}
	for i := range serviceCIDR.Status.Conditions {
		condition := serviceCIDR.Status.Conditions[i]
		if condition.Type != networkingv1.ServiceCIDRConditionReady {
			continue
		}
		return condition.Status == metav1.ConditionTrue
	}
	return true
}

func (c *Controller) listTakenIPs() (sets.Set[string], error) {
	items, err := c.ipAddresses.List(labels.Everything())
	if err != nil {
		return nil, err
	}
	out := sets.New[string]()
	for _, item := range items {
		ipAddress := item
		if ipAddress == nil {
			continue
		}
		out.Insert(ipAddress.Name)
	}
	return out, nil
}

func deleteString(values []string, target string) []string {
	filtered := values[:0]
	for _, value := range values {
		if value == target {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}

func firstAllocatable(prefix netip.Prefix) netip.Addr {
	return nextAddr(prefix.Addr())
}

func nextAddr(addr netip.Addr) netip.Addr {
	if !addr.IsValid() {
		return netip.Addr{}
	}
	next := addr.As4()
	for i := len(next) - 1; i >= 0; i-- {
		next[i]++
		if next[i] != 0 {
			return netip.AddrFrom4(next)
		}
	}
	return netip.Addr{}
}

func lastAddr(prefix netip.Prefix) netip.Addr {
	addr := prefix.Masked().Addr().As4()
	bits := prefix.Bits()
	for bit := bits; bit < 32; bit++ {
		byteIndex := bit / 8
		maskBit := 7 - (bit % 8)
		addr[byteIndex] |= 1 << maskBit
	}
	return netip.AddrFrom4(addr)
}

func isLastAddress(prefix netip.Prefix, addr netip.Addr) bool {
	return addr == lastAddr(prefix)
}
