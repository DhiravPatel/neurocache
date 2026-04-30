// Package acl implements Redis-style access control: named users with
// password lists, command/category permissions, key-pattern permissions,
// pub/sub channel patterns, and an audit log. Users and the default
// anonymous user are persisted to a file so restarts preserve grants.
package acl

// Category labels every command so ACL rules can scope permissions at a
// coarser granularity than individual command names. Matches Redis'
// canonical list — names (including the '@' prefix in rules) are stable.
const (
	CatKeyspace    = "keyspace"
	CatRead        = "read"
	CatWrite       = "write"
	CatSet         = "set"
	CatSortedSet   = "sortedset"
	CatList        = "list"
	CatHash        = "hash"
	CatString      = "string"
	CatBitmap      = "bitmap"
	CatHyperLogLog = "hyperloglog"
	CatGeo         = "geo"
	CatStream      = "stream"
	CatPubSub      = "pubsub"
	CatAdmin       = "admin"
	CatFast        = "fast"
	CatSlow        = "slow"
	CatBlocking    = "blocking"
	CatDangerous   = "dangerous"
	CatConnection  = "connection"
	CatTransaction = "transaction"
	CatScripting   = "scripting"
	CatAI          = "ai" // NeuroCache-specific: SEMANTIC_*, CACHE_LLM*, MEMORY_*
)

// AllCategories is the canonical list returned by ACL CAT with no args.
var AllCategories = []string{
	CatKeyspace, CatRead, CatWrite, CatSet, CatSortedSet, CatList, CatHash,
	CatString, CatBitmap, CatHyperLogLog, CatGeo, CatStream, CatPubSub,
	CatAdmin, CatFast, CatSlow, CatBlocking, CatDangerous, CatConnection,
	CatTransaction, CatScripting, CatAI,
}

// commandInfo captures the ACL-relevant metadata for one command: its
// category memberships and whether it's "dangerous" (FLUSHALL, DEBUG,
// etc.). Lookup is case-insensitive; callers should uppercase first.
type commandInfo struct {
	cats []string
}

// registry is the command -> category table. Keep it centralized so a
// new command must declare its categories explicitly, preventing drift
// between the dispatcher and the ACL permission checks.
var registry = map[string]commandInfo{
	// connection / server
	"PING":    {[]string{CatFast, CatConnection}},
	"ECHO":    {[]string{CatFast, CatConnection}},
	"SELECT":  {[]string{CatFast, CatConnection}},
	"HELLO":   {[]string{CatFast, CatConnection}},
	"AUTH":    {[]string{CatFast, CatConnection}},
	"QUIT":    {[]string{CatFast, CatConnection}},
	"RESET":   {[]string{CatFast, CatConnection}},
	"CLIENT":  {[]string{CatSlow, CatConnection, CatAdmin}},
	"COMMAND": {[]string{CatSlow, CatConnection}},
	"DBSIZE":  {[]string{CatFast, CatKeyspace}},
	"TIME":    {[]string{CatFast}},
	"INFO":    {[]string{CatSlow, CatAdmin, CatDangerous}},
	"DEBUG":   {[]string{CatSlow, CatAdmin, CatDangerous}},
	"ACL":     {[]string{CatSlow, CatAdmin, CatDangerous}},

	// keys
	"DEL": {[]string{CatKeyspace, CatWrite, CatSlow}}, "UNLINK": {[]string{CatKeyspace, CatWrite, CatSlow}},
	"EXISTS":    {[]string{CatKeyspace, CatRead, CatFast}},
	"TYPE":      {[]string{CatKeyspace, CatRead, CatFast}},
	"EXPIRE":    {[]string{CatKeyspace, CatWrite, CatFast}},
	"PEXPIRE":   {[]string{CatKeyspace, CatWrite, CatFast}},
	"EXPIREAT":  {[]string{CatKeyspace, CatWrite, CatFast}},
	"PEXPIREAT": {[]string{CatKeyspace, CatWrite, CatFast}},
	"PERSIST":   {[]string{CatKeyspace, CatWrite, CatFast}},
	"TTL":       {[]string{CatKeyspace, CatRead, CatFast}},
	"PTTL":      {[]string{CatKeyspace, CatRead, CatFast}},
	"KEYS":      {[]string{CatKeyspace, CatRead, CatSlow, CatDangerous}},
	"SCAN":      {[]string{CatKeyspace, CatRead, CatSlow}},
	"RANDOMKEY": {[]string{CatKeyspace, CatRead, CatFast}},
	"RENAME":    {[]string{CatKeyspace, CatWrite, CatSlow}},
	"RENAMENX":  {[]string{CatKeyspace, CatWrite, CatFast}},
	"COPY":      {[]string{CatKeyspace, CatWrite, CatSlow}},
	"DUMP":      {[]string{CatKeyspace, CatRead, CatSlow}},
	"RESTORE":   {[]string{CatKeyspace, CatWrite, CatSlow, CatDangerous}},
	"OBJECT":    {[]string{CatKeyspace, CatRead, CatSlow}},
	"FLUSHDB":   {[]string{CatKeyspace, CatWrite, CatSlow, CatDangerous}},
	"FLUSHALL":  {[]string{CatKeyspace, CatWrite, CatSlow, CatDangerous}},
	"MEMORY":    {[]string{CatSlow, CatAdmin}},

	// strings
	"SET": {[]string{CatString, CatWrite, CatFast}}, "SETNX": {[]string{CatString, CatWrite, CatFast}},
	"SETEX": {[]string{CatString, CatWrite, CatFast}}, "PSETEX": {[]string{CatString, CatWrite, CatFast}},
	"GET": {[]string{CatString, CatRead, CatFast}}, "GETSET": {[]string{CatString, CatWrite, CatFast}},
	"MGET": {[]string{CatString, CatRead, CatFast}}, "MSET": {[]string{CatString, CatWrite, CatSlow}},
	"MSETNX": {[]string{CatString, CatWrite, CatSlow}},
	"APPEND": {[]string{CatString, CatWrite, CatFast}}, "STRLEN": {[]string{CatString, CatRead, CatFast}},
	"GETRANGE": {[]string{CatString, CatRead, CatFast}}, "SUBSTR": {[]string{CatString, CatRead, CatFast}},
	"SETRANGE": {[]string{CatString, CatWrite, CatFast}},
	"INCR":     {[]string{CatString, CatWrite, CatFast}}, "DECR": {[]string{CatString, CatWrite, CatFast}},
	"INCRBY": {[]string{CatString, CatWrite, CatFast}}, "DECRBY": {[]string{CatString, CatWrite, CatFast}},
	"INCRBYFLOAT": {[]string{CatString, CatWrite, CatFast}},

	// lists
	"LPUSH": {[]string{CatList, CatWrite, CatFast}}, "RPUSH": {[]string{CatList, CatWrite, CatFast}},
	"LPUSHX": {[]string{CatList, CatWrite, CatFast}}, "RPUSHX": {[]string{CatList, CatWrite, CatFast}},
	"LPOP":  {[]string{CatList, CatWrite, CatFast}},
	"RPOP":  {[]string{CatList, CatWrite, CatFast}},
	"BLPOP": {[]string{CatList, CatWrite, CatSlow, CatBlocking}},
	"BRPOP": {[]string{CatList, CatWrite, CatSlow, CatBlocking}},
	"BLMOVE": {[]string{CatList, CatWrite, CatSlow, CatBlocking}},
	"LLEN":   {[]string{CatList, CatRead, CatFast}}, "LINDEX": {[]string{CatList, CatRead, CatFast}},
	"LRANGE": {[]string{CatList, CatRead, CatSlow}}, "LSET": {[]string{CatList, CatWrite, CatFast}},
	"LREM": {[]string{CatList, CatWrite, CatSlow}}, "LTRIM": {[]string{CatList, CatWrite, CatSlow}},
	"LINSERT": {[]string{CatList, CatWrite, CatFast}}, "RPOPLPUSH": {[]string{CatList, CatWrite, CatFast}},

	// hashes
	"HSET": {[]string{CatHash, CatWrite, CatFast}}, "HMSET": {[]string{CatHash, CatWrite, CatFast}},
	"HSETNX": {[]string{CatHash, CatWrite, CatFast}}, "HGET": {[]string{CatHash, CatRead, CatFast}},
	"HMGET": {[]string{CatHash, CatRead, CatFast}}, "HGETALL": {[]string{CatHash, CatRead, CatSlow}},
	"HDEL": {[]string{CatHash, CatWrite, CatFast}}, "HEXISTS": {[]string{CatHash, CatRead, CatFast}},
	"HLEN":  {[]string{CatHash, CatRead, CatFast}}, "HKEYS": {[]string{CatHash, CatRead, CatSlow}},
	"HVALS": {[]string{CatHash, CatRead, CatSlow}}, "HINCRBY": {[]string{CatHash, CatWrite, CatFast}},
	"HINCRBYFLOAT": {[]string{CatHash, CatWrite, CatFast}},
	"HSTRLEN":      {[]string{CatHash, CatRead, CatFast}},
	"HSCAN":        {[]string{CatHash, CatRead, CatSlow}},

	// sets
	"SADD": {[]string{CatSet, CatWrite, CatFast}}, "SREM": {[]string{CatSet, CatWrite, CatFast}},
	"SISMEMBER": {[]string{CatSet, CatRead, CatFast}}, "SMEMBERS": {[]string{CatSet, CatRead, CatSlow}},
	"SCARD": {[]string{CatSet, CatRead, CatFast}}, "SPOP": {[]string{CatSet, CatWrite, CatFast}},
	"SRANDMEMBER": {[]string{CatSet, CatRead, CatFast}}, "SMOVE": {[]string{CatSet, CatWrite, CatFast}},
	"SINTER": {[]string{CatSet, CatRead, CatSlow}}, "SUNION": {[]string{CatSet, CatRead, CatSlow}},
	"SDIFF":       {[]string{CatSet, CatRead, CatSlow}},
	"SINTERSTORE": {[]string{CatSet, CatWrite, CatSlow}}, "SUNIONSTORE": {[]string{CatSet, CatWrite, CatSlow}},
	"SDIFFSTORE": {[]string{CatSet, CatWrite, CatSlow}},
	"SSCAN":      {[]string{CatSet, CatRead, CatSlow}},

	// sorted sets
	"ZADD": {[]string{CatSortedSet, CatWrite, CatFast}}, "ZSCORE": {[]string{CatSortedSet, CatRead, CatFast}},
	"ZREM": {[]string{CatSortedSet, CatWrite, CatFast}}, "ZCARD": {[]string{CatSortedSet, CatRead, CatFast}},
	"ZINCRBY": {[]string{CatSortedSet, CatWrite, CatFast}}, "ZRANK": {[]string{CatSortedSet, CatRead, CatFast}},
	"ZREVRANK": {[]string{CatSortedSet, CatRead, CatFast}},
	"ZRANGE":   {[]string{CatSortedSet, CatRead, CatSlow}}, "ZREVRANGE": {[]string{CatSortedSet, CatRead, CatSlow}},
	"ZRANGEBYSCORE": {[]string{CatSortedSet, CatRead, CatSlow}}, "ZREVRANGEBYSCORE": {[]string{CatSortedSet, CatRead, CatSlow}},
	"ZCOUNT":   {[]string{CatSortedSet, CatRead, CatFast}},
	"ZPOPMIN":  {[]string{CatSortedSet, CatWrite, CatFast}}, "ZPOPMAX": {[]string{CatSortedSet, CatWrite, CatFast}},
	"BZPOPMIN": {[]string{CatSortedSet, CatWrite, CatSlow, CatBlocking}},
	"BZPOPMAX": {[]string{CatSortedSet, CatWrite, CatSlow, CatBlocking}},
	"ZSCAN":    {[]string{CatSortedSet, CatRead, CatSlow}},

	// bitmaps
	"SETBIT": {[]string{CatBitmap, CatWrite, CatFast}}, "GETBIT": {[]string{CatBitmap, CatRead, CatFast}},
	"BITCOUNT": {[]string{CatBitmap, CatRead, CatSlow}}, "BITPOS": {[]string{CatBitmap, CatRead, CatSlow}},
	"BITOP": {[]string{CatBitmap, CatWrite, CatSlow}},

	// HyperLogLog
	"PFADD": {[]string{CatHyperLogLog, CatWrite, CatFast}}, "PFCOUNT": {[]string{CatHyperLogLog, CatRead, CatSlow}},
	"PFMERGE": {[]string{CatHyperLogLog, CatWrite, CatSlow}},

	// streams
	"XADD": {[]string{CatStream, CatWrite, CatFast}}, "XLEN": {[]string{CatStream, CatRead, CatFast}},
	"XRANGE": {[]string{CatStream, CatRead, CatSlow}}, "XREVRANGE": {[]string{CatStream, CatRead, CatSlow}},
	"XDEL":  {[]string{CatStream, CatWrite, CatFast}}, "XTRIM": {[]string{CatStream, CatWrite, CatSlow}},
	"XREAD": {[]string{CatStream, CatRead, CatSlow, CatBlocking}},
	"XGROUP": {[]string{CatStream, CatWrite, CatSlow}}, "XREADGROUP": {[]string{CatStream, CatWrite, CatSlow, CatBlocking}},
	"XACK": {[]string{CatStream, CatWrite, CatFast}}, "XPENDING": {[]string{CatStream, CatRead, CatSlow}},
	"XCLAIM":     {[]string{CatStream, CatWrite, CatSlow}},
	"XAUTOCLAIM": {[]string{CatStream, CatWrite, CatSlow}},
	"XINFO":      {[]string{CatStream, CatRead, CatSlow}},

	// geo
	"GEOADD":  {[]string{CatGeo, CatWrite, CatFast}},
	"GEOPOS":  {[]string{CatGeo, CatRead, CatFast}},
	"GEODIST": {[]string{CatGeo, CatRead, CatFast}},
	"GEOSEARCH": {[]string{CatGeo, CatRead, CatSlow}},
	"GEOHASH":   {[]string{CatGeo, CatRead, CatFast}},

	// pub/sub
	"SUBSCRIBE": {[]string{CatPubSub, CatSlow}}, "UNSUBSCRIBE": {[]string{CatPubSub, CatSlow}},
	"PSUBSCRIBE": {[]string{CatPubSub, CatSlow}}, "PUNSUBSCRIBE": {[]string{CatPubSub, CatSlow}},
	"PUBLISH": {[]string{CatPubSub, CatFast}}, "PUBSUB": {[]string{CatPubSub, CatSlow}},

	// transactions
	"MULTI":   {[]string{CatTransaction, CatFast}},
	"EXEC":    {[]string{CatTransaction, CatSlow}},
	"DISCARD": {[]string{CatTransaction, CatFast}},
	"WATCH":   {[]string{CatTransaction, CatFast}},
	"UNWATCH": {[]string{CatTransaction, CatFast}},

	// persistence / admin
	"SAVE":         {[]string{CatAdmin, CatDangerous, CatSlow}},
	"BGSAVE":       {[]string{CatAdmin, CatSlow}},
	"BGREWRITEAOF": {[]string{CatAdmin, CatSlow}},
	"LASTSAVE":     {[]string{CatAdmin, CatFast}},

	// introspection / latency / slowlog
	"SLOWLOG": {[]string{CatAdmin, CatSlow}},
	"LATENCY": {[]string{CatAdmin, CatSlow}},

	// scripting
	"EVAL": {[]string{CatScripting, CatSlow, CatDangerous}}, "EVALSHA": {[]string{CatScripting, CatSlow, CatDangerous}},
	"SCRIPT": {[]string{CatScripting, CatSlow, CatDangerous}},

	// phase 1: driver-critical fillers — registered so COMMAND DOCS,
	// ACL CAT, and key-spec lookups treat them as first-class.
	"TOUCH":            {[]string{CatKeyspace, CatRead, CatFast}},
	"EXPIRETIME":       {[]string{CatKeyspace, CatRead, CatFast}},
	"PEXPIRETIME":      {[]string{CatKeyspace, CatRead, CatFast}},
	"LMOVE":            {[]string{CatList, CatWrite, CatFast}},
	"ZMSCORE":          {[]string{CatSortedSet, CatRead, CatFast}},
	"ZRANDMEMBER":      {[]string{CatSortedSet, CatRead, CatSlow}},
	"ZREMRANGEBYRANK":  {[]string{CatSortedSet, CatWrite, CatSlow}},
	"ZREMRANGEBYSCORE": {[]string{CatSortedSet, CatWrite, CatSlow}},
	"ZREMRANGEBYLEX":   {[]string{CatSortedSet, CatWrite, CatSlow}},
	"GEOSEARCHSTORE":   {[]string{CatGeo, CatWrite, CatSlow}},
	"EVAL_RO":          {[]string{CatScripting, CatSlow, CatRead}},
	"EVALSHA_RO":       {[]string{CatScripting, CatSlow, CatRead}},

	// phase 2: hash field TTL extras
	"HGETDEL":     {[]string{CatHash, CatWrite, CatFast}},
	"HGETEX":      {[]string{CatHash, CatWrite, CatFast}},
	"HSETEX":      {[]string{CatHash, CatWrite, CatFast}},
	"HEXPIRETIME": {[]string{CatHash, CatRead, CatFast}},
	"HPEXPIRETIME": {[]string{CatHash, CatRead, CatFast}},

	// phase 2: deprecated geo family
	"GEORADIUS":              {[]string{CatGeo, CatWrite, CatSlow}},
	"GEORADIUS_RO":           {[]string{CatGeo, CatRead, CatSlow}},
	"GEORADIUSBYMEMBER":      {[]string{CatGeo, CatWrite, CatSlow}},
	"GEORADIUSBYMEMBER_RO":   {[]string{CatGeo, CatRead, CatSlow}},

	// phase 3: HOTKEYS — admin-class observability command.
	"HOTKEYS": {[]string{CatAdmin, CatRead, CatFast}},

	// phase 5: vector-set type. Sits in its own implicit category
	// (CatRead/CatWrite + CatSlow for the index-touching ops).
	"VADD":        {[]string{CatWrite, CatSlow}},
	"VREM":        {[]string{CatWrite, CatFast}},
	"VSIM":        {[]string{CatRead, CatSlow}},
	"VEMB":        {[]string{CatRead, CatFast}},
	"VSETATTR":    {[]string{CatWrite, CatFast}},
	"VGETATTR":    {[]string{CatRead, CatFast}},
	"VDELATTR":    {[]string{CatWrite, CatFast}},
	"VLINKS":      {[]string{CatRead, CatFast}},
	"VINFO":       {[]string{CatRead, CatFast}},
	"VCARD":       {[]string{CatRead, CatFast}},
	"VDIM":        {[]string{CatRead, CatFast}},
	"VRANDMEMBER": {[]string{CatRead, CatFast}},
	"VSCAN":       {[]string{CatRead, CatSlow}},

	// phase 4: niche 8.x-pattern additions
	"DELEX":   {[]string{CatString, CatWrite, CatFast}},
	"DIGEST":  {[]string{CatKeyspace, CatRead, CatFast}},
	"MSETEX":  {[]string{CatString, CatWrite, CatSlow}},
	"XACKDEL": {[]string{CatStream, CatWrite, CatFast}},
	"XDELEX":  {[]string{CatStream, CatWrite, CatFast}},
	"XCFGSET": {[]string{CatStream, CatWrite, CatFast}},

	// NeuroCache AI-native
	"SEMANTIC_SET": {[]string{CatAI, CatWrite, CatFast}},
	"SEMANTIC_GET": {[]string{CatAI, CatRead, CatFast}},
	"CACHE_LLM":    {[]string{CatAI, CatWrite, CatFast}},
	"CACHE_LLM_GET": {[]string{CatAI, CatRead, CatFast}},
	"CACHE_LLM_STATS": {[]string{CatAI, CatRead, CatFast}},
	"MEMORY_ADD":   {[]string{CatAI, CatWrite, CatFast}},
	"MEMORY_QUERY": {[]string{CatAI, CatRead, CatFast}},
	"MEMORY_LIST":  {[]string{CatAI, CatRead, CatFast}},
}

// CategoriesFor returns the categories a command belongs to. Unknown
// commands return nil so the permission check falls back to "ALL"
// gating — a conservative default.
func CategoriesFor(cmd string) []string {
	if ci, ok := registry[cmd]; ok {
		return ci.cats
	}
	return nil
}

// CommandsInCategory enumerates every command whose category set
// includes cat. Used by ACL CAT <name>.
func CommandsInCategory(cat string) []string {
	out := []string{}
	for name, ci := range registry {
		for _, c := range ci.cats {
			if c == cat {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// AllCommands is the full list of known command names (for ACL CAT
// without args and for bootstrap population of the "default" user).
func AllCommands() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	return out
}
