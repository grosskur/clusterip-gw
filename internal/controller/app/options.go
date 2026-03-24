// Package app wires the clusterip-gw-controller CLI and process startup.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"github.com/grosskur/clusterip-gw/internal/apputil"
	"github.com/grosskur/clusterip-gw/internal/kube/clientconfig"
)

const (
	controllerCommandName               = "clusterip-gw-controller"
	controllerCommandDescription        = "Experimental Gateway API controller for clusterip-gw Gateway VIP allocation"
	defaultControllerContentType        = "application/json"
	defaultControllerKubeAPIQPS         = 5
	defaultControllerKubeAPIBurst       = 10
	defaultControllerHealthzBindAddress = "0.0.0.0:10257"
	defaultControllerMetricsBindAddress = "127.0.0.1:10258"
	defaultControllerConfigSyncPeriod   = 15 * time.Minute
)

// Options contains CLI flags and the resulting runtime configuration.
type Options struct {
	Master                    string
	Kubeconfig                string
	KubeAPIAcceptContentTypes string
	KubeAPIContentType        string
	KubeAPIQPS                float32
	KubeAPIBurst              int32
	HealthzBindAddress        string
	MetricsBindAddress        string
	ConfigSyncPeriod          time.Duration
}

// NewOptions returns Options initialized from the default runtime values.
func NewOptions() *Options {
	return &Options{
		KubeAPIContentType: defaultControllerContentType,
		KubeAPIQPS:         defaultControllerKubeAPIQPS,
		KubeAPIBurst:       defaultControllerKubeAPIBurst,
		HealthzBindAddress: defaultControllerHealthzBindAddress,
		MetricsBindAddress: defaultControllerMetricsBindAddress,
		ConfigSyncPeriod:   defaultControllerConfigSyncPeriod,
	}
}

// Execute parses clusterip-gw-controller flags and runs the process.
func Execute(ctx context.Context, args []string, stdout io.Writer) error {
	opts, err := parseArgs(args, stdout)
	if err != nil || opts == nil {
		return err
	}

	return opts.Run(ctx)
}

func parseArgs(args []string, stdout io.Writer) (*Options, error) {
	opts := NewOptions()
	fs := pflag.NewFlagSet(controllerCommandName, pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts.AddFlags(fs)

	if stdout == nil {
		stdout = io.Discard
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			_, _ = fmt.Fprintf(stdout, "%s\n\nUsage:\n  %s [flags]\n\nFlags:\n%s", controllerCommandDescription, fs.Name(), fs.FlagUsagesWrapped(80))
			return nil, nil
		}
		return nil, err
	}

	if fs.NArg() > 0 {
		return nil, fmt.Errorf("unexpected positional arguments: %s", strings.Join(fs.Args(), " "))
	}

	if err := opts.Validate(); err != nil {
		return nil, err
	}

	return opts, nil
}

// AddFlags registers controller flags on the provided flag set.
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Master, "master", o.Master, "Address of the Kubernetes API server.")
	fs.StringVar(&o.Kubeconfig, "kubeconfig", o.Kubeconfig, "Path to a kubeconfig file.")
	fs.StringVar(&o.KubeAPIAcceptContentTypes, "kube-api-accept-content-types", o.KubeAPIAcceptContentTypes, "Accept header sent to the Kubernetes API.")
	fs.StringVar(&o.KubeAPIContentType, "kube-api-content-type", o.KubeAPIContentType, "Content type used for Kubernetes API requests.")
	fs.Float32Var(&o.KubeAPIQPS, "kube-api-qps", o.KubeAPIQPS, "Queries per second allowed against the Kubernetes API.")
	fs.Int32Var(&o.KubeAPIBurst, "kube-api-burst", o.KubeAPIBurst, "Burst allowance against the Kubernetes API.")
	fs.StringVar(&o.HealthzBindAddress, "healthz-bind-address", o.HealthzBindAddress, "IPv4 host:port for the health endpoint.")
	fs.StringVar(&o.MetricsBindAddress, "metrics-bind-address", o.MetricsBindAddress, "IPv4 host:port for the metrics endpoint. Empty disables metrics.")
	fs.DurationVar(&o.ConfigSyncPeriod, "config-sync-period", o.ConfigSyncPeriod, "How often Kubernetes configuration is refreshed.")
}

// Validate validates clusterip-gw-controller runtime options.
func (o *Options) Validate() error {
	if o.ConfigSyncPeriod <= 0 {
		return fmt.Errorf("config-sync-period must be greater than 0")
	}
	if err := apputil.ValidateIPv4HostPort(o.HealthzBindAddress); err != nil {
		return fmt.Errorf("healthz-bind-address: %w", err)
	}
	if o.MetricsBindAddress != "" {
		if err := apputil.ValidateIPv4HostPort(o.MetricsBindAddress); err != nil {
			return fmt.Errorf("metrics-bind-address: %w", err)
		}
	}
	if o.KubeAPIQPS < 0 {
		return fmt.Errorf("kube-api-qps must not be negative")
	}
	if o.KubeAPIBurst < 0 {
		return fmt.Errorf("kube-api-burst must not be negative")
	}

	return nil
}

func (o *Options) clientConfig() clientconfig.Options {
	return clientconfig.Options{
		Kubeconfig:         o.Kubeconfig,
		AcceptContentTypes: o.KubeAPIAcceptContentTypes,
		ContentType:        o.KubeAPIContentType,
		QPS:                o.KubeAPIQPS,
		Burst:              o.KubeAPIBurst,
	}
}
