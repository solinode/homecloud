package lambda_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/homecloudhq/homecloud/cli/internal/core"
)

// An HTTP_PROXY integration must not be a way to read loopback services (the
// HomeCloud API, MinIO), the host provider's metadata service or the Docker
// bridge: the API is callable by anyone, so whoever can create an integration
// would otherwise get a read-through proxy into the host.
func TestAPIGatewayProxyRefusesInternalTargets(t *testing.T) {
	h, l := gwHarness(t, false)
	l.HTTP = core.SafeClient(0, 0) // what the server uses
	secret := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("internal-secret")) }))
	defer secret.Close()

	for _, target := range []string{secret.URL, "http://169.254.169.254/latest/meta-data/", "http://[::ffff:127.0.0.1]:1/", "http://[::1]:1/", "http://0.0.0.0:1/"} {
		api := h.AWSJSON(t, "apigatewayv2", "create-api", "--name", "p", "--protocol-type", "HTTP", "--target", target)
		id := api["ApiId"].(string)
		code, body := gwCall(t, h, api["ApiEndpoint"].(string), "GET", "/", nil)
		if code == http.StatusOK || body == "internal-secret" {
			t.Fatalf("%s: proxied to an internal address: %d %q (api %s)", target, code, body, id)
		}
	}
}
