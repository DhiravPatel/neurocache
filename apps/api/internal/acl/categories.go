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

	// phase 6: completionist polish — joke + niche admin.
	"LOLWUT": {[]string{CatFast, CatConnection}},

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

	// NeuroCache AI-stack — embedding cache, conversation management,
	// versioned prompt templates. All scoped under @ai so a single
	// `+@ai` rule grants the whole AI surface.
	"EMB.CACHE_SET":  {[]string{CatAI, CatWrite, CatFast}},
	"EMB.CACHE_GET":  {[]string{CatAI, CatRead, CatFast}},
	"EMB.CACHE_DEL":  {[]string{CatAI, CatWrite, CatFast}},
	"EMB.STATS":      {[]string{CatAI, CatRead, CatFast}},
	"EMB.PURGE":      {[]string{CatAI, CatWrite, CatFast}},
	"EMB.COST":       {[]string{CatAI, CatWrite, CatFast}},

	"CONV.APPEND":    {[]string{CatAI, CatWrite, CatFast}},
	"CONV.WINDOW":    {[]string{CatAI, CatRead, CatFast}},
	"CONV.SUMMARIZE": {[]string{CatAI, CatWrite, CatFast}},
	"CONV.RESET":     {[]string{CatAI, CatWrite, CatFast}},
	"CONV.LEN":       {[]string{CatAI, CatRead, CatFast}},
	"CONV.LIST":      {[]string{CatAI, CatRead, CatFast}},

	"PROMPT.SET":     {[]string{CatAI, CatWrite, CatFast}},
	"PROMPT.GET":     {[]string{CatAI, CatRead, CatFast}},
	"PROMPT.RENDER":  {[]string{CatAI, CatRead, CatFast}},
	"PROMPT.LIST":    {[]string{CatAI, CatRead, CatFast}},
	"PROMPT.DELETE":  {[]string{CatAI, CatWrite, CatFast}},
	"PROMPT.VERSIONS": {[]string{CatAI, CatRead, CatFast}},

	// Phase 11 — extended AI-ops primitives. Every command in @ai so
	// `+@ai` grants the whole AI surface; +@write grants the subset
	// that mutates state.
	"AGENT.CALL":    {[]string{CatAI, CatRead, CatFast}},
	"AGENT.STORE":   {[]string{CatAI, CatWrite, CatFast}},
	"AGENT.PROFILE": {[]string{CatAI, CatWrite, CatFast}},
	"AGENT.FORGET":  {[]string{CatAI, CatWrite, CatFast}},
	"AGENT.STATS":   {[]string{CatAI, CatRead, CatFast}},
	"AGENT.PURGE":   {[]string{CatAI, CatWrite, CatFast}},

	"STREAM.SET":    {[]string{CatAI, CatWrite, CatFast}},
	"STREAM.GET":    {[]string{CatAI, CatRead, CatFast}},
	"STREAM.REPLAY": {[]string{CatAI, CatRead, CatFast}},
	"STREAM.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"STREAM.PURGE":  {[]string{CatAI, CatWrite, CatFast}},
	"STREAM.STATS":  {[]string{CatAI, CatRead, CatFast}},

	"COST.BUDGET": {[]string{CatAI, CatWrite, CatFast}},
	"COST.CHARGE": {[]string{CatAI, CatWrite, CatFast}},
	"COST.USAGE":  {[]string{CatAI, CatRead, CatFast}},
	"COST.RESET":  {[]string{CatAI, CatWrite, CatFast}},
	"COST.LIST":   {[]string{CatAI, CatRead, CatFast}},

	"SHADOW.PUT":    {[]string{CatAI, CatWrite, CatFast}},
	"SHADOW.GET":    {[]string{CatAI, CatRead, CatFast}},
	"SHADOW.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"SHADOW.STATS":  {[]string{CatAI, CatRead, CatFast}},

	"PERSONA.SET":    {[]string{CatAI, CatWrite, CatFast}},
	"PERSONA.GET":    {[]string{CatAI, CatRead, CatFast}},
	"PERSONA.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"PERSONA.FORGET": {[]string{CatAI, CatWrite, CatFast}},

	"SAFE.SET":    {[]string{CatAI, CatWrite, CatFast}},
	"SAFE.CHECK":  {[]string{CatAI, CatRead, CatFast}},
	"SAFE.INJECT": {[]string{CatAI, CatRead, CatFast}},
	"SAFE.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"SAFE.PURGE":  {[]string{CatAI, CatWrite, CatFast}},
	"SAFE.STATS":  {[]string{CatAI, CatRead, CatFast}},

	"LINEAGE.RECORD":    {[]string{CatAI, CatWrite, CatFast}},
	"LINEAGE.LIST":      {[]string{CatAI, CatRead, CatFast}},
	"LINEAGE.SOURCES":   {[]string{CatAI, CatRead, CatFast}},
	"LINEAGE.CONSUMERS": {[]string{CatAI, CatRead, CatFast}},
	"LINEAGE.FORGET":    {[]string{CatAI, CatWrite, CatFast}},
	"LINEAGE.STATS":     {[]string{CatAI, CatRead, CatFast}},

	"SLO.SET":      {[]string{CatAI, CatWrite, CatFast}},
	"SLO.SNAPSHOT": {[]string{CatAI, CatRead, CatFast}},
	"SLO.RESET":    {[]string{CatAI, CatWrite, CatFast}},

	"AB.DEFINE":  {[]string{CatAI, CatWrite, CatFast}},
	"AB.ASSIGN":  {[]string{CatAI, CatRead, CatFast}},
	"AB.EXPOSE":  {[]string{CatAI, CatWrite, CatFast}},
	"AB.RECORD":  {[]string{CatAI, CatWrite, CatFast}},
	"AB.STATS":   {[]string{CatAI, CatRead, CatFast}},
	"AB.LIST":    {[]string{CatAI, CatRead, CatFast}},
	"AB.RESET":   {[]string{CatAI, CatWrite, CatFast}},
	"AB.DELETE":  {[]string{CatAI, CatWrite, CatFast}},

	"GRAPH.LINK":      {[]string{CatAI, CatWrite, CatFast}},
	"GRAPH.UNLINK":    {[]string{CatAI, CatWrite, CatFast}},
	"GRAPH.NEIGHBORS": {[]string{CatAI, CatRead, CatFast}},
	"GRAPH.IN":        {[]string{CatAI, CatRead, CatFast}},
	"GRAPH.PATH":      {[]string{CatAI, CatRead, CatFast}},
	"GRAPH.SUBJECTS":  {[]string{CatAI, CatRead, CatFast}},
	"GRAPH.STATS":     {[]string{CatAI, CatRead, CatFast}},

	"SCHEDULE.AT":     {[]string{CatAI, CatWrite, CatFast}},
	"SCHEDULE.IN":     {[]string{CatAI, CatWrite, CatFast}},
	"SCHEDULE.CANCEL": {[]string{CatAI, CatWrite, CatFast}},
	"SCHEDULE.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"SCHEDULE.STATS":  {[]string{CatAI, CatRead, CatFast}},

	"EVENT.APPEND":  {[]string{CatAI, CatWrite, CatFast}},
	"EVENT.PROJECT": {[]string{CatAI, CatWrite, CatFast}},
	"EVENT.READ":    {[]string{CatAI, CatRead, CatFast}},
	"EVENT.RANGE":   {[]string{CatAI, CatRead, CatFast}},
	"EVENT.LEN":     {[]string{CatAI, CatRead, CatFast}},

	"POLICY.ALLOW": {[]string{CatAI, CatRead, CatFast}},
	"POLICY.SET":   {[]string{CatAI, CatWrite, CatFast}},
	"POLICY.PURGE": {[]string{CatAI, CatWrite, CatFast}},
	"POLICY.STATS": {[]string{CatAI, CatRead, CatFast}},

	"INFER.GENERATE": {[]string{CatAI, CatWrite, CatSlow}},
	"INFER.FORGET":   {[]string{CatAI, CatWrite, CatFast}},
	"INFER.PURGE":    {[]string{CatAI, CatWrite, CatFast}},
	"INFER.STATS":    {[]string{CatAI, CatRead, CatFast}},
	"INFER.DEFAULT":  {[]string{CatAI, CatWrite, CatFast}},

	"MCP.TOOLS":     {[]string{CatAI, CatRead, CatFast}},
	"MCP.RESOURCES": {[]string{CatAI, CatRead, CatFast}},
	"MCP.CALL":      {[]string{CatAI, CatWrite, CatSlow}},
	"MCP.READ":      {[]string{CatAI, CatRead, CatFast}},
	"MCP.RPC":       {[]string{CatAI, CatWrite, CatSlow}},

	// Phase 12 — uniqueness primitives.
	"CHURN.TAG":        {[]string{CatAI, CatWrite, CatFast}},
	"CHURN.UNTAG":      {[]string{CatAI, CatWrite, CatFast}},
	"CHURN.INVALIDATE": {[]string{CatAI, CatWrite, CatFast}},
	"CHURN.KEYS":       {[]string{CatAI, CatRead, CatFast}},
	"CHURN.TAGS_OF":    {[]string{CatAI, CatRead, CatFast}},
	"CHURN.TAGS":       {[]string{CatAI, CatRead, CatFast}},
	"CHURN.STATS":      {[]string{CatAI, CatRead, CatFast}},

	"WORKER.ENQUEUE": {[]string{CatAI, CatWrite, CatFast}},
	"WORKER.DEQUEUE": {[]string{CatAI, CatWrite, CatFast}},
	"WORKER.ACK":     {[]string{CatAI, CatWrite, CatFast}},
	"WORKER.NACK":    {[]string{CatAI, CatWrite, CatFast}},
	"WORKER.STATS":   {[]string{CatAI, CatRead, CatFast}},
	"WORKER.DLQ":     {[]string{CatAI, CatRead, CatFast}},
	"WORKER.REQUEUE": {[]string{CatAI, CatWrite, CatFast}},
	"WORKER.CONFIG":  {[]string{CatAI, CatWrite, CatFast}},
	"WORKER.QUEUES":  {[]string{CatAI, CatRead, CatFast}},

	"FLAG.SET":    {[]string{CatAI, CatWrite, CatFast}},
	"FLAG.IS":     {[]string{CatAI, CatRead, CatFast}},
	"FLAG.ALLOW":  {[]string{CatAI, CatWrite, CatFast}},
	"FLAG.DENY":   {[]string{CatAI, CatWrite, CatFast}},
	"FLAG.GET":    {[]string{CatAI, CatRead, CatFast}},
	"FLAG.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"FLAG.DELETE": {[]string{CatAI, CatWrite, CatFast}},

	"AUDIT.LOG":       {[]string{CatAI, CatWrite, CatFast}},
	"AUDIT.QUERY":     {[]string{CatAI, CatRead, CatFast}},
	"AUDIT.COUNT":     {[]string{CatAI, CatRead, CatFast}},
	"AUDIT.STATS":     {[]string{CatAI, CatRead, CatFast}},
	"AUDIT.RETENTION": {[]string{CatAI, CatWrite, CatFast}},

	"TRACE.START":    {[]string{CatAI, CatWrite, CatFast}},
	"TRACE.END":      {[]string{CatAI, CatWrite, CatFast}},
	"TRACE.ANNOTATE": {[]string{CatAI, CatWrite, CatFast}},
	"TRACE.GET":      {[]string{CatAI, CatRead, CatFast}},
	"TRACE.LIST":     {[]string{CatAI, CatRead, CatFast}},
	"TRACE.FORGET":   {[]string{CatAI, CatWrite, CatFast}},
	"TRACE.STATS":    {[]string{CatAI, CatRead, CatFast}},

	"DOC.INIT":   {[]string{CatAI, CatWrite, CatFast}},
	"DOC.APPLY":  {[]string{CatAI, CatWrite, CatFast}},
	"DOC.GET":    {[]string{CatAI, CatRead, CatFast}},
	"DOC.SINCE":  {[]string{CatAI, CatRead, CatFast}},
	"DOC.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"DOC.FORGET": {[]string{CatAI, CatWrite, CatFast}},

	"OBSERVE.REGISTER": {[]string{CatAdmin, CatWrite, CatFast}},
	"OBSERVE.INC":      {[]string{CatAdmin, CatWrite, CatFast}},
	"OBSERVE.SET":      {[]string{CatAdmin, CatWrite, CatFast}},
	"OBSERVE.RENDER":   {[]string{CatAdmin, CatRead, CatFast}},

	"KV.SUBSCRIBE":   {[]string{CatPubSub, CatRead, CatFast}},
	"KV.UNSUBSCRIBE": {[]string{CatPubSub, CatRead, CatFast}},

	// Phase 13 — resilience & coordination primitives. CIRCUIT and
	// SAGA are control-plane writes (state-machine transitions);
	// CRDT is data-plane state. All gate under @ai so a single
	// `+@ai` rule grants the whole Phase 13 surface.
	"CIRCUIT.CONFIG": {[]string{CatAI, CatWrite, CatFast}},
	"CIRCUIT.RECORD": {[]string{CatAI, CatWrite, CatFast}},
	"CIRCUIT.CHECK":  {[]string{CatAI, CatWrite, CatFast}},
	"CIRCUIT.STATE":  {[]string{CatAI, CatRead, CatFast}},
	"CIRCUIT.TRIP":   {[]string{CatAI, CatWrite, CatFast}},
	"CIRCUIT.RESET":  {[]string{CatAI, CatWrite, CatFast}},
	"CIRCUIT.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"CIRCUIT.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"CIRCUIT.STATS":  {[]string{CatAI, CatRead, CatFast}},

	"SAGA.START":    {[]string{CatAI, CatWrite, CatFast}},
	"SAGA.STEP":     {[]string{CatAI, CatWrite, CatFast}},
	"SAGA.COMPLETE": {[]string{CatAI, CatWrite, CatFast}},
	"SAGA.FAIL":     {[]string{CatAI, CatWrite, CatFast}},
	"SAGA.STATUS":   {[]string{CatAI, CatRead, CatFast}},
	"SAGA.LIST":     {[]string{CatAI, CatRead, CatFast}},
	"SAGA.FORGET":   {[]string{CatAI, CatWrite, CatFast}},
	"SAGA.STATS":    {[]string{CatAI, CatRead, CatFast}},

	"CRDT.GINCR":     {[]string{CatAI, CatWrite, CatFast}},
	"CRDT.GVALUE":    {[]string{CatAI, CatRead, CatFast}},
	"CRDT.PNINCR":    {[]string{CatAI, CatWrite, CatFast}},
	"CRDT.PNVALUE":   {[]string{CatAI, CatRead, CatFast}},
	"CRDT.SADD":      {[]string{CatAI, CatWrite, CatFast}},
	"CRDT.SREM":      {[]string{CatAI, CatWrite, CatFast}},
	"CRDT.SMEMBERS":  {[]string{CatAI, CatRead, CatFast}},
	"CRDT.SISMEMBER": {[]string{CatAI, CatRead, CatFast}},
	"CRDT.LWWSET":    {[]string{CatAI, CatWrite, CatFast}},
	"CRDT.LWWGET":    {[]string{CatAI, CatRead, CatFast}},
	"CRDT.MERGE":     {[]string{CatAI, CatWrite, CatFast}},
	"CRDT.STATE":     {[]string{CatAI, CatRead, CatFast}},
	"CRDT.TYPE":      {[]string{CatAI, CatRead, CatFast}},
	"CRDT.LIST":      {[]string{CatAI, CatRead, CatFast}},
	"CRDT.FORGET":    {[]string{CatAI, CatWrite, CatFast}},
	"CRDT.STATS":     {[]string{CatAI, CatRead, CatFast}},
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
