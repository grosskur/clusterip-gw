//go:build linux

package nftables

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"

	upstreamnftables "github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const (
	preroutingChainName = "prerouting"
	outputChainName     = "output"
)

type rulesetSpec struct {
	tableName     string
	family        upstreamnftables.TableFamily
	baseChains    []baseChainSpec
	serviceChains []serviceChainSpec
}

type baseChainSpec struct {
	name      string
	hook      upstreamnftables.ChainHook
	priority  upstreamnftables.ChainPriority
	chainType upstreamnftables.ChainType
	policy    upstreamnftables.ChainPolicy
}

type serviceChainSpec struct {
	serviceKey      string
	chainName       string
	dispatchMapName string
	clusterIP       net.IP
	servicePort     uint16
	backendChains   []backendChainSpec
}

type backendChainSpec struct {
	chainName   string
	backendIP   net.IP
	backendPort uint16
}

func defaultBaseChains() []baseChainSpec {
	return []baseChainSpec{
		{
			name:      preroutingChainName,
			hook:      *upstreamnftables.ChainHookPrerouting,
			priority:  *upstreamnftables.ChainPriorityNATDest,
			chainType: upstreamnftables.ChainTypeNAT,
			policy:    upstreamnftables.ChainPolicyAccept,
		},
		{
			name:      outputChainName,
			hook:      *upstreamnftables.ChainHookOutput,
			priority:  *upstreamnftables.ChainPriorityNATDest,
			chainType: upstreamnftables.ChainTypeNAT,
			policy:    upstreamnftables.ChainPolicyAccept,
		},
	}
}

type rulesetApplier interface {
	Apply(ctx context.Context, spec *rulesetSpec) error
}

type nftConn interface {
	DelTable(t *upstreamnftables.Table)
	AddTable(t *upstreamnftables.Table) *upstreamnftables.Table
	AddChain(c *upstreamnftables.Chain) *upstreamnftables.Chain
	AddSet(s *upstreamnftables.Set, vals []upstreamnftables.SetElement) error
	AddRule(r *upstreamnftables.Rule) *upstreamnftables.Rule
	Flush() error
}

type connFactory interface {
	New() (nftConn, error)
}

type realConnFactory struct{}

func (realConnFactory) New() (nftConn, error) {
	return upstreamnftables.New()
}

type netlinkApplier struct {
	connFactory connFactory
}

func (a *netlinkApplier) Apply(ctx context.Context, spec *rulesetSpec) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	err := a.applyOnce(ctx, spec, true)
	if err == nil || !isTableMissingError(err) {
		return err
	}

	return a.applyOnce(ctx, spec, false)
}

func (a *netlinkApplier) applyOnce(ctx context.Context, spec *rulesetSpec, deleteTable bool) error {
	if spec == nil {
		return fmt.Errorf("ruleset spec must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	conn, err := a.connFactory.New()
	if err != nil {
		return fmt.Errorf("open nftables conn: %w", err)
	}

	if err := queueRuleset(conn, spec, deleteTable); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("flush nftables ruleset: %w", err)
	}

	return nil
}

func queueRuleset(conn nftConn, spec *rulesetSpec, deleteTable bool) error {
	table := &upstreamnftables.Table{
		Name:   spec.tableName,
		Family: spec.family,
	}
	if deleteTable {
		conn.DelTable(table)
	}
	table = conn.AddTable(table)

	baseChains := make(map[string]*upstreamnftables.Chain, len(spec.baseChains))
	for _, chainSpec := range spec.baseChains {
		hook := chainSpec.hook
		priority := chainSpec.priority
		policy := chainSpec.policy
		baseChains[chainSpec.name] = conn.AddChain(&upstreamnftables.Chain{
			Name:     chainSpec.name,
			Table:    table,
			Hooknum:  upstreamnftables.ChainHookRef(hook),
			Priority: upstreamnftables.ChainPriorityRef(priority),
			Type:     chainSpec.chainType,
			Policy:   &policy,
		})
	}

	serviceChains := make(map[string]*upstreamnftables.Chain, len(spec.serviceChains))
	backendChains := make(map[string]*upstreamnftables.Chain)
	dispatchMaps := make(map[string]*upstreamnftables.Set, len(spec.serviceChains))
	for _, service := range spec.serviceChains {
		serviceChains[service.chainName] = conn.AddChain(&upstreamnftables.Chain{
			Name:  service.chainName,
			Table: table,
		})
		for _, backend := range service.backendChains {
			backendChains[backend.chainName] = conn.AddChain(&upstreamnftables.Chain{
				Name:  backend.chainName,
				Table: table,
			})
		}
		dispatchMap := &upstreamnftables.Set{
			Table:    table,
			Name:     service.dispatchMapName,
			KeyType:  upstreamnftables.TypeInteger,
			DataType: upstreamnftables.TypeVerdict,
			IsMap:    true,
		}
		if err := conn.AddSet(dispatchMap, serviceDispatchMapElements(service)); err != nil {
			return fmt.Errorf("add dispatch map %s: %w", service.dispatchMapName, err)
		}
		dispatchMaps[service.chainName] = dispatchMap
	}

	for _, service := range spec.serviceChains {
		conn.AddRule(&upstreamnftables.Rule{
			Table: table,
			Chain: baseChains[preroutingChainName],
			Exprs: serviceJumpExprs(service),
		})
	}
	for _, service := range spec.serviceChains {
		conn.AddRule(&upstreamnftables.Rule{
			Table: table,
			Chain: baseChains[outputChainName],
			Exprs: serviceJumpExprs(service),
		})
	}
	for _, service := range spec.serviceChains {
		conn.AddRule(&upstreamnftables.Rule{
			Table: table,
			Chain: serviceChains[service.chainName],
			Exprs: serviceDispatchExprs(service, dispatchMaps[service.chainName]),
		})
	}
	for _, service := range spec.serviceChains {
		for _, backend := range service.backendChains {
			conn.AddRule(&upstreamnftables.Rule{
				Table: table,
				Chain: backendChains[backend.chainName],
				Exprs: backendDNATExprs(backend),
			})
		}
	}

	return nil
}

func serviceJumpExprs(service serviceChainSpec) []expr.Any {
	return []expr.Any{
		&expr.Payload{
			DestRegister: 1,
			Base:         expr.PayloadBaseNetworkHeader,
			Offset:       16,
			Len:          4,
		},
		&expr.Cmp{
			Op:       expr.CmpOpEq,
			Register: 1,
			Data:     append(net.IP(nil), service.clusterIP...),
		},
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{
			Op:       expr.CmpOpEq,
			Register: 1,
			Data:     []byte{unix.IPPROTO_TCP},
		},
		&expr.Payload{
			DestRegister: 1,
			Base:         expr.PayloadBaseTransportHeader,
			Offset:       2,
			Len:          2,
		},
		&expr.Cmp{
			Op:       expr.CmpOpEq,
			Register: 1,
			Data:     binaryutil.BigEndian.PutUint16(service.servicePort),
		},
		&expr.Verdict{
			Kind:  expr.VerdictJump,
			Chain: service.chainName,
		},
	}
}

func serviceDispatchExprs(service serviceChainSpec, dispatchMap *upstreamnftables.Set) []expr.Any {
	return []expr.Any{
		&expr.Numgen{
			Register: 1,
			Type:     unix.NFT_NG_RANDOM,
			Modulus:  uint32(len(service.backendChains)),
			Offset:   0,
		},
		&expr.Lookup{
			SourceRegister: 1,
			DestRegister:   0,
			IsDestRegSet:   true,
			SetName:        dispatchMap.Name,
			SetID:          dispatchMap.ID,
		},
	}
}

func serviceDispatchMapElements(service serviceChainSpec) []upstreamnftables.SetElement {
	elements := make([]upstreamnftables.SetElement, 0, len(service.backendChains))
	for i, backend := range service.backendChains {
		elements = append(elements, upstreamnftables.SetElement{
			Key: binaryutil.NativeEndian.PutUint32(uint32(i)),
			VerdictData: &expr.Verdict{
				Kind:  expr.VerdictJump,
				Chain: backend.chainName,
			},
		})
	}
	return elements
}

func backendDNATExprs(backend backendChainSpec) []expr.Any {
	return []expr.Any{
		&expr.Immediate{
			Register: 1,
			Data:     append(net.IP(nil), backend.backendIP...),
		},
		&expr.Immediate{
			Register: 2,
			Data:     binaryutil.BigEndian.PutUint16(backend.backendPort),
		},
		&expr.NAT{
			Type:        expr.NATTypeDestNAT,
			Family:      uint32(tableFamily),
			RegAddrMin:  1,
			RegProtoMin: 2,
		},
	}
}

func copyIPv4(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	ipv4 := ip.To4()
	if ipv4 == nil {
		return nil
	}
	return append(net.IP(nil), ipv4...)
}

func isTableMissingError(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOENT)
}
