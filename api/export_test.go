package api

import "time"

// setPollIntervalForTest shortens job polling so tests do not wait on the
// production two-second cadence.
func (c *Client) setPollIntervalForTest(interval time.Duration) {
	c.pollInterval = interval
}
