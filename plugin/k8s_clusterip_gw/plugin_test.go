package k8sclusteripgw

import (
	"context"
	"errors"
	"net"
	"testing"

	"github.com/coredns/coredns/plugin/pkg/dnstest"
	"github.com/coredns/coredns/plugin/ready"
	"github.com/coredns/coredns/plugin/test"
	"github.com/miekg/dns"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestServeDNSFallsThroughOutsideZone(t *testing.T) {
	handler := New(&fakeGatewayStore{synced: true}, []string{"gw.cluster.local."}, defaultTTL)
	handler.Next = test.NextHandler(dns.RcodeSuccess, nil)

	req := new(dns.Msg)
	req.SetQuestion("demo.default.cluster.local.", dns.TypeA)

	code, err := handler.ServeDNS(context.Background(), dnstest.NewRecorder(&test.ResponseWriter{}), req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	if code != dns.RcodeSuccess {
		t.Fatalf("ServeDNS returned code %d, want %d", code, dns.RcodeSuccess)
	}
}

func TestServeDNSReturnsGatewayARecords(t *testing.T) {
	handler := New(&fakeGatewayStore{
		synced: true,
		gateways: map[string]*gatewayv1.Gateway{
			"default/demo": gatewayWithAddresses("default", "demo", "10.0.0.7", "10.0.0.8"),
		},
	}, []string{"gw.cluster.local."}, defaultTTL)

	req := new(dns.Msg)
	req.SetQuestion("demo.default.gw.cluster.local.", dns.TypeA)
	w := dnstest.NewRecorder(&test.ResponseWriter{})

	code, err := handler.ServeDNS(context.Background(), w, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	if code != dns.RcodeSuccess {
		t.Fatalf("ServeDNS returned code %d, want %d", code, dns.RcodeSuccess)
	}
	if w.Msg == nil {
		t.Fatal("ServeDNS did not write a response")
	}
	if !w.Msg.Authoritative {
		t.Fatal("expected authoritative response")
	}
	if len(w.Msg.Answer) != 2 {
		t.Fatalf("expected 2 answers, got %d", len(w.Msg.Answer))
	}

	got := make(map[string]struct{}, len(w.Msg.Answer))
	for _, answer := range w.Msg.Answer {
		a, ok := answer.(*dns.A)
		if !ok {
			t.Fatalf("expected A record, got %T", answer)
		}
		got[a.A.String()] = struct{}{}
		if a.Hdr.Name != "demo.default.gw.cluster.local." {
			t.Fatalf("unexpected record name %q", a.Hdr.Name)
		}
		if a.Hdr.Ttl != defaultTTL {
			t.Fatalf("unexpected TTL %d", a.Hdr.Ttl)
		}
	}
	for _, want := range []string{"10.0.0.7", "10.0.0.8"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing answer %s in %#v", want, got)
		}
	}
}

func TestServeDNSReturnsNXDOMAINForUnknownGateway(t *testing.T) {
	handler := New(&fakeGatewayStore{synced: true}, []string{"gw.cluster.local."}, defaultTTL)

	req := new(dns.Msg)
	req.SetQuestion("demo.default.gw.cluster.local.", dns.TypeA)
	w := dnstest.NewRecorder(&test.ResponseWriter{})

	code, err := handler.ServeDNS(context.Background(), w, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	if code != dns.RcodeNameError {
		t.Fatalf("ServeDNS returned code %d, want %d", code, dns.RcodeNameError)
	}
	if w.Msg == nil || w.Msg.Rcode != dns.RcodeNameError {
		t.Fatalf("expected NXDOMAIN response, got %#v", w.Msg)
	}
	if len(w.Msg.Ns) != 1 {
		t.Fatalf("expected SOA in authority section, got %#v", w.Msg.Ns)
	}
}

func TestServeDNSReturnsNXDOMAINWhenGatewayHasNoUsableIPv4(t *testing.T) {
	hostnameType := gatewayv1.HostnameAddressType
	handler := New(&fakeGatewayStore{
		synced: true,
		gateways: map[string]*gatewayv1.Gateway{
			"default/demo": {
				ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
				Spec: gatewayv1.GatewaySpec{
					GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
				},
				Status: gatewayv1.GatewayStatus{
					Addresses: []gatewayv1.GatewayStatusAddress{
						{Type: &hostnameType, Value: "example.net"},
						{Value: "2001:db8::1"},
					},
				},
			},
		},
	}, []string{"gw.cluster.local."}, defaultTTL)

	req := new(dns.Msg)
	req.SetQuestion("demo.default.gw.cluster.local.", dns.TypeA)
	w := dnstest.NewRecorder(&test.ResponseWriter{})

	code, err := handler.ServeDNS(context.Background(), w, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	if code != dns.RcodeNameError {
		t.Fatalf("ServeDNS returned code %d, want %d", code, dns.RcodeNameError)
	}
}

func TestServeDNSReturnsNODATAForUnsupportedTypeOnExistingGateway(t *testing.T) {
	handler := New(&fakeGatewayStore{
		synced: true,
		gateways: map[string]*gatewayv1.Gateway{
			"default/demo": gatewayWithAddresses("default", "demo", "10.0.0.7"),
		},
	}, []string{"gw.cluster.local."}, defaultTTL)

	req := new(dns.Msg)
	req.SetQuestion("demo.default.gw.cluster.local.", dns.TypeAAAA)
	w := dnstest.NewRecorder(&test.ResponseWriter{})

	code, err := handler.ServeDNS(context.Background(), w, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	if code != dns.RcodeSuccess {
		t.Fatalf("ServeDNS returned code %d, want %d", code, dns.RcodeSuccess)
	}
	if w.Msg == nil {
		t.Fatal("ServeDNS did not write a response")
	}
	if len(w.Msg.Answer) != 0 {
		t.Fatalf("expected empty answer section, got %#v", w.Msg.Answer)
	}
	if len(w.Msg.Ns) != 1 {
		t.Fatalf("expected SOA in authority section, got %#v", w.Msg.Ns)
	}
}

func TestServeDNSReturnsSERVFAILBeforeCacheSync(t *testing.T) {
	handler := New(&fakeGatewayStore{synced: false}, []string{"gw.cluster.local."}, defaultTTL)

	req := new(dns.Msg)
	req.SetQuestion("demo.default.gw.cluster.local.", dns.TypeA)
	w := dnstest.NewRecorder(&test.ResponseWriter{})

	code, err := handler.ServeDNS(context.Background(), w, req)
	if err != nil {
		t.Fatalf("ServeDNS returned error: %v", err)
	}
	if code != dns.RcodeServerFailure {
		t.Fatalf("ServeDNS returned code %d, want %d", code, dns.RcodeServerFailure)
	}
	if w.Msg == nil || w.Msg.Rcode != dns.RcodeServerFailure {
		t.Fatalf("expected SERVFAIL response, got %#v", w.Msg)
	}
}

func TestHandlerDoesNotParticipateInCoreDNSReadiness(t *testing.T) {
	handler := New(&fakeGatewayStore{synced: false}, []string{"gw.cluster.local."}, defaultTTL)

	if _, ok := any(handler).(ready.Readiness); ok {
		t.Fatal("expected handler to not implement ready.Readiness")
	}
}

func TestLookupGatewaySurfacesStoreErrors(t *testing.T) {
	handler := New(&fakeGatewayStore{
		synced: true,
		err:    errors.New("boom"),
	}, []string{"gw.cluster.local."}, defaultTTL)

	_, err := handler.lookupGateway("default", "demo")
	if err == nil {
		t.Fatal("expected lookupGateway to return an error")
	}
}

func gatewayWithAddresses(namespace, name string, addresses ...string) *gatewayv1.Gateway {
	statusAddresses := make([]gatewayv1.GatewayStatusAddress, 0, len(addresses))
	for _, address := range addresses {
		addressType := gatewayv1.IPAddressType
		statusAddresses = append(statusAddresses, gatewayv1.GatewayStatusAddress{
			Type:  &addressType,
			Value: address,
		})
	}

	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(gatewayClassName),
		},
		Status: gatewayv1.GatewayStatus{
			Addresses: statusAddresses,
		},
	}
}

type fakeGatewayStore struct {
	synced   bool
	err      error
	gateways map[string]*gatewayv1.Gateway
}

func (f *fakeGatewayStore) HasSynced() bool {
	return f.synced
}

func (f *fakeGatewayStore) GetByKey(key string) (*gatewayv1.Gateway, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	gateway, ok := f.gateways[key]
	if !ok {
		return nil, false, nil
	}
	return gateway.DeepCopy(), true, nil
}

func (f *fakeGatewayStore) Run(context.Context) {}

func TestParseGatewayQuery(t *testing.T) {
	tests := []struct {
		name      string
		qname     string
		zone      string
		wantName  string
		wantNS    string
		wantMatch bool
	}{
		{
			name:      "match",
			qname:     "demo.default.gw.cluster.local.",
			zone:      "gw.cluster.local.",
			wantName:  "demo",
			wantNS:    "default",
			wantMatch: true,
		},
		{
			name:      "apex",
			qname:     "gw.cluster.local.",
			zone:      "gw.cluster.local.",
			wantMatch: false,
		},
		{
			name:      "too many labels",
			qname:     "a.b.c.gw.cluster.local.",
			zone:      "gw.cluster.local.",
			wantMatch: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotNS, gotMatch := parseGatewayQuery(tt.qname, tt.zone)
			if gotMatch != tt.wantMatch {
				t.Fatalf("parseGatewayQuery() match = %v, want %v", gotMatch, tt.wantMatch)
			}
			if gotName != tt.wantName || gotNS != tt.wantNS {
				t.Fatalf("parseGatewayQuery() = (%q, %q), want (%q, %q)", gotName, gotNS, tt.wantName, tt.wantNS)
			}
		})
	}
}

func TestIPv4AddressesFiltersNonIPv4AndNonIP(t *testing.T) {
	addressType := gatewayv1.IPAddressType
	addresses := ipv4Addresses(&gatewayv1.Gateway{
		Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{
				{Type: &addressType, Value: "10.0.0.7"},
				{Type: &addressType, Value: "2001:db8::1"},
				{Type: &addressType, Value: "not-an-ip"},
			},
		},
	})

	if len(addresses) != 1 {
		t.Fatalf("ipv4Addresses returned %d entries, want 1", len(addresses))
	}
	if !addresses[0].Equal(net.ParseIP("10.0.0.7")) {
		t.Fatalf("ipv4Addresses returned %v, want 10.0.0.7", addresses[0])
	}
}
