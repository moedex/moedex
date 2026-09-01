package servecmd

import "testing"

func TestValidateBindSecurity(t *testing.T) {
	for _, test := range []struct {
		name          string
		addr          string
		token         string
		allowInsecure bool
		wantError     bool
	}{
		{name: "disabled", addr: ""},
		{name: "ipv4 loopback", addr: "127.0.0.1:8080"},
		{name: "ipv6 loopback", addr: "[::1]:8080"},
		{name: "localhost", addr: "localhost:8080"},
		{name: "wildcard", addr: ":8080", wantError: true},
		{name: "ipv4 wildcard", addr: "0.0.0.0:8080", wantError: true},
		{name: "external", addr: "10.0.0.8:8080", wantError: true},
		{name: "protected", addr: "0.0.0.0:8080", token: "secret"},
		{name: "acknowledged", addr: "0.0.0.0:8080", allowInsecure: true},
		{name: "invalid", addr: "not-an-address", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateBindSecurity(test.addr, test.token, test.allowInsecure)
			if (err != nil) != test.wantError {
				t.Fatalf("error = %v, wantError=%v", err, test.wantError)
			}
		})
	}
}
