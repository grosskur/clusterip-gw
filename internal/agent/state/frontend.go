package state

import (
	"fmt"
	"net"
)

// Frontend stores the immutable routing data for a frontend.
type Frontend struct {
	Address net.IP
	Port    int
}

// NewFrontend builds a Frontend from the provided data.
func NewFrontend(address net.IP, port int) Frontend {
	return Frontend{Address: address, Port: port}
}

func (info Frontend) String() string {
	return fmt.Sprintf("%s:%d/tcp", info.Address.String(), info.Port)
}
