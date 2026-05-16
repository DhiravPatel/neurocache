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

	// REDACT.* — PII redaction with restore tokens.
	"REDACT.SCRUB":          {[]string{CatAI, CatWrite, CatFast}},
	"REDACT.RESTORE":        {[]string{CatAI, CatWrite, CatFast}},
	"REDACT.FORGET":         {[]string{CatAI, CatWrite, CatFast}},
	"REDACT.PATTERN.ADD":    {[]string{CatAI, CatWrite, CatFast}},
	"REDACT.PATTERN.REMOVE": {[]string{CatAI, CatWrite, CatFast}},
	"REDACT.PATTERN.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"REDACT.STATS":          {[]string{CatAI, CatRead, CatFast}},

	// GROUND.* — citation grounding scorer.
	"GROUND.CHECK":          {[]string{CatAI, CatWrite, CatFast}},
	"GROUND.THRESHOLDS":     {[]string{CatAI, CatRead, CatFast}},
	"GROUND.SET_THRESHOLDS": {[]string{CatAI, CatWrite, CatFast}},
	"GROUND.STATS":          {[]string{CatAI, CatRead, CatFast}},

	// CANARY.* — prompt canary deployments with auto-rollback.
	"CANARY.CREATE":      {[]string{CatAI, CatWrite, CatFast}},
	"CANARY.PICK":        {[]string{CatAI, CatRead, CatFast}},
	"CANARY.RECORD":      {[]string{CatAI, CatWrite, CatFast}},
	"CANARY.STATUS":      {[]string{CatAI, CatRead, CatFast}},
	"CANARY.SET_TRAFFIC": {[]string{CatAI, CatWrite, CatFast}},
	"CANARY.PROMOTE":     {[]string{CatAI, CatWrite, CatFast}},
	"CANARY.ROLLBACK":    {[]string{CatAI, CatWrite, CatFast}},
	"CANARY.LIST":        {[]string{CatAI, CatRead, CatFast}},
	"CANARY.FORGET":      {[]string{CatAI, CatWrite, CatFast}},
	"CANARY.STATS":       {[]string{CatAI, CatRead, CatFast}},

	// RERANK.* — cross-encoder rerank score cache.
	"RERANK.GET":     {[]string{CatAI, CatRead, CatFast}},
	"RERANK.SET":     {[]string{CatAI, CatWrite, CatFast}},
	"RERANK.SCORE":   {[]string{CatAI, CatRead, CatFast}},
	"RERANK.FORGET":  {[]string{CatAI, CatWrite, CatFast}},
	"RERANK.PURGE":   {[]string{CatAI, CatWrite, CatFast}},
	"RERANK.SETCAP":  {[]string{CatAI, CatWrite, CatFast}},
	"RERANK.SETCOST": {[]string{CatAI, CatWrite, CatFast}},
	"RERANK.STATS":   {[]string{CatAI, CatRead, CatFast}},

	// JUDGE.* — LLM-as-judge eval suite.
	"JUDGE.CASE.ADD":    {[]string{CatAI, CatWrite, CatFast}},
	"JUDGE.CASE.REMOVE": {[]string{CatAI, CatWrite, CatFast}},
	"JUDGE.CASE.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"JUDGE.SCORE":       {[]string{CatAI, CatWrite, CatFast}},
	"JUDGE.HISTORY":     {[]string{CatAI, CatRead, CatFast}},
	"JUDGE.PASSRATE":    {[]string{CatAI, CatRead, CatFast}},
	"JUDGE.PROMPTS":     {[]string{CatAI, CatRead, CatFast}},
	"JUDGE.FORGET":      {[]string{CatAI, CatWrite, CatFast}},
	"JUDGE.STATS":       {[]string{CatAI, CatRead, CatFast}},

	// FEWSHOT.* — few-shot example library w/ semantic retrieval.
	"FEWSHOT.ADD":    {[]string{CatAI, CatWrite, CatFast}},
	"FEWSHOT.QUERY":  {[]string{CatAI, CatRead, CatFast}},
	"FEWSHOT.GET":    {[]string{CatAI, CatRead, CatFast}},
	"FEWSHOT.DEL":    {[]string{CatAI, CatWrite, CatFast}},
	"FEWSHOT.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"FEWSHOT.BANKS":  {[]string{CatAI, CatRead, CatFast}},
	"FEWSHOT.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"FEWSHOT.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// GUARDRAIL.* — composable safety pipeline.
	"GUARDRAIL.DEFINE": {[]string{CatAI, CatWrite, CatFast}},
	"GUARDRAIL.RUN":    {[]string{CatAI, CatWrite, CatFast}},
	"GUARDRAIL.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"GUARDRAIL.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"GUARDRAIL.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// STRUCT.* — JSON schema validation + repair prompts.
	"STRUCT.SCHEMA.SET":   {[]string{CatAI, CatWrite, CatFast}},
	"STRUCT.SCHEMA.GET":   {[]string{CatAI, CatRead, CatFast}},
	"STRUCT.SCHEMA.LIST":  {[]string{CatAI, CatRead, CatFast}},
	"STRUCT.VALIDATE":     {[]string{CatAI, CatRead, CatFast}},
	"STRUCT.REPAIR_PROMPT": {[]string{CatAI, CatRead, CatFast}},
	"STRUCT.FORGET":       {[]string{CatAI, CatWrite, CatFast}},
	"STRUCT.STATS":        {[]string{CatAI, CatRead, CatFast}},

	// COALESCE.* — single-flight thundering-herd protection.
	// WAIT is CatSlow because it can block for the full timeout.
	"COALESCE.LOCK":    {[]string{CatAI, CatWrite, CatFast}},
	"COALESCE.PUBLISH": {[]string{CatAI, CatWrite, CatFast}},
	"COALESCE.WAIT":    {[]string{CatAI, CatRead, CatSlow}},
	"COALESCE.STATUS":  {[]string{CatAI, CatRead, CatFast}},
	"COALESCE.FORGET":  {[]string{CatAI, CatWrite, CatFast}},
	"COALESCE.KEYS":    {[]string{CatAI, CatRead, CatFast}},
	"COALESCE.STATS":   {[]string{CatAI, CatRead, CatFast}},

	// HEDGE.* — multi-provider hedged call tracker.
	// WAIT is CatSlow (it can block).
	"HEDGE.START":   {[]string{CatAI, CatWrite, CatFast}},
	"HEDGE.PUBLISH": {[]string{CatAI, CatWrite, CatFast}},
	"HEDGE.WAIT":    {[]string{CatAI, CatRead, CatSlow}},
	"HEDGE.STATUS":  {[]string{CatAI, CatRead, CatFast}},
	"HEDGE.FORGET":  {[]string{CatAI, CatWrite, CatFast}},
	"HEDGE.STATS":   {[]string{CatAI, CatRead, CatFast}},

	// VERIFY.* — self-consistency consensus.
	"VERIFY.SAMPLE":    {[]string{CatAI, CatWrite, CatFast}},
	"VERIFY.CONSENSUS": {[]string{CatAI, CatRead, CatFast}},
	"VERIFY.SAMPLES":   {[]string{CatAI, CatRead, CatFast}},
	"VERIFY.FORGET":    {[]string{CatAI, CatWrite, CatFast}},
	"VERIFY.STATS":     {[]string{CatAI, CatRead, CatFast}},

	// REWRITE.* — query rewrite cache (hyDE / step-back / etc.).
	"REWRITE.SET":       {[]string{CatAI, CatWrite, CatFast}},
	"REWRITE.GET":       {[]string{CatAI, CatRead, CatFast}},
	"REWRITE.SET_MULTI": {[]string{CatAI, CatWrite, CatFast}},
	"REWRITE.LIST":      {[]string{CatAI, CatRead, CatFast}},
	"REWRITE.FORGET":    {[]string{CatAI, CatWrite, CatFast}},
	"REWRITE.PURGE":     {[]string{CatAI, CatWrite, CatFast}},
	"REWRITE.SETCAP":    {[]string{CatAI, CatWrite, CatFast}},
	"REWRITE.SETCOST":   {[]string{CatAI, CatWrite, CatFast}},
	"REWRITE.STATS":     {[]string{CatAI, CatRead, CatFast}},

	// CITE.* — citation extractor + validator.
	"CITE.EXTRACT":  {[]string{CatAI, CatRead, CatFast}},
	"CITE.RESOLVE":  {[]string{CatAI, CatRead, CatFast}},
	"CITE.VALIDATE": {[]string{CatAI, CatRead, CatFast}},
	"CITE.STATS":    {[]string{CatAI, CatRead, CatFast}},

	// SHRINK.* — prompt compression. Pure compute, no durable state.
	"SHRINK.TEXT":  {[]string{CatAI, CatRead, CatFast}},
	"SHRINK.STATS": {[]string{CatAI, CatRead, CatFast}},

	// AGENTLOOP.* — agent step budget enforcer.
	"AGENTLOOP.START":  {[]string{CatAI, CatWrite, CatFast}},
	"AGENTLOOP.STEP":   {[]string{CatAI, CatWrite, CatFast}},
	"AGENTLOOP.STATUS": {[]string{CatAI, CatRead, CatFast}},
	"AGENTLOOP.RESET":  {[]string{CatAI, CatWrite, CatFast}},
	"AGENTLOOP.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"AGENTLOOP.ACTIVE": {[]string{CatAI, CatRead, CatFast}},
	"AGENTLOOP.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// DEDUP.SEM.* — semantic dedup for high-volume streams.
	"DEDUP.SEM.SEEN":    {[]string{CatAI, CatWrite, CatFast}},
	"DEDUP.SEM.PEEK":    {[]string{CatAI, CatRead, CatFast}},
	"DEDUP.SEM.ADD":     {[]string{CatAI, CatWrite, CatFast}},
	"DEDUP.SEM.RECENT":  {[]string{CatAI, CatRead, CatFast}},
	"DEDUP.SEM.FORGET":  {[]string{CatAI, CatWrite, CatFast}},
	"DEDUP.SEM.BUCKETS": {[]string{CatAI, CatRead, CatFast}},
	"DEDUP.SEM.STATS":   {[]string{CatAI, CatRead, CatFast}},

	// PREFIX.* — KV-cache-aware prefix routing.
	"PREFIX.REGISTER": {[]string{CatAI, CatWrite, CatFast}},
	"PREFIX.LOOKUP":   {[]string{CatAI, CatRead, CatFast}},
	"PREFIX.HASH":     {[]string{CatAI, CatRead, CatFast}},
	"PREFIX.FORGET":   {[]string{CatAI, CatWrite, CatFast}},
	"PREFIX.EVICT":    {[]string{CatAI, CatWrite, CatFast}},
	"PREFIX.LIST":     {[]string{CatAI, CatRead, CatFast}},
	"PREFIX.STATS":    {[]string{CatAI, CatRead, CatFast}},

	// TOOLBOX.* — tool schema registry w/ semantic search.
	"TOOLBOX.REGISTER": {[]string{CatAI, CatWrite, CatFast}},
	"TOOLBOX.SEARCH":   {[]string{CatAI, CatRead, CatFast}},
	"TOOLBOX.GET":      {[]string{CatAI, CatRead, CatFast}},
	"TOOLBOX.LIST":     {[]string{CatAI, CatRead, CatFast}},
	"TOOLBOX.FORGET":   {[]string{CatAI, CatWrite, CatFast}},
	"TOOLBOX.STATS":    {[]string{CatAI, CatRead, CatFast}},

	// TRANSLATE.* — multi-language translation cache.
	"TRANSLATE.SET":     {[]string{CatAI, CatWrite, CatFast}},
	"TRANSLATE.GET":     {[]string{CatAI, CatRead, CatFast}},
	"TRANSLATE.MGET":    {[]string{CatAI, CatRead, CatFast}},
	"TRANSLATE.FORGET":  {[]string{CatAI, CatWrite, CatFast}},
	"TRANSLATE.PURGE":   {[]string{CatAI, CatWrite, CatFast}},
	"TRANSLATE.SETCAP":  {[]string{CatAI, CatWrite, CatFast}},
	"TRANSLATE.SETCOST": {[]string{CatAI, CatWrite, CatFast}},
	"TRANSLATE.STATS":   {[]string{CatAI, CatRead, CatFast}},

	// EMBED.MAT.* — inline embedding matrix + top-K cosine search.
	"EMBED.MAT.SET":    {[]string{CatAI, CatWrite, CatFast}},
	"EMBED.MAT.DEL":    {[]string{CatAI, CatWrite, CatFast}},
	"EMBED.MAT.TOPK":   {[]string{CatAI, CatRead, CatFast}},
	"EMBED.MAT.DOT":    {[]string{CatAI, CatRead, CatFast}},
	"EMBED.MAT.COSINE": {[]string{CatAI, CatRead, CatFast}},
	"EMBED.MAT.LEN":    {[]string{CatAI, CatRead, CatFast}},
	"EMBED.MAT.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"EMBED.MAT.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"EMBED.MAT.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// OPCACHE.* — deterministic LLM op memoisation.
	"OPCACHE.SET":     {[]string{CatAI, CatWrite, CatFast}},
	"OPCACHE.GET":     {[]string{CatAI, CatRead, CatFast}},
	"OPCACHE.FORGET":  {[]string{CatAI, CatWrite, CatFast}},
	"OPCACHE.PURGE":   {[]string{CatAI, CatWrite, CatFast}},
	"OPCACHE.SETCAP":  {[]string{CatAI, CatWrite, CatFast}},
	"OPCACHE.SETCOST": {[]string{CatAI, CatWrite, CatFast}},
	"OPCACHE.STATS":   {[]string{CatAI, CatRead, CatFast}},

	// AUTOCOMPLETE.* — radix-trie prefix completion.
	"AUTOCOMPLETE.ADD":     {[]string{CatAI, CatWrite, CatFast}},
	"AUTOCOMPLETE.SUGGEST": {[]string{CatAI, CatRead, CatFast}},
	"AUTOCOMPLETE.DEL":     {[]string{CatAI, CatWrite, CatFast}},
	"AUTOCOMPLETE.SIZE":    {[]string{CatAI, CatRead, CatFast}},
	"AUTOCOMPLETE.LIST":    {[]string{CatAI, CatRead, CatFast}},
	"AUTOCOMPLETE.FORGET":  {[]string{CatAI, CatWrite, CatFast}},
	"AUTOCOMPLETE.STATS":   {[]string{CatAI, CatRead, CatFast}},

	// CHAINSTATE.* — multi-step workflow state machine.
	"CHAINSTATE.DEFINE":        {[]string{CatAI, CatWrite, CatFast}},
	"CHAINSTATE.START":         {[]string{CatAI, CatWrite, CatFast}},
	"CHAINSTATE.DONE":          {[]string{CatAI, CatWrite, CatFast}},
	"CHAINSTATE.FAIL":          {[]string{CatAI, CatWrite, CatFast}},
	"CHAINSTATE.RESUME":        {[]string{CatAI, CatRead, CatFast}},
	"CHAINSTATE.ARTIFACT":      {[]string{CatAI, CatRead, CatFast}},
	"CHAINSTATE.STATUS":        {[]string{CatAI, CatRead, CatFast}},
	"CHAINSTATE.RUNS":          {[]string{CatAI, CatRead, CatFast}},
	"CHAINSTATE.FORGET":        {[]string{CatAI, CatWrite, CatFast}},
	"CHAINSTATE.FORGET_CHAIN":  {[]string{CatAI, CatWrite, CatFast}},
	"CHAINSTATE.STATS":         {[]string{CatAI, CatRead, CatFast}},

	// MOE.* — mixture-of-experts router.
	"MOE.EXPERT.REGISTER": {[]string{CatAI, CatWrite, CatFast}},
	"MOE.ROUTE":           {[]string{CatAI, CatRead, CatFast}},
	"MOE.RECORD":          {[]string{CatAI, CatWrite, CatFast}},
	"MOE.EXPERTS":         {[]string{CatAI, CatRead, CatFast}},
	"MOE.FORGET":          {[]string{CatAI, CatWrite, CatFast}},
	"MOE.STATS":           {[]string{CatAI, CatRead, CatFast}},

	// CONFIDENCE.* — calibration via reliability bins.
	"CONFIDENCE.RECORD":    {[]string{CatAI, CatWrite, CatFast}},
	"CONFIDENCE.CURVE":     {[]string{CatAI, CatRead, CatFast}},
	"CONFIDENCE.ECE":       {[]string{CatAI, CatRead, CatFast}},
	"CONFIDENCE.CALIBRATE": {[]string{CatAI, CatRead, CatFast}},
	"CONFIDENCE.RESET":     {[]string{CatAI, CatWrite, CatFast}},
	"CONFIDENCE.MODELS":    {[]string{CatAI, CatRead, CatFast}},
	"CONFIDENCE.STATS":     {[]string{CatAI, CatRead, CatFast}},

	// DRIFT.* — input distribution drift detection.
	"DRIFT.BASELINE": {[]string{CatAI, CatWrite, CatFast}},
	"DRIFT.OBSERVE":  {[]string{CatAI, CatWrite, CatFast}},
	"DRIFT.SCORE":    {[]string{CatAI, CatRead, CatFast}},
	"DRIFT.RESET":    {[]string{CatAI, CatWrite, CatFast}},
	"DRIFT.FORGET":   {[]string{CatAI, CatWrite, CatFast}},
	"DRIFT.TRACKERS": {[]string{CatAI, CatRead, CatFast}},
	"DRIFT.STATS":    {[]string{CatAI, CatRead, CatFast}},

	// WATERMARK.* — AI-generated text detector.
	"WATERMARK.SCORE":          {[]string{CatAI, CatRead, CatFast}},
	"WATERMARK.PATTERN.ADD":    {[]string{CatAI, CatWrite, CatFast}},
	"WATERMARK.PATTERN.REMOVE": {[]string{CatAI, CatWrite, CatFast}},
	"WATERMARK.PATTERN.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"WATERMARK.STATS":          {[]string{CatAI, CatRead, CatFast}},

	// MATRYOSHKA.* — hierarchical 3-pass embedding retrieval.
	"MATRYOSHKA.SET":    {[]string{CatAI, CatWrite, CatFast}},
	"MATRYOSHKA.DEL":    {[]string{CatAI, CatWrite, CatFast}},
	"MATRYOSHKA.TOPK":   {[]string{CatAI, CatRead, CatFast}},
	"MATRYOSHKA.LEN":    {[]string{CatAI, CatRead, CatFast}},
	"MATRYOSHKA.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"MATRYOSHKA.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// VEC.QUANT.* — int8-quantized embedding matrix.
	"VEC.QUANT.SET":    {[]string{CatAI, CatWrite, CatFast}},
	"VEC.QUANT.DEL":    {[]string{CatAI, CatWrite, CatFast}},
	"VEC.QUANT.TOPK":   {[]string{CatAI, CatRead, CatFast}},
	"VEC.QUANT.COSINE": {[]string{CatAI, CatRead, CatFast}},
	"VEC.QUANT.LEN":    {[]string{CatAI, CatRead, CatFast}},
	"VEC.QUANT.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"VEC.QUANT.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// EMBED.POOL.* — stateless bulk pooling ops.
	"EMBED.POOL.MEAN":     {[]string{CatAI, CatRead, CatFast}},
	"EMBED.POOL.MAX":      {[]string{CatAI, CatRead, CatFast}},
	"EMBED.POOL.WEIGHTED": {[]string{CatAI, CatRead, CatFast}},
	"EMBED.POOL.NORM_SUM": {[]string{CatAI, CatRead, CatFast}},
	"EMBED.POOL.STATS":    {[]string{CatAI, CatRead, CatFast}},

	// STREAM.PARSE.* — incremental JSON streaming parser.
	"STREAM.PARSE.OPEN":     {[]string{CatAI, CatWrite, CatFast}},
	"STREAM.PARSE.PUSH":     {[]string{CatAI, CatWrite, CatFast}},
	"STREAM.PARSE.COMPLETE": {[]string{CatAI, CatWrite, CatFast}},
	"STREAM.PARSE.STATUS":   {[]string{CatAI, CatRead, CatFast}},
	"STREAM.PARSE.FORGET":   {[]string{CatAI, CatWrite, CatFast}},
	"STREAM.PARSE.STATS":    {[]string{CatAI, CatRead, CatFast}},

	// LIMITER.LLM.* — token-aware rate limiter.
	"LIMITER.LLM.CONFIG":  {[]string{CatAI, CatWrite, CatFast}},
	"LIMITER.LLM.RESERVE": {[]string{CatAI, CatWrite, CatFast}},
	"LIMITER.LLM.RECORD":  {[]string{CatAI, CatWrite, CatFast}},
	"LIMITER.LLM.USAGE":   {[]string{CatAI, CatRead, CatFast}},
	"LIMITER.LLM.RESET":   {[]string{CatAI, CatWrite, CatFast}},
	"LIMITER.LLM.ALL":     {[]string{CatAI, CatRead, CatFast}},
	"LIMITER.LLM.STATS":   {[]string{CatAI, CatRead, CatFast}},

	// CACHE.LAYERS.* — 3-layer cache lookup.
	"CACHE.LAYERS.SET":           {[]string{CatAI, CatWrite, CatFast}},
	"CACHE.LAYERS.LOOKUP":        {[]string{CatAI, CatRead, CatFast}},
	"CACHE.LAYERS.FORGET":        {[]string{CatAI, CatWrite, CatFast}},
	"CACHE.LAYERS.PURGE":         {[]string{CatAI, CatWrite, CatFast}},
	"CACHE.LAYERS.SET_THRESHOLD": {[]string{CatAI, CatWrite, CatFast}},
	"CACHE.LAYERS.STATS":         {[]string{CatAI, CatRead, CatFast}},

	// CONTRACT.* — LLM tool-call signature validator.
	"CONTRACT.REGISTER":   {[]string{CatAI, CatWrite, CatFast}},
	"CONTRACT.UNREGISTER": {[]string{CatAI, CatWrite, CatFast}},
	"CONTRACT.VALIDATE":   {[]string{CatAI, CatRead, CatFast}},
	"CONTRACT.LIST":       {[]string{CatAI, CatRead, CatFast}},
	"CONTRACT.STATS":      {[]string{CatAI, CatRead, CatFast}},

	// TIMELINE.* — per-key time-windowed event log.
	"TIMELINE.APPEND": {[]string{CatAI, CatWrite, CatFast}},
	"TIMELINE.RANGE":  {[]string{CatAI, CatRead, CatFast}},
	"TIMELINE.RECENT": {[]string{CatAI, CatRead, CatFast}},
	"TIMELINE.LEN":    {[]string{CatAI, CatRead, CatFast}},
	"TIMELINE.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"TIMELINE.KEYS":   {[]string{CatAI, CatRead, CatFast}},
	"TIMELINE.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// HASH.LSH.* — random-hyperplane LSH index.
	"HASH.LSH.CREATE":    {[]string{CatAI, CatWrite, CatFast}},
	"HASH.LSH.SET":       {[]string{CatAI, CatWrite, CatFast}},
	"HASH.LSH.DEL":       {[]string{CatAI, CatWrite, CatFast}},
	"HASH.LSH.SIGN":      {[]string{CatAI, CatRead, CatFast}},
	"HASH.LSH.NEIGHBORS": {[]string{CatAI, CatRead, CatFast}},
	"HASH.LSH.LEN":       {[]string{CatAI, CatRead, CatFast}},
	"HASH.LSH.FORGET":    {[]string{CatAI, CatWrite, CatFast}},
	"HASH.LSH.STATS":     {[]string{CatAI, CatRead, CatFast}},

	// NLI.* — entailment cache for hallucination detection.
	"NLI.SET":    {[]string{CatAI, CatWrite, CatFast}},
	"NLI.GET":    {[]string{CatAI, CatRead, CatFast}},
	"NLI.CHECK":  {[]string{CatAI, CatRead, CatFast}},
	"NLI.MGET":   {[]string{CatAI, CatRead, CatFast}},
	"NLI.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"NLI.PURGE":  {[]string{CatAI, CatWrite, CatFast}},
	"NLI.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// CASCADE.* — cost-tier model fallback ladder with learning.
	"CASCADE.CONFIG": {[]string{CatAI, CatWrite, CatFast}},
	"CASCADE.PICK":   {[]string{CatAI, CatRead, CatFast}},
	"CASCADE.RECORD": {[]string{CatAI, CatWrite, CatFast}},
	"CASCADE.STATUS": {[]string{CatAI, CatRead, CatFast}},
	"CASCADE.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"CASCADE.PURGE":  {[]string{CatAI, CatWrite, CatFast}},
	"CASCADE.ALL":    {[]string{CatAI, CatRead, CatFast}},
	"CASCADE.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// MASK.* — fill-in-the-middle prompt templates.
	"MASK.REGISTER":   {[]string{CatAI, CatWrite, CatFast}},
	"MASK.BUILD":      {[]string{CatAI, CatRead, CatFast}},
	"MASK.UNREGISTER": {[]string{CatAI, CatWrite, CatFast}},
	"MASK.LIST":       {[]string{CatAI, CatRead, CatFast}},
	"MASK.STATS":      {[]string{CatAI, CatRead, CatFast}},

	// FACT.* — versioned fact registry + stamp tracking.
	"FACT.SET":        {[]string{CatAI, CatWrite, CatFast}},
	"FACT.BUMP":       {[]string{CatAI, CatWrite, CatFast}},
	"FACT.GET":        {[]string{CatAI, CatRead, CatFast}},
	"FACT.STAMP":      {[]string{CatAI, CatWrite, CatFast}},
	"FACT.STALE":      {[]string{CatAI, CatRead, CatFast}},
	"FACT.STALE_KEYS": {[]string{CatAI, CatRead, CatFast}},
	"FACT.UNSTAMP":    {[]string{CatAI, CatWrite, CatFast}},
	"FACT.LIST":       {[]string{CatAI, CatRead, CatFast}},
	"FACT.FORGET":     {[]string{CatAI, CatWrite, CatFast}},
	"FACT.STATS":      {[]string{CatAI, CatRead, CatFast}},

	// CACHE.INVALIDATE.* — semantic cache invalidation scan.
	"CACHE.INVALIDATE.TRACK":    {[]string{CatAI, CatWrite, CatFast}},
	"CACHE.INVALIDATE.UNTRACK":  {[]string{CatAI, CatWrite, CatFast}},
	"CACHE.INVALIDATE.SEMANTIC": {[]string{CatAI, CatRead, CatSlow}},
	"CACHE.INVALIDATE.STATS":    {[]string{CatAI, CatRead, CatFast}},
	"CACHE.INVALIDATE.PURGE":    {[]string{CatAI, CatWrite, CatFast}},
	"CACHE.STALE.LIST":          {[]string{CatAI, CatRead, CatFast}},

	// BANDIT.* — adaptive multi-armed bandit router.
	"BANDIT.CREATE":       {[]string{CatAI, CatWrite, CatFast}},
	"BANDIT.PICK":         {[]string{CatAI, CatRead, CatFast}},
	"BANDIT.RECORD":       {[]string{CatAI, CatWrite, CatFast}},
	"BANDIT.STATS":        {[]string{CatAI, CatRead, CatFast}},
	"BANDIT.ARMS":         {[]string{CatAI, CatRead, CatFast}},
	"BANDIT.RESET":        {[]string{CatAI, CatWrite, CatFast}},
	"BANDIT.FORGET":       {[]string{CatAI, CatWrite, CatFast}},
	"BANDIT.LIST":         {[]string{CatAI, CatRead, CatFast}},
	"BANDIT.GLOBAL_STATS": {[]string{CatAI, CatRead, CatFast}},

	// POLICY.SEM.* — semantic firewall by example.
	"POLICY.SEM.DEFINE": {[]string{CatAI, CatWrite, CatFast}},
	"POLICY.SEM.ADD":    {[]string{CatAI, CatWrite, CatFast}},
	"POLICY.SEM.REMOVE": {[]string{CatAI, CatWrite, CatFast}},
	"POLICY.SEM.CHECK":  {[]string{CatAI, CatRead, CatFast}},
	"POLICY.SEM.LIST":   {[]string{CatAI, CatRead, CatFast}},
	"POLICY.SEM.FORGET": {[]string{CatAI, CatWrite, CatFast}},
	"POLICY.SEM.STATS":  {[]string{CatAI, CatRead, CatFast}},

	// NOVELTY.* — per-query out-of-distribution gate.
	"NOVELTY.BASELINE":       {[]string{CatAI, CatWrite, CatFast}},
	"NOVELTY.ADD":            {[]string{CatAI, CatWrite, CatFast}},
	"NOVELTY.SCORE":          {[]string{CatAI, CatRead, CatFast}},
	"NOVELTY.SET_THRESHOLDS": {[]string{CatAI, CatWrite, CatFast}},
	"NOVELTY.SIZE":           {[]string{CatAI, CatRead, CatFast}},
	"NOVELTY.FORGET":         {[]string{CatAI, CatWrite, CatFast}},
	"NOVELTY.DETECTORS":      {[]string{CatAI, CatRead, CatFast}},
	"NOVELTY.STATS":          {[]string{CatAI, CatRead, CatFast}},

	// LOCK.SEM.* — semantic dedup-locks.
	"LOCK.SEM.ACQUIRE":          {[]string{CatAI, CatWrite, CatFast}},
	"LOCK.SEM.RELEASE":          {[]string{CatAI, CatWrite, CatFast}},
	"LOCK.SEM.STATUS":           {[]string{CatAI, CatRead, CatFast}},
	"LOCK.SEM.FORGET":           {[]string{CatAI, CatWrite, CatFast}},
	"LOCK.SEM.FORGET_NAMESPACE": {[]string{CatAI, CatWrite, CatFast}},
	"LOCK.SEM.STATS":            {[]string{CatAI, CatRead, CatFast}},
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
