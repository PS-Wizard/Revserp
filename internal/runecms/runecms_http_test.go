package runecms

import (
	"context"
	"testing"
	"time"
)

func TestStrictEndpointHTTPSchemeAndPorts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pub := "93.184.216.34"
	good := []string{
		"http://" + pub + "/mcp",
		"http://" + pub + ":80/mcp",
		"http://" + pub + ":8080/mcp",
		"https://" + pub + "/mcp",
		"https://" + pub + ":443/mcp",
		"https://" + pub + ":8443/mcp",
	}
	for _, ep := range good {
		if _, err := validateStrictEndpoint(ctx, ep); err != nil {
			t.Errorf("endpoint %q: unexpected reject: %v (code=%s)", ep, err, ErrorCode(err))
		}
	}
	bad := []string{
		"ftp://" + pub + "/mcp",
		"ws://" + pub + "/mcp",
		"http://" + pub + ":443/mcp",
		"http://" + pub + ":8443/mcp",
		"http://" + pub + ":8081/mcp",
		"http://" + pub + ":8090/mcp",
		"https://" + pub + ":80/mcp",
		"https://" + pub + ":8080/mcp",
		"https://" + pub + ":8081/mcp",
		"http://user:pass@" + pub + "/mcp",
		"http://" + pub + "/mcp#frag",
		"http://10.0.0.1/mcp",
		"http://127.0.0.1/mcp",
		"http://169.254.169.254/",
		"http://[::1]/",
		"http://[fe80::1]/",
		"http://[ff02::1]/",
		"http://224.0.0.1/",
		"http://203.0.113.1/",
		"http://100.64.0.1/",
		"http://[::ffff:127.0.0.1]/",
	}
	for _, ep := range bad {
		if _, err := validateStrictEndpoint(ctx, ep); ErrorCode(err) != string(CodeInvalidEndpoint) {
			t.Errorf("endpoint %q: code = %q, want invalid_endpoint", ep, ErrorCode(err))
		}
	}
}

func TestSafeDialPortGate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, port := range []string{"80", "8080", "443", "8443"} {
		_, err := safeDialContext(ctx, "tcp", "127.0.0.1:"+port)
		if err == nil || err.Error() != "runecms: blocked host" {
			t.Errorf("127.0.0.1:%s: err = %v, want blocked-host (port must pass)", port, err)
		}
	}
	for _, port := range []string{"8081", "8090", "8000", "3000", "22"} {
		_, err := safeDialContext(ctx, "tcp", "127.0.0.1:"+port)
		if err == nil || err.Error() != "runecms: blocked port" {
			t.Errorf("127.0.0.1:%s: err = %v, want blocked-port", port, err)
		}
	}
}
