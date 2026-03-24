package k8sclusteripgw

import (
	"context"
	"fmt"
	"runtime"
	"strconv"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/coremain"
	"github.com/coredns/coredns/plugin"
	clog "github.com/coredns/coredns/plugin/pkg/log"
	"github.com/miekg/dns"
	"k8s.io/client-go/rest"
)

var log = clog.NewWithPlugin(pluginName)

func init() { plugin.Register(pluginName, setup) }

func setup(c *caddy.Controller) error {
	handler, err := parse(c)
	if err != nil {
		return plugin.Error(pluginName, err)
	}

	config, err := rest.InClusterConfig()
	if err != nil {
		return plugin.Error(pluginName, fmt.Errorf("build in-cluster config: %w", err))
	}
	config.UserAgent = fmt.Sprintf(
		"%s/%s git_commit:%s (%s/%s/%s)",
		coremain.CoreName,
		coremain.CoreVersion,
		coremain.GitCommit,
		runtime.GOOS,
		runtime.GOARCH,
		runtime.Version(),
	)

	store, err := newInformerGatewayStore(config)
	if err != nil {
		return plugin.Error(pluginName, err)
	}
	handler.store = store

	var runCtx context.Context
	var cancel context.CancelFunc

	c.OnStartup(func() error {
		runCtx, cancel = context.WithCancel(context.Background())
		go store.Run(runCtx)
		log.Infof("watching Gateways for zone %q", handler.Zones[0])
		return nil
	})
	c.OnShutdown(func() error {
		if cancel != nil {
			cancel()
		}
		return nil
	})

	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler {
		handler.Next = next
		return handler
	})

	return nil
}

func parse(c *caddy.Controller) (*K8sClusterIPGW, error) {
	handler := New(nil, []string{defaultZone}, defaultTTL)

	i := 0
	for c.Next() {
		if i > 0 {
			return nil, plugin.ErrOnce
		}
		i++

		args := c.RemainingArgs()
		switch len(args) {
		case 0:
		case 1:
			handler.Zones = []string{dns.Fqdn(args[0])}
		default:
			return nil, c.ArgErr()
		}

		for c.NextBlock() {
			switch c.Val() {
			case "ttl":
				args := c.RemainingArgs()
				if len(args) != 1 {
					return nil, c.ArgErr()
				}
				ttl, err := strconv.Atoi(args[0])
				if err != nil {
					return nil, err
				}
				if ttl < 0 || ttl > 3600 {
					return nil, c.Errf("ttl must be in range [0, 3600]: %d", ttl)
				}
				handler.TTL = uint32(ttl)
			default:
				return nil, c.Errf("unknown property %q", c.Val())
			}
		}
	}

	return handler, nil
}
