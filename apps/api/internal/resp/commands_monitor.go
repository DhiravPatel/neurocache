package resp

// monitorCmd promotes this connection into MONITOR mode. Once
// promoted, the conn replies +OK and then receives one push frame per
// command executed cluster-wide. The connection cannot dispatch
// further commands — exactly the Redis behaviour.
func (c *conn) monitorCmd() {
	if c.monitorID != 0 {
		writeSimple(c.bw, "OK")
		return
	}
	id, ch := c.eng.Monitor.Subscribe()
	c.monitorID = id
	writeSimple(c.bw, "OK")
	_ = c.bw.Flush()

	go func() {
		for line := range ch {
			c.writeMu.Lock()
			_, _ = c.bw.WriteString(line)
			_ = c.bw.Flush()
			c.writeMu.Unlock()
		}
	}()
}
