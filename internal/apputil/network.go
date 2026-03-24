package apputil

import (
	"fmt"
	"net"
	"strconv"
)

// ValidateIPv4Host validates an IPv4 host value.
func ValidateIPv4Host(host string, allowEmpty bool) error {
	if host == "" {
		if allowEmpty {
			return nil
		}
		return fmt.Errorf("must not be empty")
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("must be an IP address")
	}
	if ip.To4() == nil {
		return fmt.Errorf("only IPv4 is supported in this scaffold")
	}
	return nil
}

// ValidateIPv4HostPort validates an IPv4 host:port value.
func ValidateIPv4HostPort(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}
	if port == "" {
		return fmt.Errorf("port must not be empty")
	}
	if _, err := strconv.ParseUint(port, 10, 16); err != nil {
		return fmt.Errorf("port must be a valid integer between 0 and 65535")
	}
	if host == "::" || host == "[::]" {
		return fmt.Errorf("only IPv4 is supported in this scaffold")
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("host must be an IP address")
	}
	if ip.To4() == nil {
		return fmt.Errorf("only IPv4 is supported in this scaffold")
	}
	return nil
}
