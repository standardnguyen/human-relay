package api

import "net/http"

// TransportForTest exposes the request transport so tests can assert it never
// consults proxy environment variables.
func TransportForTest(c *Client) *http.Transport { return c.hc.Transport.(*http.Transport) }
