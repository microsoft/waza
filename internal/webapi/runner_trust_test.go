package webapi

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeRunnerHTTPPeerAndAuthority(t *testing.T) {
	for _, control := range []struct {
		host, peer string
		allowed    bool
	}{
		{"127.0.0.1:3000", "127.0.0.1:4000", true},
		{"localhost:3000", "[::1]:4000", true},
		{"[::1]:3000", "[::ffff:127.0.0.1]:4000", true},
		{"[::ffff:127.0.0.1]:3000", "127.0.0.1:4000", true},
		{"attacker.example", "127.0.0.1:4000", false},
		{"localhost.attacker.example", "127.0.0.1:4000", false},
		{"127.0.0.1.attacker.example", "127.0.0.1:4000", false},
		{"127.0.0.1", "192.0.2.1:4000", false},
		{"localhost", "localhost:4000", false},
		{"localhost", "invalid-peer", false},
		{"attacker@localhost", "127.0.0.1:4000", false},
		{"localhost/path", "127.0.0.1:4000", false},
		{"localhost?query", "127.0.0.1:4000", false},
		{"localhost#fragment", "127.0.0.1:4000", false},
		{"localhost:invalid", "127.0.0.1:4000", false},
	} {
		t.Run(control.host+"/"+control.peer, func(t *testing.T) {
			request := httptest.NewRequest("POST", "http://localhost/api/v1/runner/run", nil)
			request.Host, request.RemoteAddr = control.host, control.peer
			err := authorizeNativeRunnerHTTP(request)
			if control.allowed {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}
