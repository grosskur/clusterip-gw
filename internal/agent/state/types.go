package state

import (
	"k8s.io/apimachinery/pkg/types"
)

// FrontendKey uniquely identifies a frontend listener within a namespace.
type FrontendKey struct {
	types.NamespacedName
	Listener string
}

func (fk FrontendKey) String() string {
	if fk.Listener == "" {
		return fk.NamespacedName.String()
	}
	return fk.NamespacedName.String() + ":" + fk.Listener
}

// FrontendMap maps a frontend key to its routing data.
type FrontendMap map[FrontendKey]Frontend

// EndpointsMap maps a frontend key to its current endpoints.
type EndpointsMap map[FrontendKey][]Endpoint
