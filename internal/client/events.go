package client

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/remy/yamo/internal/library"
)

// Events streams catalogue changes from the server.
//
// The channel closes when the context is cancelled or the connection drops. A
// caller that cares about not missing anything should refetch on reconnect
// rather than assume continuity: the server drops events for a subscriber that
// has fallen behind, in preference to blocking the write that produced one.
func (c *Client) Events(ctx context.Context) (<-chan library.Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/v1/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &ErrServerUnreachable{Base: c.base, Err: err}
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, decodeError(resp)
	}

	out := make(chan library.Event, 32)
	go func() {
		defer close(out)
		defer resp.Body.Close()

		sc := bufio.NewScanner(resp.Body)
		// An event carrying a long list of changed ids can be large; the
		// default line limit would truncate it into unparseable JSON.
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

		for sc.Scan() {
			line := sc.Text()
			// Only the data line matters: the event name is repeated inside it.
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			var e library.Event
			if json.Unmarshal([]byte(payload), &e) != nil {
				continue
			}
			select {
			case out <- e:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}
