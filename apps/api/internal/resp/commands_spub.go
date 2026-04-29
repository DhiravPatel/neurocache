package resp

import (
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/cluster"
	"github.com/dhiravpatel/neurocache/apps/api/internal/pubsub"
)

// Sharded pub/sub implementation. Each channel hashes (via the cluster
// CRC16 keyslot) to one of the 16384 slots — only the node that owns
// that slot delivers messages on it. Within the local node, sharded
// channels live in the same broker as regular pub/sub but namespaced
// under a "__shard__:" prefix so they can't collide with PUBLISH /
// SUBSCRIBE traffic.

const shardChannelPrefix = "__shard__:"

// ssubscribeCmd handles SSUBSCRIBE channel [channel ...]. The reply
// shape mirrors SUBSCRIBE so existing client libraries that treat
// shard pub/sub as "subscribe variant" parse it without changes.
func (c *conn) ssubscribeCmd(args []string) {
	if len(args) == 0 {
		writeError(c.bw, "wrong number of arguments for 'ssubscribe'")
		return
	}
	for _, ch := range args {
		// Cluster mode: refuse SSUBSCRIBE for slots we don't own.
		if c.eng.Cluster != nil && c.eng.Cluster.Enabled() {
			slot := cluster.KeySlot(ch)
			if !c.eng.Cluster.IsOurs(slot) {
				owner := c.eng.Cluster.SlotOwner(slot)
				if owner != nil {
					writeError(c.bw, "MOVED "+itoa(slot)+" "+owner.Addr())
					return
				}
			}
		}
		if _, dup := c.shardSubs[ch]; dup {
			continue
		}
		sub := c.eng.PubSub.Subscribe(shardChannelPrefix + ch)
		c.shardSubs[ch] = sub
		go c.pumpShardSubscription(ch, sub)
		writeValue(c.bw, []any{"ssubscribe", ch, int64(len(c.shardSubs))})
	}
}

// sunsubscribeCmd leaves channels (or all of them when called bare).
func (c *conn) sunsubscribeCmd(args []string) {
	targets := args
	if len(targets) == 0 {
		for ch := range c.shardSubs {
			targets = append(targets, ch)
		}
	}
	if len(targets) == 0 {
		writeValue(c.bw, []any{"sunsubscribe", nil, int64(0)})
		return
	}
	for _, ch := range targets {
		if sub, ok := c.shardSubs[ch]; ok {
			sub.Close()
			delete(c.shardSubs, ch)
		}
		writeValue(c.bw, []any{"sunsubscribe", ch, int64(len(c.shardSubs))})
	}
}

// spublishCmd publishes only on the slot owner. In cluster mode the
// CRC16 of the channel decides which node serves it; non-cluster mode
// just routes to the local broker (single-node degenerate case).
func (c *conn) spublishCmd(args []string) {
	if len(args) < 2 {
		writeError(c.bw, "wrong number of arguments for 'spublish'")
		return
	}
	channel, payload := args[0], args[1]
	if c.eng.Cluster != nil && c.eng.Cluster.Enabled() {
		slot := cluster.KeySlot(channel)
		if !c.eng.Cluster.IsOurs(slot) {
			owner := c.eng.Cluster.SlotOwner(slot)
			if owner != nil {
				writeError(c.bw, "MOVED "+itoa(slot)+" "+owner.Addr())
				return
			}
		}
	}
	n := c.eng.PubSub.Publish(shardChannelPrefix+channel, payload)
	// Cluster gossip fans out to other shard members (replicas of the
	// same master), matching Redis where SPUBLISH reaches every node
	// serving the slot.
	if c.eng.Bus != nil {
		c.eng.Bus.PublishToCluster(shardChannelPrefix+channel, payload)
	}
	writeInt(c.bw, int64(n))
}

// pubsubShardCmd handles PUBSUB SHARDCHANNELS / SHARDNUMSUB.
func (c *conn) pubsubShardCmd(args []string) {
	switch strings.ToUpper(args[0]) {
	case "SHARDCHANNELS":
		pattern := "*"
		if len(args) >= 2 {
			pattern = args[1]
		}
		all := c.eng.PubSub.Channels(shardChannelPrefix + pattern)
		out := make([]string, 0, len(all))
		for _, ch := range all {
			out = append(out, strings.TrimPrefix(ch, shardChannelPrefix))
		}
		writeArray(c.bw, out)
	case "SHARDNUMSUB":
		channels := args[1:]
		prefixed := make([]string, len(channels))
		for i, ch := range channels {
			prefixed[i] = shardChannelPrefix + ch
		}
		counts := c.eng.PubSub.NumSub(prefixed...)
		out := make([]any, 0, 2*len(channels))
		for _, ch := range channels {
			out = append(out, ch, int64(counts[shardChannelPrefix+ch]))
		}
		writeValue(c.bw, out)
	default:
		writeError(c.bw, "unknown PUBSUB subcommand "+args[0])
	}
}

// pumpShardSubscription fans messages from the broker to the client.
// Same shape as the regular pump but emits "smessage" framing.
func (c *conn) pumpShardSubscription(channel string, sub *pubsub.Subscription) {
	for {
		select {
		case <-c.done:
			return
		case m, ok := <-sub.Ch():
			if !ok {
				return
			}
			c.writeMu.Lock()
			writeValue(c.bw, []any{"smessage", channel, m.Payload})
			_ = c.bw.Flush()
			c.writeMu.Unlock()
		}
	}
}

// itoa is a tiny strconv shim — keeps the import list of this file
// tight (most other commands in the package import it themselves).
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	digits := []byte{}
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}
