package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ErrStreamClosed is returned by Events when the relay ends the stream.
var ErrStreamClosed = errors.New("event stream closed")

// Events streams GET /events until it ends. onOpen runs once the relay has
// accepted the stream, and onFrame once per message. The request sends no
// Authorization header and the read has no deadline, however quiet the stream
// is; the call ends when the relay closes the connection or ctx is cancelled. A
// message that is not valid JSON is still delivered, as an empty Frame, since
// the caller refetches on every frame anyway.
func (c *Client) Events(ctx context.Context, onOpen func(), onFrame func(Frame)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/events"), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.sse.Do(req)
	if err != nil {
		return transportError(ctx, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
		return httpError(resp, b)
	}
	onOpen()

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var data []string
	flush := func() {
		if len(data) == 0 {
			return
		}
		var f Frame
		if json.Unmarshal([]byte(strings.Join(data, "\n")), &f) != nil {
			f = Frame{}
		}
		data = data[:0]
		onFrame(f)
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrStreamClosed, err)
	}
	return ErrStreamClosed
}

// Backoff bounds the delay between reconnect attempts.
type Backoff struct{ Min, Max time.Duration }

// DefaultBackoff retries after 1s, doubling up to 15s.
var DefaultBackoff = Backoff{Min: time.Second, Max: 15 * time.Second}

// Watch keeps the event stream connected until ctx is cancelled, reconnecting
// with exponential backoff. onState(true) fires when a stream opens and
// onState(false) when the connection is lost or cannot be made; repeated
// failures report false once. Every open is a point where the caller refetches,
// because it has no record of what happened while it was disconnected.
func (c *Client) Watch(ctx context.Context, bo Backoff, onState func(up bool), onFrame func(Frame)) {
	if bo.Min <= 0 {
		bo = DefaultBackoff
	}
	if bo.Max < bo.Min {
		bo.Max = bo.Min
	}
	delay := bo.Min
	var last *bool
	report := func(up bool) {
		if last != nil && *last == up {
			return
		}
		v := up
		last = &v
		onState(up)
	}
	for ctx.Err() == nil {
		opened := false
		c.Events(ctx, func() { opened = true; report(true) }, onFrame)
		if ctx.Err() != nil {
			return
		}
		report(false)
		if opened {
			delay = bo.Min
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if !opened {
			delay *= 2
			if delay > bo.Max {
				delay = bo.Max
			}
		}
	}
}
