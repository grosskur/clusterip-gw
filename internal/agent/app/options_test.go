package app

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/pflag"
)

func TestDefaultOptionsAreValid(t *testing.T) {
	opts := NewOptions()
	if err := opts.Validate(); err != nil {
		t.Fatalf("Validate(NewOptions()) returned error: %v", err)
	}
}

func TestValidateRejectsIPv6(t *testing.T) {
	opts := NewOptions()
	opts.HealthzBindAddress = "[::]:10256"

	if err := opts.Validate(); err == nil {
		t.Fatal("expected IPv6 healthz bind address to fail validation")
	}
}

func TestValidateRejectsInvalidBindAddresses(t *testing.T) {
	testCases := []struct {
		name string
		addr string
	}{
		{name: "out of range port", addr: "127.0.0.1:99999"},
		{name: "missing port", addr: "127.0.0.1:"},
		{name: "empty host", addr: ":10256"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			opts := NewOptions()
			opts.HealthzBindAddress = tc.addr

			if err := opts.Validate(); err == nil {
				t.Fatalf("expected %q to fail validation", tc.addr)
			}
		})
	}
}

func TestValidateAllowsValidIPv4BindAddresses(t *testing.T) {
	opts := NewOptions()
	opts.HealthzBindAddress = "127.0.0.1:10256"
	opts.MetricsBindAddress = "0.0.0.0:10249"

	if err := opts.Validate(); err != nil {
		t.Fatalf("expected valid IPv4 bind addresses to pass validation: %v", err)
	}
}

func TestFlagsPopulateKubeAPIOptions(t *testing.T) {
	opts := NewOptions()
	fs := pflag.NewFlagSet("agent", pflag.ContinueOnError)
	opts.AddFlags(fs)

	if err := fs.Parse([]string{
		"--kube-api-accept-content-types=application/json",
		"--kube-api-content-type=application/json",
		"--kube-api-qps=12.5",
		"--kube-api-burst=20",
		"--nftables-sync-period=45s",
	}); err != nil {
		t.Fatalf("parse flags: %v", err)
	}
	if err := opts.Validate(); err != nil {
		t.Fatalf("validate options: %v", err)
	}

	if opts.KubeAPIAcceptContentTypes != "application/json" {
		t.Fatalf("expected accept content types override, got %q", opts.KubeAPIAcceptContentTypes)
	}
	if opts.KubeAPIContentType != "application/json" {
		t.Fatalf("expected content type override, got %q", opts.KubeAPIContentType)
	}
	if opts.KubeAPIQPS != 12.5 {
		t.Fatalf("expected qps 12.5, got %v", opts.KubeAPIQPS)
	}
	if opts.KubeAPIBurst != 20 {
		t.Fatalf("expected burst 20, got %d", opts.KubeAPIBurst)
	}
	if opts.NFTablesSyncPeriod.String() != "45s" {
		t.Fatalf("expected nftables sync period 45s, got %v", opts.NFTablesSyncPeriod)
	}
}

func TestDurationFlagRejectsInvalidValue(t *testing.T) {
	opts := NewOptions()
	fs := pflag.NewFlagSet("agent", pflag.ContinueOnError)
	opts.AddFlags(fs)

	err := fs.Parse([]string{"--config-sync-period=not-a-duration"})
	if err == nil {
		t.Fatal("expected invalid duration to fail parsing")
	}
	if !strings.Contains(err.Error(), "invalid argument") {
		t.Fatalf("expected invalid argument error, got %v", err)
	}
}

func TestParseArgsReturnsHelp(t *testing.T) {
	var stdout bytes.Buffer

	opts, err := parseArgs([]string{"--help"}, &stdout)
	if err != nil {
		t.Fatalf("expected help to return nil error, got %v", err)
	}
	if opts != nil {
		t.Fatalf("expected nil options on help, got %#v", opts)
	}

	output := stdout.String()
	if !strings.Contains(output, agentCommandDescription) {
		t.Fatalf("expected help output to contain description, got %q", output)
	}
	if !strings.Contains(output, "Usage:\n  "+agentCommandName+" [flags]") {
		t.Fatalf("expected help output to contain usage, got %q", output)
	}
}

func TestParseArgsRejectsPositionalArguments(t *testing.T) {
	_, err := parseArgs([]string{"unexpected"}, nil)
	if err == nil {
		t.Fatal("expected positional arguments to fail parsing")
	}
	if !strings.Contains(err.Error(), "unexpected positional arguments") {
		t.Fatalf("expected positional argument error, got %v", err)
	}
}

func TestParseArgsRejectsRemovedFlags(t *testing.T) {
	for _, arg := range []string{"--hostname-override=node-a", "--bind-address=10.0.0.1"} {
		if _, err := parseArgs([]string{arg}, nil); err == nil {
			t.Fatalf("expected removed flag %q to fail parsing", arg)
		}
	}
}
