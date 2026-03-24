// Package state contains the reduced frontend/backend tracking model used by clusterip-gw-agent.
package state

import (
	"net"
	"strconv"
)

// Endpoint stores the immutable attributes of a backend endpoint.
type Endpoint struct {
	IP   string
	Port int
}

// NewEndpoint builds an Endpoint for the provided backend data.
func NewEndpoint(ip string, port int) Endpoint {
	return Endpoint{IP: ip, Port: port}
}

func (info Endpoint) String() string {
	return net.JoinHostPort(info.IP, strconv.Itoa(info.Port))
}
