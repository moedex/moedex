package servecmd

import (
	"fmt"
	"net"
	"strings"
)

func validateBindSecurity(addr, token string, allowInsecure bool) error {
	if addr == "" || token != "" || allowInsecure {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid bind address %q: %w", addr, err)
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing tokenless non-loopback bind %q; configure MOEDEX_AUTH_TOKEN/--auth-token or acknowledge exposure with --allow-insecure", addr)
}
