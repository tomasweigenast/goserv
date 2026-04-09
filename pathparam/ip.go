package pathparam

import (
	"fmt"
	"net"
)

type IPDecoder struct{}

func (IPDecoder) Zero() any { return net.IP{} }
func (IPDecoder) Decode(raw string) (any, error) {
	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, fmt.Errorf("not a valid IP address")
	}
	return ip, nil
}
