package resp

// writeCommands enumerates every command that mutates the keyspace.
// After a successful dispatch we hand these to the engine's AOF appender
// so durability tracks the live state. Pure reads are excluded.
var writeCommands = map[string]bool{
	// strings / keys
	"SET": true, "SETNX": true, "SETEX": true, "PSETEX": true,
	"GETSET": true, "APPEND": true, "SETRANGE": true,
	"MSET": true, "MSETNX": true,
	"INCR": true, "DECR": true, "INCRBY": true, "DECRBY": true, "INCRBYFLOAT": true,
	"DEL": true, "UNLINK": true,
	"EXPIRE": true, "PEXPIRE": true, "EXPIREAT": true, "PEXPIREAT": true, "PERSIST": true,
	"RENAME": true, "RENAMENX": true,
	"FLUSHDB": true, "FLUSHALL": true,

	// lists
	"LPUSH": true, "RPUSH": true, "LPUSHX": true, "RPUSHX": true,
	"LPOP": true, "RPOP": true, "LSET": true, "LREM": true, "LTRIM": true,
	"LINSERT": true, "RPOPLPUSH": true,

	// hashes
	"HSET": true, "HMSET": true, "HSETNX": true,
	"HDEL": true, "HINCRBY": true, "HINCRBYFLOAT": true,

	// sets
	"SADD": true, "SREM": true, "SPOP": true, "SMOVE": true,
	"SINTERSTORE": true, "SUNIONSTORE": true, "SDIFFSTORE": true,

	// sorted sets
	"ZADD": true, "ZREM": true, "ZINCRBY": true,
	"ZPOPMIN": true, "ZPOPMAX": true,

	// bitmaps
	"SETBIT": true, "BITOP": true,

	// HyperLogLog
	"PFADD": true, "PFMERGE": true,

	// streams
	"XADD": true, "XDEL": true, "XTRIM": true,

	// geo
	"GEOADD": true,

	// NeuroCache AI-native
	"SEMANTIC_SET": true, "CACHE_LLM": true, "MEMORY_ADD": true,
}

// isWriteCommand returns true if the command mutates the keyspace.
// Called from the dispatch path after a successful reply so that AOF
// captures writes without bogging down reads.
func isWriteCommand(cmd string) bool { return writeCommands[cmd] }
