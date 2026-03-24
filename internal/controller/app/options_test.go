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

func TestDefaultUsesJSONContentType(t *testing.T) {
	opts := NewOptions()
	if opts.KubeAPIContentType != "application/json" {
		t.Fatalf("expected JSON content type by default, got %q", opts.KubeAPIContentType)
	}
}

func TestValidateRejectsNonPositiveSyncPeriod(t *testing.T) {
	opts := NewOptions()
	opts.ConfigSyncPeriod = 0

	if err := opts.Validate(); err == nil {
		t.Fatalf("expected config sync period validation error")
	}
}

func TestValidateRejectsInvalidBindAddresses(t *testing.T) {
	opts := NewOptions()
	opts.HealthzBindAddress = "localhost:10257"

	if err := opts.Validate(); err == nil {
		t.Fatalf("expected validation error")
	}
}

func TestValidateAllowsEmptyMetricsAddress(t *testing.T) {
	opts := NewOptions()
	opts.MetricsBindAddress = ""

	if err := opts.Validate(); err != nil {
		t.Fatalf("expected empty metrics address to be valid: %v", err)
	}
}

func TestFlagsPopulateKubeAPIOptions(t *testing.T) {
	opts := NewOptions()
	fs := pflag.NewFlagSet("controller", pflag.ContinueOnError)
	opts.AddFlags(fs)

	if err := fs.Parse([]string{
		"--kube-api-accept-content-types=application/json",
		"--kube-api-content-type=application/json",
		"--kube-api-qps=7.5",
		"--kube-api-burst=15",
		"--config-sync-period=10m",
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
	if opts.KubeAPIQPS != 7.5 {
		t.Fatalf("expected qps 7.5, got %v", opts.KubeAPIQPS)
	}
	if opts.KubeAPIBurst != 15 {
		t.Fatalf("expected burst 15, got %d", opts.KubeAPIBurst)
	}
	if opts.ConfigSyncPeriod.String() != "10m0s" {
		t.Fatalf("expected config sync period 10m0s, got %v", opts.ConfigSyncPeriod)
	}
}

func TestDurationFlagRejectsInvalidValue(t *testing.T) {
	opts := NewOptions()
	fs := pflag.NewFlagSet("controller", pflag.ContinueOnError)
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
	if !strings.Contains(output, controllerCommandDescription) {
		t.Fatalf("expected help output to contain description, got %q", output)
	}
	if !strings.Contains(output, "Usage:\n  "+controllerCommandName+" [flags]") {
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
