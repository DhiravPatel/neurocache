// Package cluster implements Redis-compatible clustering: a 16384-slot
// hash space partitioned across nodes, a gossip bus for membership +
// failure detection, MOVED/ASK redirection on the data path, and
// MIGRATE for live key rebalancing.
//
// The slot calculation is bit-for-bit compatible with Redis (CRC16
// XMODEM polynomial 0x1021 + hashtag substring rule), so a Redis-aware
// client driver can shard against a NeuroCache cluster without any
// changes.
package cluster

// SlotCount is the size of the keyspace hash table — fixed by the
// Redis spec so client libraries can hard-code it.
const SlotCount = 16384

// crc16Table is the precomputed XMODEM (poly 0x1021, no init/xor)
// lookup. Same constants the reference Redis implementation uses.
var crc16Table = func() [256]uint16 {
	var t [256]uint16
	for i := 0; i < 256; i++ {
		c := uint16(i) << 8
		for j := 0; j < 8; j++ {
			if c&0x8000 != 0 {
				c = (c << 1) ^ 0x1021
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// CRC16 computes XMODEM CRC over the input. Used directly by KeySlot
// and exposed for tests + cluster-aware drivers that want to verify
// their own routing.
func CRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc = (crc << 8) ^ crc16Table[byte(crc>>8)^b]
	}
	return crc
}

// KeySlot returns the slot a key hashes into. Honours the {tag}
// extraction rule: if the key contains "{...}" with a non-empty body,
// only the body is hashed — that's how Redis lets you co-locate
// related keys (e.g. {user:7}:profile and {user:7}:cart land together).
func KeySlot(key string) int {
	start := -1
	for i := 0; i < len(key); i++ {
		if key[i] == '{' {
			start = i
			break
		}
	}
	if start >= 0 {
		for i := start + 1; i < len(key); i++ {
			if key[i] == '}' {
				if i-start > 1 {
					return int(CRC16([]byte(key[start+1:i]))) & (SlotCount - 1)
				}
				break
			}
		}
	}
	return int(CRC16([]byte(key))) & (SlotCount - 1)
}
