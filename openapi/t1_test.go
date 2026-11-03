package openapi

import (
	"os"
	"testing"

	"github.com/alrusov/config"
)

//----------------------------------------------------------------------------------------------------------------------------//

func Test1(t *testing.T) {
	// TODO
}

//----------------------------------------------------------------------------------------------------------------------------//

func TestServerURL(t *testing.T) {
	hostname, _ := os.Hostname()

	tests := []struct {
		name     string
		oaCfg    Config
		httpCfg  config.Listener
		expected string
	}{
		{"ipv4", Config{}, config.Listener{Addr: "127.0.0.1:8080"}, "http://127.0.0.1:8080"},
		{"empty host", Config{}, config.Listener{Addr: ":8080"}, "http://" + hostname + ":8080"},
		{"ipv4 any", Config{}, config.Listener{Addr: "0.0.0.0:8080"}, "http://" + hostname + ":8080"},
		{"ipv6", Config{}, config.Listener{Addr: "[::1]:8080"}, "http://[::1]:8080"},
		{"ipv6 any", Config{}, config.Listener{Addr: "[::]:8080"}, "http://" + hostname + ":8080"},
		{"no port", Config{}, config.Listener{Addr: "localhost"}, "http://localhost:80"},
		{"ipv6 no port", Config{}, config.Listener{Addr: "::1"}, "http://[::1]:80"},
		{"https default port", Config{}, config.Listener{Addr: "localhost", SSLCombinedPem: "x.pem"}, "https://localhost:443"},
		{"cfg host and port", Config{Host: "example.com", Port: 9000}, config.Listener{Addr: "127.0.0.1:8080"}, "http://example.com:9000"},
		{"cfg host only", Config{Host: "example.com"}, config.Listener{Addr: "127.0.0.1:8080"}, "http://example.com:8080"},
		{"cfg ipv6 host", Config{Host: "::1", Port: 9000}, config.Listener{}, "http://[::1]:9000"},
		{"cfg bracketed ipv6 host", Config{Host: "[::1]", Port: 9000}, config.Listener{}, "http://[::1]:9000"},
		{"cfg protocol", Config{Protocol: "https"}, config.Listener{Addr: "localhost"}, "https://localhost:443"},
		{"cfg server", Config{Server: "http://srv/api"}, config.Listener{Addr: "127.0.0.1:8080"}, "http://srv/api"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			proc := &processor{
				oaCfg:   &tc.oaCfg,
				httpCfg: &tc.httpCfg,
			}

			got, err := proc.serverURL()
			if err != nil {
				t.Fatal(err)
			}

			if got != tc.expected {
				t.Errorf(`got "%s", expected "%s"`, got, tc.expected)
			}
		})
	}
}

//----------------------------------------------------------------------------------------------------------------------------//
