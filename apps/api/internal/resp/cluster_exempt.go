package resp

// clusterRoutingExempt enumerates commands that should never trigger
// MOVED/ASK/CROSSSLOT redirection in cluster mode. These either:
//
//   - have no key arguments (PING, INFO, TIME, …),
//   - are admin-plane commands the operator runs on a specific node
//     (CLUSTER, CONFIG, DEBUG, FAILOVER, REPLICAOF, …),
//   - are connection-control commands (AUTH, SELECT, HELLO, RESET,
//     ASKING, READONLY, READWRITE, CLIENT *).
//
// MIGRATE is *also* exempt because the source-node operator drives it
// even when the slot has already been marked MIGRATING (otherwise we'd
// redirect ourselves into an infinite ASK loop).
var clusterRoutingExempt = map[string]bool{
	"PING": true, "ECHO": true, "TIME": true, "INFO": true,
	"DBSIZE": true, "FLUSHDB": true, "FLUSHALL": true,
	"COMMAND": true, "HELLO": true, "AUTH": true, "ACL": true,
	"QUIT": true, "RESET": true, "SELECT": true,
	"CLIENT": true, "CONFIG": true, "DEBUG": true,
	"OBJECT": true, "MEMORY": true, "SLOWLOG": true, "LATENCY": true,

	// transactions don't redirect — Redis blocks WATCH/MULTI keys at
	// EXEC time when a slot moves; we follow the same model.
	"MULTI": true, "EXEC": true, "DISCARD": true, "WATCH": true, "UNWATCH": true,

	// pub/sub channels are independent of slot ownership.
	"SUBSCRIBE": true, "UNSUBSCRIBE": true, "PSUBSCRIBE": true,
	"PUNSUBSCRIBE": true, "PUBLISH": true, "PUBSUB": true,

	// sharded pub/sub does its own slot routing inside the handler.
	"SSUBSCRIBE": true, "SUNSUBSCRIBE": true, "SPUBLISH": true,

	// MONITOR / FUNCTION / FCALL re-enter the dispatcher per-call;
	// those nested calls do their own routing.
	"MONITOR": true, "FUNCTION": true, "FCALL": true, "FCALL_RO": true,

	// SENTINEL targets a specific node and bypasses slot routing.
	// (CONFIG is already in the connection-control block above.)
	"SENTINEL": true,

	// scripting is keyless from the dispatcher's perspective; the
	// individual redis.call invocations re-enter the routing check.
	"EVAL": true, "EVALSHA": true, "SCRIPT": true,

	// replication + cluster admin
	"REPLICAOF": true, "SLAVEOF": true, "ROLE": true, "WAIT": true,
	"FAILOVER": true, "PSYNC": true, "SYNC": true, "REPLCONF": true,
	"CLUSTER": true, "MIGRATE": true,
	"ASKING": true, "READONLY": true, "READWRITE": true,

	// persistence is an operator action, not a keyspace one.
	"SAVE": true, "BGSAVE": true, "BGREWRITEAOF": true, "LASTSAVE": true,

	// SCAN walks the local keyspace; cluster clients iterate per-node.
	"SCAN": true, "RANDOMKEY": true, "KEYS": true,

	// AI-native — single-node engine state, not slot routed.
	"SEMANTIC_SET": true, "SEMANTIC_GET": true,
	"CACHE_LLM": true, "CACHE_LLM_GET": true, "CACHE_LLM_STATS": true,
	"MEMORY_ADD": true, "MEMORY_QUERY": true, "MEMORY_LIST": true,
}
