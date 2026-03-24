//go:build linux

// Package nftables implements the nftables-backed proxy engine.
package nftables

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"slices"
	"sync"
	"time"

	upstreamnftables "github.com/google/nftables"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/klog/v2"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	agentstate "github.com/grosskur/clusterip-gw/internal/agent/state"
)

const tableFamily = upstreamnftables.TableFamilyIPv4

// Options contains the runtime settings for the nftables proxier.
type Options struct {
	TableName     string
	SyncPeriod    time.Duration
	MinSyncPeriod time.Duration
	ApplyRules    bool
}

// Proxier translates Gateway and EndpointSlice state into an nftables ruleset.
type Proxier struct {
	lock                 sync.RWMutex
	tableName            string
	syncPeriod           time.Duration
	minSyncPeriod        time.Duration
	applyRules           bool
	gatewaysSynced       bool
	endpointSlicesSynced bool
	syncPending          bool
	gatewayTracker       *agentstate.GatewayChangeTracker
	endpointTracker      *agentstate.EndpointsChangeTracker
	applier              rulesetApplier
	resyncCh             chan struct{}
	lastSync             time.Time
}

// NewProxier returns an nftables-backed proxier using the provided configuration.
func NewProxier(cfg Options) (*Proxier, error) {
	if cfg.TableName == "" {
		return nil, fmt.Errorf("nftables table name must not be empty")
	}

	return &Proxier{
		tableName:       cfg.TableName,
		syncPeriod:      cfg.SyncPeriod,
		minSyncPeriod:   cfg.MinSyncPeriod,
		applyRules:      cfg.ApplyRules,
		gatewayTracker:  agentstate.NewGatewayChangeTracker(),
		endpointTracker: agentstate.NewEndpointsChangeTracker(),
		applier:         &netlinkApplier{connFactory: realConnFactory{}},
		resyncCh:        make(chan struct{}, 1),
	}, nil
}

// Ready reports whether the proxier has received its initial Gateway and EndpointSlice state.
func (p *Proxier) Ready() bool {
	p.lock.RLock()
	defer p.lock.RUnlock()
	return p.gatewaysSynced && p.endpointSlicesSynced
}

// OnGatewayAdd records a Gateway add event and schedules a sync.
func (p *Proxier) OnGatewayAdd(gateway *gatewayv1.Gateway) {
	p.gatewayTracker.Update(nil, gateway)
	p.requestSync()
}

// OnGatewayUpdate records a Gateway update event and schedules a sync.
func (p *Proxier) OnGatewayUpdate(oldGateway, gateway *gatewayv1.Gateway) {
	p.gatewayTracker.Update(oldGateway, gateway)
	p.requestSync()
}

// OnGatewayDelete records a Gateway delete event and schedules a sync.
func (p *Proxier) OnGatewayDelete(gateway *gatewayv1.Gateway) {
	p.gatewayTracker.Update(gateway, nil)
	p.requestSync()
}

// OnGatewaySynced marks the initial Gateway sync as complete and schedules a sync.
func (p *Proxier) OnGatewaySynced() {
	p.lock.Lock()
	p.gatewaysSynced = true
	p.lock.Unlock()
	p.requestSync()
}

// OnEndpointSliceAdd records an EndpointSlice add event and schedules a sync.
func (p *Proxier) OnEndpointSliceAdd(endpointSlice *discoveryv1.EndpointSlice) {
	p.endpointTracker.Update(endpointSlice, false)
	p.requestSync()
}

// OnEndpointSliceUpdate records an EndpointSlice update event and schedules a sync.
func (p *Proxier) OnEndpointSliceUpdate(_ *discoveryv1.EndpointSlice, newEndpointSlice *discoveryv1.EndpointSlice) {
	p.endpointTracker.Update(newEndpointSlice, false)
	p.requestSync()
}

// OnEndpointSliceDelete records an EndpointSlice delete event and schedules a sync.
func (p *Proxier) OnEndpointSliceDelete(endpointSlice *discoveryv1.EndpointSlice) {
	p.endpointTracker.Update(endpointSlice, true)
	p.requestSync()
}

// OnEndpointSlicesSynced marks the initial EndpointSlice sync as complete and schedules a sync.
func (p *Proxier) OnEndpointSlicesSynced() {
	p.lock.Lock()
	p.endpointSlicesSynced = true
	p.lock.Unlock()
	p.requestSync()
}

// SyncLoop runs the periodic and event-driven nftables reconciliation loop.
func (p *Proxier) SyncLoop(ctx context.Context) {
	ticker := time.NewTicker(p.syncPeriod)
	defer ticker.Stop()

	var retryTimer *time.Timer
	var retryCh <-chan time.Time

	stopRetry := func() {
		if retryTimer == nil {
			return
		}
		if !retryTimer.Stop() {
			select {
			case <-retryCh:
			default:
			}
		}
		retryTimer = nil
		retryCh = nil
	}
	defer stopRetry()

	scheduleRetry := func(delay time.Duration) {
		if delay <= 0 {
			delay = time.Nanosecond
		}
		if retryTimer == nil {
			retryTimer = time.NewTimer(delay)
			retryCh = retryTimer.C
			return
		}
		if !retryTimer.Stop() {
			select {
			case <-retryCh:
			default:
			}
		}
		retryTimer.Reset(delay)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.resyncCh:
		case <-retryCh:
			retryTimer = nil
			retryCh = nil
		}

		delay, err := p.sync(ctx)
		if err != nil {
			klog.Errorf("clusterip-gw-agent sync failed: %v", err)
			continue
		}
		if delay > 0 {
			scheduleRetry(delay)
			continue
		}
		stopRetry()
	}
}

// Sync executes a single proxier reconciliation.
func (p *Proxier) Sync(ctx context.Context) error {
	_, err := p.sync(ctx)
	return err
}

func (p *Proxier) sync(ctx context.Context) (time.Duration, error) {
	p.lock.Lock()
	defer p.lock.Unlock()

	if !p.gatewaysSynced || !p.endpointSlicesSynced {
		return 0, nil
	}
	if p.minSyncPeriod > 0 && !p.lastSync.IsZero() {
		if remaining := p.minSyncPeriod - time.Since(p.lastSync); remaining > 0 {
			if p.syncPending {
				return remaining, nil
			}
			return 0, nil
		}
	}

	ruleset := p.desiredRulesetLocked()
	if p.applyRules {
		if err := p.applier.Apply(ctx, ruleset); err != nil {
			return 0, err
		}
	}
	p.lastSync = time.Now()
	p.syncPending = false
	return 0, nil
}

func (p *Proxier) requestSync() {
	p.lock.Lock()
	p.syncPending = true
	p.lock.Unlock()

	select {
	case p.resyncCh <- struct{}{}:
	default:
	}
}

func (p *Proxier) desiredRulesetLocked() *rulesetSpec {
	frontends := p.gatewayTracker.Snapshot()
	endpoints := p.endpointTracker.Snapshot()

	keys := make([]agentstate.FrontendKey, 0, len(frontends))
	for key := range frontends {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b agentstate.FrontendKey) int {
		if a.String() < b.String() {
			return -1
		}
		if a.String() > b.String() {
			return 1
		}
		return 0
	})

	spec := &rulesetSpec{
		tableName:  p.tableName,
		family:     tableFamily,
		baseChains: defaultBaseChains(),
	}

	for _, key := range keys {
		frontend := frontends[key]
		clusterIP := copyIPv4(frontend.Address)
		servicePortNumber, ok := toUint16Port(frontend.Port)
		if !ok || clusterIP == nil {
			continue
		}

		serviceKey := key.String()
		serviceChainName := chainNameFor(serviceKey)
		backends := backendChainSpecsForEndpoints(serviceChainName, endpoints[key])
		if len(backends) == 0 {
			continue
		}

		spec.serviceChains = append(spec.serviceChains, serviceChainSpec{
			serviceKey:      serviceKey,
			chainName:       serviceChainName,
			dispatchMapName: dispatchMapNameFor(serviceChainName),
			clusterIP:       clusterIP,
			servicePort:     servicePortNumber,
			backendChains:   backends,
		})
	}

	return spec
}

func backendChainSpecsForEndpoints(serviceChainName string, endpoints []agentstate.Endpoint) []backendChainSpec {
	backends := make([]backendChainSpec, 0, len(endpoints))
	for _, endpoint := range endpoints {
		backendIP := copyIPv4(net.ParseIP(endpoint.IP))
		backendPort, ok := toUint16Port(endpoint.Port)
		if !ok || backendIP == nil {
			continue
		}

		backends = append(backends, backendChainSpec{
			chainName:   backendChainNameFor(serviceChainName, len(backends)),
			backendIP:   backendIP,
			backendPort: backendPort,
		})
	}
	return backends
}

func chainNameFor(in string) string {
	return hashedName("svc_", in)
}

func dispatchMapNameFor(serviceChainName string) string {
	return "map_" + serviceChainName[len("svc_"):]
}

func backendChainNameFor(serviceChainName string, index int) string {
	return fmt.Sprintf("%s_be%d", serviceChainName, index)
}

func hashedName(prefix, in string) string {
	sum := sha256.Sum256([]byte(in))
	return prefix + hex.EncodeToString(sum[:8])
}

func toUint16Port(port int) (uint16, bool) {
	if port <= 0 || port > 65535 {
		return 0, false
	}
	return uint16(port), true
}
