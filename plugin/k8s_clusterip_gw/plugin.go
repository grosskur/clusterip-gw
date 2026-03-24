// Package k8sclusteripgw serves Gateway VIP DNS records for the clusterip-gw GatewayClass.
package k8sclusteripgw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/coredns/coredns/plugin"
	"github.com/coredns/coredns/request"
	"github.com/miekg/dns"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const (
	pluginName         = "k8s_clusterip_gw"
	defaultZone        = "gw.cluster.local."
	defaultTTL  uint32 = 30

	gatewayClassName = "clusterip-gw"
)

var errGatewayNotFound = errors.New("gateway record not found")

type gatewayStore interface {
	HasSynced() bool
	GetByKey(key string) (*gatewayv1.Gateway, bool, error)
	Run(context.Context)
}

// K8sClusterIPGW serves Gateway VIP DNS records from cached Gateway objects.
type K8sClusterIPGW struct {
	Next  plugin.Handler
	Zones []string
	TTL   uint32

	store gatewayStore
}

var _ plugin.Handler = (*K8sClusterIPGW)(nil)

// New returns a CoreDNS handler for Gateway VIP records.
func New(store gatewayStore, zones []string, ttl uint32) *K8sClusterIPGW {
	if len(zones) == 0 {
		zones = []string{defaultZone}
	}
	if ttl == 0 {
		ttl = defaultTTL
	}

	normalizedZones := make([]string, 0, len(zones))
	for _, zone := range zones {
		normalizedZones = append(normalizedZones, dns.Fqdn(zone))
	}

	return &K8sClusterIPGW{
		Zones: normalizedZones,
		TTL:   ttl,
		store: store,
	}
}

// Name returns the CoreDNS plugin name.
func (k *K8sClusterIPGW) Name() string { return pluginName }

// ServeDNS resolves Gateway VIP records within the configured zones.
func (k *K8sClusterIPGW) ServeDNS(ctx context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	state := request.Request{W: w, Req: r}

	zone := plugin.Zones(k.Zones).Matches(state.Name())
	if zone == "" {
		return plugin.NextOrFailure(k.Name(), k.Next, ctx, w, r)
	}
	zone = state.QName()[len(state.QName())-len(zone):]

	if k.store == nil || !k.store.HasSynced() {
		return k.writeNegative(w, r, zone, dns.RcodeServerFailure)
	}

	if state.QType() == dns.TypeSOA && state.Name() == strings.ToLower(zone) {
		msg := new(dns.Msg)
		msg.SetReply(r)
		msg.Authoritative = true
		msg.Answer = []dns.RR{k.soa(zone)}
		if state.Do() {
			state.SizeAndDo(msg)
		}
		if err := w.WriteMsg(msg); err != nil {
			return dns.RcodeServerFailure, err
		}
		return dns.RcodeSuccess, nil
	}

	name, namespace, ok := parseGatewayQuery(state.Name(), strings.ToLower(zone))
	if !ok {
		return k.writeNegative(w, r, zone, dns.RcodeNameError)
	}

	addresses, err := k.lookupGateway(namespace, name)
	if err != nil {
		if errors.Is(err, errGatewayNotFound) {
			return k.writeNegative(w, r, zone, dns.RcodeNameError)
		}
		return dns.RcodeServerFailure, err
	}

	switch state.QType() {
	case dns.TypeA:
		if len(addresses) == 0 {
			return k.writeNegative(w, r, zone, dns.RcodeNameError)
		}
		return k.writeAResponse(w, r, state.QName(), addresses)
	default:
		return k.writeNegative(w, r, zone, dns.RcodeSuccess)
	}
}

func (k *K8sClusterIPGW) lookupGateway(namespace, name string) ([]net.IP, error) {
	gateway, exists, err := k.store.GetByKey(namespace + "/" + name)
	if err != nil {
		return nil, fmt.Errorf("get gateway %s/%s from cache: %w", namespace, name, err)
	}
	if !exists || gateway == nil {
		return nil, errGatewayNotFound
	}
	if gateway.DeletionTimestamp != nil {
		return nil, errGatewayNotFound
	}
	if string(gateway.Spec.GatewayClassName) != gatewayClassName {
		return nil, errGatewayNotFound
	}
	return ipv4Addresses(gateway), nil
}

func ipv4Addresses(gateway *gatewayv1.Gateway) []net.IP {
	if gateway == nil {
		return nil
	}

	addresses := make([]net.IP, 0, len(gateway.Status.Addresses))
	for _, address := range gateway.Status.Addresses {
		if address.Type != nil && *address.Type != gatewayv1.IPAddressType {
			continue
		}
		ip := net.ParseIP(address.Value)
		if ip == nil {
			continue
		}
		ip = ip.To4()
		if ip == nil {
			continue
		}
		addresses = append(addresses, ip)
	}
	return addresses
}

func parseGatewayQuery(qname, zone string) (string, string, bool) {
	if !dns.IsSubDomain(zone, qname) {
		return "", "", false
	}
	if qname == zone {
		return "", "", false
	}

	trimmed := strings.TrimSuffix(qname, zone)
	trimmed = strings.TrimSuffix(trimmed, ".")
	labels := dns.SplitDomainName(trimmed)
	if len(labels) != 2 {
		return "", "", false
	}
	return labels[0], labels[1], true
}

func (k *K8sClusterIPGW) writeAResponse(w dns.ResponseWriter, r *dns.Msg, qname string, addresses []net.IP) (int, error) {
	msg := new(dns.Msg)
	msg.SetReply(r)
	msg.Authoritative = true
	msg.Answer = make([]dns.RR, 0, len(addresses))
	for _, address := range addresses {
		msg.Answer = append(msg.Answer, (&dns.A{
			Hdr: dns.RR_Header{
				Name:   qname,
				Rrtype: dns.TypeA,
				Class:  dns.ClassINET,
				Ttl:    k.TTL,
			},
			A: address,
		}))
	}
	if err := w.WriteMsg(msg); err != nil {
		return dns.RcodeServerFailure, err
	}
	return dns.RcodeSuccess, nil
}

func (k *K8sClusterIPGW) writeNegative(w dns.ResponseWriter, r *dns.Msg, zone string, rcode int) (int, error) {
	msg := new(dns.Msg)
	msg.SetRcode(r, rcode)
	msg.Authoritative = true
	if rcode != dns.RcodeServerFailure {
		msg.Ns = []dns.RR{k.soa(zone)}
	}
	if err := w.WriteMsg(msg); err != nil {
		return dns.RcodeServerFailure, err
	}
	return rcode, nil
}

func (k *K8sClusterIPGW) soa(zone string) *dns.SOA {
	return &dns.SOA{
		Hdr: dns.RR_Header{
			Name:   dns.Fqdn(zone),
			Rrtype: dns.TypeSOA,
			Class:  dns.ClassINET,
			Ttl:    k.TTL,
		},
		Ns:      "ns." + dns.Fqdn(zone),
		Mbox:    "hostmaster." + dns.Fqdn(zone),
		Serial:  1,
		Refresh: 7200,
		Retry:   1800,
		Expire:  86400,
		Minttl:  k.TTL,
	}
}
