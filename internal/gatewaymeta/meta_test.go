package gatewaymeta

import (
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestManagedByValueIsValidLabelValue(t *testing.T) {
	if errs := validation.IsValidLabelValue(ManagedByValue); len(errs) != 0 {
		t.Fatalf("expected managed-by label value %q to be valid, got %v", ManagedByValue, errs)
	}
}
