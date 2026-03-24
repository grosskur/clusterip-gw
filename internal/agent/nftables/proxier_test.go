//go:build linux

package nftables

import (
	"bytes"
	"context"
	"net"
	"sync"
	"testing"
	"time"

	upstreamnftables "github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/grosskur/clusterip-gw/internal/gatewaymeta"
	"github.com/mdlayher/netlink"
	"golang.org/x/sys/unix"
	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type fakeApplier struct {
	mu       sync.Mutex
	specs    []*rulesetSpec
	notify   chan struct{}
	applyErr error
}

func (f *fakeApplier) Apply(_ context.Context, spec *rulesetSpec) error {
	if f.applyErr != nil {
		return f.applyErr
	}

	f.mu.Lock()
	f.specs = append(f.specs, cloneRulesetSpec(spec))
	notify := f.notify
	f.mu.Unlock()

	if notify != nil {
		select {
		case notify <- struct{}{}:
		default:
		}
	}

	return nil
}

func (f *fakeApplier) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.specs)
}

func (f *fakeApplier) lastSpec() *rulesetSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.specs) == 0 {
		return nil
	}
	return cloneRulesetSpec(f.specs[len(f.specs)-1])
}

type recordingConnFactory struct {
	mu       sync.Mutex
	requests [][]netlink.Message
}

func (f *recordingConnFactory) New() (nftConn, error) {
	conn, err := upstreamnftables.New(upstreamnftables.WithTestDial(func(req []netlink.Message) ([]netlink.Message, error) {
		if len(req) != 0 {
			f.mu.Lock()
			f.requests = append(f.requests, append([]netlink.Message(nil), req...))
			f.mu.Unlock()
		}

		acks := make([]netlink.Message, 0, len(req))
		for _, msg := range req {
			if msg.Header.Flags&netlink.Acknowledge == 0 {
				continue
			}
			acks = append(acks, netlink.Message{
				Header: netlink.Header{
					Length:   4,
					Type:     netlink.Error,
					Sequence: msg.Header.Sequence,
					PID:      msg.Header.PID,
				},
				Data: []byte{0, 0, 0, 0},
			})
		}
		return acks, nil
	}))
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func (f *recordingConnFactory) batches() [][]netlink.Message {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([][]netlink.Message, len(f.requests))
	for i := range f.requests {
		out[i] = append([]netlink.Message(nil), f.requests[i]...)
	}
	return out
}

type fakeConnFactory struct {
	mu          sync.Mutex
	connections []*fakeConn
	idx         int
}

func (f *fakeConnFactory) New() (nftConn, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.idx >= len(f.connections) {
		conn := &fakeConn{}
		f.connections = append(f.connections, conn)
	}
	conn := f.connections[f.idx]
	f.idx++
	return conn, nil
}

type fakeConn struct {
	ops       []string
	flushErr  error
	nextSetID uint32
}

func (f *fakeConn) DelTable(t *upstreamnftables.Table) {
	f.ops = append(f.ops, "del-table:"+t.Name)
}

func (f *fakeConn) AddTable(t *upstreamnftables.Table) *upstreamnftables.Table {
	f.ops = append(f.ops, "add-table:"+t.Name)
	return t
}

func (f *fakeConn) AddChain(c *upstreamnftables.Chain) *upstreamnftables.Chain {
	f.ops = append(f.ops, "add-chain:"+c.Name)
	return c
}

func (f *fakeConn) AddSet(s *upstreamnftables.Set, _ []upstreamnftables.SetElement) error {
	if s.ID == 0 {
		f.nextSetID++
		s.ID = f.nextSetID
	}
	f.ops = append(f.ops, "add-set:"+s.Name)
	return nil
}

func (f *fakeConn) AddRule(r *upstreamnftables.Rule) *upstreamnftables.Rule {
	f.ops = append(f.ops, "add-rule:"+r.Chain.Name)
	return r
}

func (f *fakeConn) Flush() error {
	f.ops = append(f.ops, "flush")
	return f.flushErr
}

func TestProxierBuildsBasicTCPClusterIPRuleset(t *testing.T) {
	p := newTestProxier(t)
	addGatewayAndEndpoint(p, "default", "demo", "tcp", "10.96.0.10", 80, "10.0.0.2", 8080, true)

	spec := p.desiredRulesetLocked()
	if spec.tableName != "clusterip-gw" {
		t.Fatalf("expected table name clusterip-gw, got %q", spec.tableName)
	}
	if spec.family != tableFamily {
		t.Fatalf("expected family %v, got %v", tableFamily, spec.family)
	}
	if len(spec.baseChains) != 2 {
		t.Fatalf("expected two base chains, got %d", len(spec.baseChains))
	}
	if spec.baseChains[0].name != preroutingChainName {
		t.Fatalf("expected first base chain %q, got %q", preroutingChainName, spec.baseChains[0].name)
	}
	if spec.baseChains[1].name != outputChainName {
		t.Fatalf("expected second base chain %q, got %q", outputChainName, spec.baseChains[1].name)
	}
	if len(spec.serviceChains) != 1 {
		t.Fatalf("expected one service chain, got %d", len(spec.serviceChains))
	}

	service := spec.serviceChains[0]
	if service.serviceKey != "default/demo:tcp" {
		t.Fatalf("expected service key default/demo:tcp, got %q", service.serviceKey)
	}
	if service.chainName != chainNameFor(service.serviceKey) {
		t.Fatalf("expected hashed chain name, got %q", service.chainName)
	}
	if service.dispatchMapName != dispatchMapNameFor(service.chainName) {
		t.Fatalf("expected dispatch map name %q, got %q", dispatchMapNameFor(service.chainName), service.dispatchMapName)
	}
	if got, want := service.clusterIP.String(), "10.96.0.10"; got != want {
		t.Fatalf("expected cluster IP %q, got %q", want, got)
	}
	if got, want := service.servicePort, uint16(80); got != want {
		t.Fatalf("expected service port %d, got %d", want, got)
	}
	if got, want := len(service.backendChains), 1; got != want {
		t.Fatalf("expected %d backend chain, got %d", want, got)
	}
	if got, want := service.backendChains[0].chainName, backendChainNameFor(service.chainName, 0); got != want {
		t.Fatalf("expected backend chain name %q, got %q", want, got)
	}
	if got, want := service.backendChains[0].backendIP.String(), "10.0.0.2"; got != want {
		t.Fatalf("expected backend IP %q, got %q", want, got)
	}
	if got, want := service.backendChains[0].backendPort, uint16(8080); got != want {
		t.Fatalf("expected backend port %d, got %d", want, got)
	}
}

func TestProxierBuildsRulesetForMultipleListeners(t *testing.T) {
	p := newTestProxier(t)
	p.OnGatewayAdd(testGatewayWithListeners("default", "demo", "10.96.0.10",
		testTCPListener("tcp-80", 80),
		testTCPListener("tcp-81", 81),
	))
	p.OnEndpointSliceAdd(testEndpointSlice("default", "demo", "tcp-80", "10.0.0.2", 8080, true))
	p.OnEndpointSliceAdd(testEndpointSlice("default", "demo", "tcp-81", "10.0.0.3", 8081, true))

	spec := p.desiredRulesetLocked()
	if len(spec.serviceChains) != 2 {
		t.Fatalf("expected two service chains, got %#v", spec.serviceChains)
	}
	if got, want := spec.serviceChains[0].serviceKey, "default/demo:tcp-80"; got != want {
		t.Fatalf("expected first service key %q, got %q", want, got)
	}
	if got, want := spec.serviceChains[1].serviceKey, "default/demo:tcp-81"; got != want {
		t.Fatalf("expected second service key %q, got %q", want, got)
	}
	if got, want := spec.serviceChains[0].servicePort, uint16(80); got != want {
		t.Fatalf("expected first service port %d, got %d", want, got)
	}
	if got, want := spec.serviceChains[1].servicePort, uint16(81); got != want {
		t.Fatalf("expected second service port %d, got %d", want, got)
	}
}

func TestProxierSkipsServicesWithoutReadyEndpoints(t *testing.T) {
	p := newTestProxier(t)
	addGatewayAndEndpoint(p, "default", "demo", "tcp", "10.96.0.10", 80, "10.0.0.2", 8080, false)

	spec := p.desiredRulesetLocked()
	if len(spec.serviceChains) != 0 {
		t.Fatalf("expected no service chains for unready endpoints, got %d", len(spec.serviceChains))
	}
}

func TestProxierBuildsBackendChainsForAllReadyEndpointsInStableOrder(t *testing.T) {
	p := newTestProxier(t)
	p.OnGatewayAdd(testGateway("default", "demo", "tcp", "10.96.0.10", 80))
	p.OnEndpointSliceAdd(testEndpointSliceWithName("default", "demo", "tcp", "demo-tcp-a", "10.0.0.9", 8080, true))
	p.OnEndpointSliceAdd(testEndpointSliceWithName("default", "demo", "tcp", "demo-tcp-b", "10.0.0.2", 8080, true))
	p.OnEndpointSliceAdd(testEndpointSliceWithName("default", "demo", "tcp", "demo-tcp-c", "10.0.0.5", 8080, true))

	spec := p.desiredRulesetLocked()
	if len(spec.serviceChains) != 1 {
		t.Fatalf("expected one service chain, got %d", len(spec.serviceChains))
	}

	backends := spec.serviceChains[0].backendChains
	if got, want := len(backends), 3; got != want {
		t.Fatalf("expected %d backends, got %d", want, got)
	}

	for i, wantIP := range []string{"10.0.0.2", "10.0.0.5", "10.0.0.9"} {
		if got := backends[i].backendIP.String(); got != wantIP {
			t.Fatalf("expected backend %d IP %q, got %q", i, wantIP, got)
		}
		if got, want := backends[i].chainName, backendChainNameFor(spec.serviceChains[0].chainName, i); got != want {
			t.Fatalf("expected backend %d chain name %q, got %q", i, want, got)
		}
	}
}

func TestProxierOrdersServiceChainsDeterministically(t *testing.T) {
	p := newTestProxier(t)
	addGatewayAndEndpoint(p, "default", "zulu", "tcp", "10.96.0.20", 80, "10.0.0.3", 8080, true)
	addGatewayAndEndpoint(p, "default", "alpha", "tcp", "10.96.0.10", 80, "10.0.0.2", 8080, true)

	spec := p.desiredRulesetLocked()
	if len(spec.serviceChains) != 2 {
		t.Fatalf("expected two service chains, got %d", len(spec.serviceChains))
	}
	if got, want := spec.serviceChains[0].serviceKey, "default/alpha:tcp"; got != want {
		t.Fatalf("expected first service key %q, got %q", want, got)
	}
	if got, want := spec.serviceChains[1].serviceKey, "default/zulu:tcp"; got != want {
		t.Fatalf("expected second service key %q, got %q", want, got)
	}
}

func TestProxierRemovesTerminatingGatewayFrontends(t *testing.T) {
	p := newTestProxier(t)

	gateway := testGateway("default", "demo", "tcp", "10.96.0.10", 80)
	p.OnGatewayAdd(gateway)
	p.OnEndpointSliceAdd(testEndpointSlice("default", "demo", "tcp", "10.0.0.2", 8080, true))

	spec := p.desiredRulesetLocked()
	if len(spec.serviceChains) != 1 {
		t.Fatalf("expected one service chain before termination, got %d", len(spec.serviceChains))
	}

	terminatingGateway := gateway.DeepCopy()
	now := metav1.Now()
	terminatingGateway.DeletionTimestamp = &now
	p.OnGatewayUpdate(gateway, terminatingGateway)

	spec = p.desiredRulesetLocked()
	if len(spec.serviceChains) != 0 {
		t.Fatalf("expected terminating gateway frontend to be removed, got %d service chains", len(spec.serviceChains))
	}
}

func TestProxierSkipsGatewayWithTooManyListeners(t *testing.T) {
	p := newTestProxier(t)
	listeners := make([]gatewayv1.Listener, 0, 11)
	for i := 0; i < 11; i++ {
		listeners = append(listeners, testTCPListener(gatewayv1.SectionName("tcp-"+string(rune('a'+i))), gatewayv1.PortNumber(80+i)))
	}
	p.OnGatewayAdd(testGatewayWithListeners("default", "demo", "10.96.0.10", listeners...))
	p.OnEndpointSliceAdd(testEndpointSlice("default", "demo", "tcp-a", "10.0.0.2", 8080, true))

	spec := p.desiredRulesetLocked()
	if len(spec.serviceChains) != 0 {
		t.Fatalf("expected no service chains for unsupported gateway, got %#v", spec.serviceChains)
	}
}

func TestServiceJumpExprsMatchClusterIPTCPPortAndJump(t *testing.T) {
	exprs := serviceJumpExprs(serviceChainSpec{
		chainName:   "svc_deadbeef",
		clusterIP:   net.ParseIP("10.96.0.10").To4(),
		servicePort: 80,
	})
	if len(exprs) != 7 {
		t.Fatalf("expected seven expressions, got %d", len(exprs))
	}

	payload, ok := exprs[0].(*expr.Payload)
	if !ok || payload.Base != expr.PayloadBaseNetworkHeader || payload.Offset != 16 || payload.Len != 4 {
		t.Fatalf("unexpected destination IP payload expression: %#v", exprs[0])
	}
	ipCmp, ok := exprs[1].(*expr.Cmp)
	if !ok || !bytes.Equal(ipCmp.Data, net.ParseIP("10.96.0.10").To4()) {
		t.Fatalf("unexpected destination IP compare expression: %#v", exprs[1])
	}
	l4Proto, ok := exprs[2].(*expr.Meta)
	if !ok || l4Proto.Key != expr.MetaKeyL4PROTO {
		t.Fatalf("unexpected L4 proto expression: %#v", exprs[2])
	}
	protoCmp, ok := exprs[3].(*expr.Cmp)
	if !ok || !bytes.Equal(protoCmp.Data, []byte{unix.IPPROTO_TCP}) {
		t.Fatalf("unexpected protocol compare expression: %#v", exprs[3])
	}
	portPayload, ok := exprs[4].(*expr.Payload)
	if !ok || portPayload.Base != expr.PayloadBaseTransportHeader || portPayload.Offset != 2 || portPayload.Len != 2 {
		t.Fatalf("unexpected destination port payload expression: %#v", exprs[4])
	}
	portCmp, ok := exprs[5].(*expr.Cmp)
	if !ok || !bytes.Equal(portCmp.Data, []byte{0x00, 0x50}) {
		t.Fatalf("unexpected port compare expression: %#v", exprs[5])
	}
	verdict, ok := exprs[6].(*expr.Verdict)
	if !ok || verdict.Kind != expr.VerdictJump || verdict.Chain != "svc_deadbeef" {
		t.Fatalf("unexpected verdict expression: %#v", exprs[6])
	}
}

func TestServiceDispatchExprsSelectBackendViaNumgenAndVerdictMap(t *testing.T) {
	dispatchMap := &upstreamnftables.Set{Name: "map_deadbeef", ID: 12}
	exprs := serviceDispatchExprs(serviceChainSpec{
		backendChains: []backendChainSpec{
			{chainName: "svc_deadbeef_be0"},
			{chainName: "svc_deadbeef_be1"},
		},
	}, dispatchMap)
	if len(exprs) != 2 {
		t.Fatalf("expected two expressions, got %d", len(exprs))
	}

	numgen, ok := exprs[0].(*expr.Numgen)
	if !ok || numgen.Register != 1 || numgen.Type != unix.NFT_NG_RANDOM || numgen.Modulus != 2 || numgen.Offset != 0 {
		t.Fatalf("unexpected numgen expression: %#v", exprs[0])
	}

	lookup, ok := exprs[1].(*expr.Lookup)
	if !ok || lookup.SourceRegister != 1 || lookup.DestRegister != 0 || !lookup.IsDestRegSet || lookup.SetName != dispatchMap.Name || lookup.SetID != dispatchMap.ID {
		t.Fatalf("unexpected dispatch lookup expression: %#v", exprs[1])
	}
}

func TestServiceDispatchMapElementsJumpToBackendChains(t *testing.T) {
	elements := serviceDispatchMapElements(serviceChainSpec{
		backendChains: []backendChainSpec{
			{chainName: "svc_deadbeef_be0"},
			{chainName: "svc_deadbeef_be1"},
		},
	})
	if len(elements) != 2 {
		t.Fatalf("expected two map elements, got %d", len(elements))
	}

	if !bytes.Equal(elements[0].Key, []byte{0x00, 0x00, 0x00, 0x00}) {
		t.Fatalf("unexpected first map key: %#v", elements[0].Key)
	}
	if elements[0].VerdictData == nil || elements[0].VerdictData.Kind != expr.VerdictJump || elements[0].VerdictData.Chain != "svc_deadbeef_be0" {
		t.Fatalf("unexpected first map verdict: %#v", elements[0].VerdictData)
	}
	wantSecondKey := binaryutil.NativeEndian.PutUint32(1)
	if !bytes.Equal(elements[1].Key, wantSecondKey) {
		t.Fatalf("unexpected second map key: %#v", elements[1].Key)
	}
	if elements[1].VerdictData == nil || elements[1].VerdictData.Kind != expr.VerdictJump || elements[1].VerdictData.Chain != "svc_deadbeef_be1" {
		t.Fatalf("unexpected second map verdict: %#v", elements[1].VerdictData)
	}
}

func TestBackendDNATExprsTranslateToSingleBackend(t *testing.T) {
	exprs := backendDNATExprs(backendChainSpec{
		backendIP:   net.ParseIP("10.0.0.2").To4(),
		backendPort: 8080,
	})
	if len(exprs) != 3 {
		t.Fatalf("expected three expressions, got %d", len(exprs))
	}

	addr, ok := exprs[0].(*expr.Immediate)
	if !ok || addr.Register != 1 || !bytes.Equal(addr.Data, net.ParseIP("10.0.0.2").To4()) {
		t.Fatalf("unexpected backend IP immediate: %#v", exprs[0])
	}
	port, ok := exprs[1].(*expr.Immediate)
	if !ok || port.Register != 2 || !bytes.Equal(port.Data, []byte{0x1f, 0x90}) {
		t.Fatalf("unexpected backend port immediate: %#v", exprs[1])
	}
	nat, ok := exprs[2].(*expr.NAT)
	if !ok || nat.Type != expr.NATTypeDestNAT || nat.Family != uint32(tableFamily) || nat.RegAddrMin != 1 || nat.RegProtoMin != 2 {
		t.Fatalf("unexpected NAT expression: %#v", exprs[2])
	}
}

func TestProxierSyncLoopRequeuesThrottledSyncs(t *testing.T) {
	p, err := NewProxier(Options{
		TableName:     "clusterip-gw",
		SyncPeriod:    time.Hour,
		MinSyncPeriod: 25 * time.Millisecond,
		ApplyRules:    true,
	})
	if err != nil {
		t.Fatalf("new proxier: %v", err)
	}

	fake := &fakeApplier{notify: make(chan struct{}, 2)}
	p.applier = fake
	p.gatewaysSynced = true
	p.endpointSlicesSynced = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go p.SyncLoop(ctx)

	p.requestSync()
	waitForCall(t, fake.notify, 200*time.Millisecond)

	start := time.Now()
	p.requestSync()
	waitForCall(t, fake.notify, 300*time.Millisecond)

	if elapsed := time.Since(start); elapsed < p.minSyncPeriod {
		t.Fatalf("expected throttled sync to run after at least %v, got %v", p.minSyncPeriod, elapsed)
	}
	if fake.callCount() != 2 {
		t.Fatalf("expected two apply calls after requeue, got %d", fake.callCount())
	}
}

func TestProxierSyncAppliesSingleRuleset(t *testing.T) {
	p := newTestProxier(t)
	fake := &fakeApplier{}
	p.applier = fake
	p.applyRules = true
	p.gatewaysSynced = true
	p.endpointSlicesSynced = true

	addGatewayAndEndpoint(p, "default", "demo", "tcp", "10.96.0.10", 80, "10.0.0.2", 8080, true)

	if err := p.Sync(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if fake.callCount() != 1 {
		t.Fatalf("expected a single apply call, got %d", fake.callCount())
	}

	spec := fake.lastSpec()
	if spec == nil || len(spec.serviceChains) != 1 {
		t.Fatalf("expected a single service chain to be applied, got %#v", spec)
	}
}

func TestNetlinkApplierProgramsExpectedBatch(t *testing.T) {
	factory := &recordingConnFactory{}
	applier := &netlinkApplier{connFactory: factory}
	spec := &rulesetSpec{
		tableName:  "clusterip-gw",
		family:     tableFamily,
		baseChains: defaultBaseChains(),
		serviceChains: []serviceChainSpec{{
			serviceKey:      "default/demo:tcp",
			chainName:       chainNameFor("default/demo:tcp"),
			dispatchMapName: dispatchMapNameFor(chainNameFor("default/demo:tcp")),
			clusterIP:       net.ParseIP("10.96.0.10").To4(),
			servicePort:     80,
			backendChains: []backendChainSpec{
				{
					chainName:   backendChainNameFor(chainNameFor("default/demo:tcp"), 0),
					backendIP:   net.ParseIP("10.0.0.2").To4(),
					backendPort: 8080,
				},
				{
					chainName:   backendChainNameFor(chainNameFor("default/demo:tcp"), 1),
					backendIP:   net.ParseIP("10.0.0.3").To4(),
					backendPort: 8081,
				},
			},
		}},
	}

	if err := applier.Apply(context.Background(), spec); err != nil {
		t.Fatalf("apply: %v", err)
	}

	batches := factory.batches()
	if len(batches) != 1 {
		t.Fatalf("expected one batched flush, got %d", len(batches))
	}

	messages := batches[0]
	if len(messages) != 16 {
		t.Fatalf("expected 16 netlink messages in the batch, got %d", len(messages))
	}

	if got, want := messages[1].Header.Type, netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES<<8)|unix.NFT_MSG_DELTABLE); got != want {
		t.Fatalf("expected delete-table message, got %v", got)
	}
	if got, want := messages[2].Header.Type, netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES<<8)|unix.NFT_MSG_NEWTABLE); got != want {
		t.Fatalf("expected add-table message, got %v", got)
	}
	for i := 3; i <= 7; i++ {
		if got, want := messages[i].Header.Type, netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES<<8)|unix.NFT_MSG_NEWCHAIN); got != want {
			t.Fatalf("expected chain creation message at index %d, got %v", i, got)
		}
	}
	if got, want := messages[8].Header.Type, netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES<<8)|unix.NFT_MSG_NEWSET); got != want {
		t.Fatalf("expected set creation message at index 8, got %v", got)
	}
	if got, want := messages[9].Header.Type, netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES<<8)|unix.NFT_MSG_NEWSETELEM); got != want {
		t.Fatalf("expected set element message at index 9, got %v", got)
	}
	for i := 10; i <= 14; i++ {
		if got, want := messages[i].Header.Type, netlink.HeaderType((unix.NFNL_SUBSYS_NFTABLES<<8)|unix.NFT_MSG_NEWRULE); got != want {
			t.Fatalf("expected rule creation message at index %d, got %v", i, got)
		}
	}

	if !bytes.Contains(messages[2].Data, []byte("clusterip-gw\x00")) {
		t.Fatalf("expected table name in add-table message")
	}
	if !bytes.Contains(messages[3].Data, []byte(preroutingChainName+"\x00")) {
		t.Fatalf("expected prerouting chain in first chain message")
	}
	if !bytes.Contains(messages[4].Data, []byte(outputChainName+"\x00")) {
		t.Fatalf("expected output chain in second chain message")
	}
	if !bytes.Contains(messages[5].Data, []byte(spec.serviceChains[0].chainName+"\x00")) {
		t.Fatalf("expected service chain in third chain message")
	}
	if !bytes.Contains(messages[6].Data, []byte(spec.serviceChains[0].backendChains[0].chainName+"\x00")) {
		t.Fatalf("expected first backend chain in fourth chain message")
	}
	if !bytes.Contains(messages[7].Data, []byte(spec.serviceChains[0].backendChains[1].chainName+"\x00")) {
		t.Fatalf("expected second backend chain in fifth chain message")
	}
	if !bytes.Contains(messages[8].Data, []byte(spec.serviceChains[0].dispatchMapName+"\x00")) {
		t.Fatalf("expected dispatch map in set creation message")
	}
	if !bytes.Contains(messages[10].Data, []byte(preroutingChainName+"\x00")) {
		t.Fatalf("expected prerouting rule message")
	}
	if !bytes.Contains(messages[11].Data, []byte(outputChainName+"\x00")) {
		t.Fatalf("expected output rule message")
	}
	if !bytes.Contains(messages[12].Data, []byte(spec.serviceChains[0].chainName+"\x00")) {
		t.Fatalf("expected service-chain dispatch rule message")
	}
	if !bytes.Contains(messages[13].Data, []byte(spec.serviceChains[0].backendChains[0].chainName+"\x00")) {
		t.Fatalf("expected first backend DNAT rule message")
	}
	if !bytes.Contains(messages[14].Data, []byte(spec.serviceChains[0].backendChains[1].chainName+"\x00")) {
		t.Fatalf("expected second backend DNAT rule message")
	}
}

func TestNetlinkApplierRetriesWithoutDeleteWhenTableIsMissing(t *testing.T) {
	factory := &fakeConnFactory{
		connections: []*fakeConn{
			{flushErr: unix.ENOENT},
			{},
		},
	}
	applier := &netlinkApplier{connFactory: factory}
	spec := &rulesetSpec{
		tableName:  "clusterip-gw",
		family:     tableFamily,
		baseChains: defaultBaseChains(),
		serviceChains: []serviceChainSpec{{
			serviceKey:      "default/demo:tcp",
			chainName:       chainNameFor("default/demo:tcp"),
			dispatchMapName: dispatchMapNameFor(chainNameFor("default/demo:tcp")),
			clusterIP:       net.ParseIP("10.96.0.10").To4(),
			servicePort:     80,
			backendChains: []backendChainSpec{{
				chainName:   backendChainNameFor(chainNameFor("default/demo:tcp"), 0),
				backendIP:   net.ParseIP("10.0.0.2").To4(),
				backendPort: 8080,
			}},
		}},
	}

	if err := applier.Apply(context.Background(), spec); err != nil {
		t.Fatalf("apply: %v", err)
	}

	if factory.idx != 2 {
		t.Fatalf("expected two connection attempts, got %d", factory.idx)
	}
	if got := factory.connections[0].ops[0]; got != "del-table:clusterip-gw" {
		t.Fatalf("expected first attempt to delete the table, got %q", got)
	}
	if got := factory.connections[1].ops[0]; got != "add-table:clusterip-gw" {
		t.Fatalf("expected retry to skip delete and start with add-table, got %q", got)
	}
}

func newTestProxier(t *testing.T) *Proxier {
	t.Helper()

	p, err := NewProxier(Options{
		TableName:     "clusterip-gw",
		SyncPeriod:    0,
		MinSyncPeriod: 0,
	})
	if err != nil {
		t.Fatalf("new proxier: %v", err)
	}

	return p
}

func addGatewayAndEndpoint(p *Proxier, namespace, name, listenerName, vip string, listenerPort int32, endpointIP string, endpointPort int32, ready bool) {
	p.OnGatewayAdd(testGateway(namespace, name, listenerName, vip, listenerPort))
	p.OnEndpointSliceAdd(testEndpointSlice(namespace, name, listenerName, endpointIP, endpointPort, ready))
}

func testGateway(namespace, name, listenerName, vip string, listenerPort int32) *gatewayv1.Gateway {
	return testGatewayWithListeners(namespace, name, vip, testTCPListener(gatewayv1.SectionName(listenerName), gatewayv1.PortNumber(listenerPort)))
}

func testGatewayWithListeners(namespace, name, vip string, listeners ...gatewayv1.Listener) *gatewayv1.Gateway {
	addressType := gatewayv1.IPAddressType
	return &gatewayv1.Gateway{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: gatewayv1.GatewaySpec{
			GatewayClassName: gatewayv1.ObjectName(gatewaymeta.GatewayClassName),
			Listeners:        append([]gatewayv1.Listener(nil), listeners...),
		},
		Status: gatewayv1.GatewayStatus{
			Addresses: []gatewayv1.GatewayStatusAddress{{
				Type:  &addressType,
				Value: vip,
			}},
		},
	}
}

func testTCPListener(name gatewayv1.SectionName, port gatewayv1.PortNumber) gatewayv1.Listener {
	return gatewayv1.Listener{
		Name:     name,
		Port:     port,
		Protocol: gatewayv1.ProtocolType("TCP"),
	}
}

func testEndpointSlice(namespace, name, listenerName, endpointIP string, endpointPort int32, ready bool) *discoveryv1.EndpointSlice {
	return testEndpointSliceWithName(namespace, name, listenerName, name+"-"+listenerName, endpointIP, endpointPort, ready)
}

func testEndpointSliceWithName(namespace, name, listenerName, sliceName, endpointIP string, endpointPort int32, ready bool) *discoveryv1.EndpointSlice {
	tcp := v1.ProtocolTCP
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      sliceName,
			Labels: map[string]string{
				discoveryv1.LabelManagedBy:           gatewaymeta.ManagedByValue,
				gatewaymeta.GatewayNameLabelKey:      name,
				gatewaymeta.GatewayNamespaceLabelKey: namespace,
				gatewaymeta.GatewayListenerLabelKey:  listenerName,
			},
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Ports: []discoveryv1.EndpointPort{{
			Name:     &listenerName,
			Protocol: &tcp,
			Port:     &endpointPort,
		}},
		Endpoints: []discoveryv1.Endpoint{{
			Addresses: []string{endpointIP},
			Conditions: discoveryv1.EndpointConditions{
				Ready: &ready,
			},
		}},
	}
}

func cloneRulesetSpec(spec *rulesetSpec) *rulesetSpec {
	if spec == nil {
		return nil
	}

	out := &rulesetSpec{
		tableName:     spec.tableName,
		family:        spec.family,
		baseChains:    append([]baseChainSpec(nil), spec.baseChains...),
		serviceChains: make([]serviceChainSpec, len(spec.serviceChains)),
	}
	for i := range spec.serviceChains {
		out.serviceChains[i] = spec.serviceChains[i]
		out.serviceChains[i].clusterIP = copyIPv4(spec.serviceChains[i].clusterIP)
		out.serviceChains[i].backendChains = make([]backendChainSpec, len(spec.serviceChains[i].backendChains))
		for j := range spec.serviceChains[i].backendChains {
			out.serviceChains[i].backendChains[j] = spec.serviceChains[i].backendChains[j]
			out.serviceChains[i].backendChains[j].backendIP = copyIPv4(spec.serviceChains[i].backendChains[j].backendIP)
		}
	}
	return out
}

func waitForCall(t *testing.T, ch <-chan struct{}, timeout time.Duration) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for apply call after %v", timeout)
	}
}
