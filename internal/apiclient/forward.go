package apiclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
)

// Forwarded is one call passed through from a browser.
//
// It exists because two parts of the account flow cannot be server-rendered:
// `navigator.credentials` runs in the browser and so does the Push API. The
// backend is not publicly exposed, so the browser posts to the frontend and the
// frontend hands it on.
//
// The body is opaque on purpose. A WebAuthn answer is a signed structure —
// anything reshaped on the way through is something the signature no longer
// covers — and a proxy that parsed it would be a second place where the
// protocol is understood, to fall out of step with the first.
type Forwarded struct {
	Method string
	Path   string
	Query  string

	Body []byte

	// Session is the reader's own token, attached as a bearer header.
	Session string

	// From is the reader's address, so the backend's limiter bounds readers
	// rather than bounding the service in front of them.
	From string
}

// maxForwardBytes bounds what a proxied call may answer with. These are small
// JSON objects; a large one is a bug or an attack, not a page.
const maxForwardBytes = 256 << 10

// Forward performs a proxied call and returns the answer with its status.
//
// The status comes back rather than being turned into an error, because the
// caller is a proxy: a 422 from the backend is the answer the page needs to
// see, not a failure of the proxy.
func (c *Client) Forward(ctx context.Context, call Forwarded) ([]byte, int, error) {
	var reader io.Reader
	if len(call.Body) > 0 {
		reader = bytes.NewReader(call.Body)
	}

	target := c.baseURL + call.Path
	if call.Query != "" {
		target += "?" + call.Query
	}

	req, err := http.NewRequestWithContext(ctx, call.Method, target, reader)
	if err != nil {
		return nil, 0, fmt.Errorf("build forwarded request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if call.Session != "" {
		req.Header.Set("Authorization", "Bearer "+call.Session)
	}
	if call.From != "" {
		req.Header.Set("X-Forwarded-For", call.From)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("call backend %s: %w", call.Path, err)
	}
	defer resp.Body.Close() //nolint:errcheck

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxForwardBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("read the backend's answer: %w", err)
	}
	return answer, resp.StatusCode, nil
}
