package probmod

import (
	"strconv"
	"strings"

	"github.com/dhiravpatel/neurocache/apps/api/internal/acl"
	"github.com/dhiravpatel/neurocache/apps/api/internal/modules"
)

var (
	bloomTypeID  = modules.MakeTypeID("bf-rb1!")
	cuckooTypeID = modules.MakeTypeID("cf-rb1!")
	cmsTypeID    = modules.MakeTypeID("cms-rb1!")
)

// Module is the registration entry. main wires it via side-effect
// import of internal/modules/builtin/probmod.
var Module = modules.Module{
	Name:        "probabilistic",
	Version:     "1.0.0",
	Description: "RedisBloom-compatible BF/CF/CMS probabilistic data types",
	Init:        initModule,
}

func init() { modules.RegisterAvailable(Module) }

func initModule(ctx *modules.RegisterCtx) error {
	if err := ctx.RegisterType(modules.CustomType{
		ID: bloomTypeID, Name: "MBbloom--",
		Marshal:   func(v any) ([]byte, error) { return v.(*Bloom).Marshal() },
		Unmarshal: func(b []byte) (any, error) { return UnmarshalBloom(b) },
		MemUsage:  func(v any) int64 { return int64(v.(*Bloom).Size()) },
	}); err != nil {
		return err
	}
	if err := ctx.RegisterType(modules.CustomType{
		ID: cuckooTypeID, Name: "MBbloomCF",
		Marshal:   func(v any) ([]byte, error) { return v.(*Cuckoo).Marshal() },
		Unmarshal: func(b []byte) (any, error) { return UnmarshalCuckoo(b) },
		MemUsage: func(v any) int64 {
			c := v.(*Cuckoo)
			return int64(c.NumBuckets) * int64(c.BucketSize) * 2
		},
	}); err != nil {
		return err
	}
	if err := ctx.RegisterType(modules.CustomType{
		ID: cmsTypeID, Name: "CMSk-type",
		Marshal:   func(v any) ([]byte, error) { return v.(*CMS).Marshal() },
		Unmarshal: func(b []byte) (any, error) { return UnmarshalCMS(b) },
		MemUsage:  func(v any) int64 { c := v.(*CMS); return int64(c.Width*c.Depth) * 8 },
	}); err != nil {
		return err
	}

	for _, c := range commands() {
		if err := ctx.RegisterCmd(c); err != nil {
			return err
		}
	}
	return nil
}

// commands is the full set of BF.* / CF.* / CMS.* registrations,
// grouped here so additions are obvious.
func commands() []modules.Cmd {
	r := []string{acl.CatRead, acl.CatFast}
	w := []string{acl.CatWrite, acl.CatFast}
	return []modules.Cmd{
		// ── Bloom ──────────────────────────────────────────────
		{Name: "BF.RESERVE", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: bfReserve},
		{Name: "BF.ADD", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: bfAdd},
		{Name: "BF.MADD", Arity: -3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: bfMAdd},
		{Name: "BF.EXISTS", Arity: 3, Categories: r, KeyPosition: modules.KeyAt(1), Run: bfExists},
		{Name: "BF.MEXISTS", Arity: -3, Categories: r, KeyPosition: modules.KeyAt(1), Run: bfMExists},
		{Name: "BF.INSERT", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: bfInsert},
		{Name: "BF.INFO", Arity: -2, Categories: r, KeyPosition: modules.KeyAt(1), Run: bfInfo},
		{Name: "BF.CARD", Arity: 2, Categories: r, KeyPosition: modules.KeyAt(1), Run: bfCard},

		// ── Cuckoo ─────────────────────────────────────────────
		{Name: "CF.RESERVE", Arity: -3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cfReserve},
		{Name: "CF.ADD", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cfAdd},
		{Name: "CF.ADDNX", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cfAddNX},
		{Name: "CF.INSERT", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cfInsert},
		{Name: "CF.INSERTNX", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cfInsertNX},
		{Name: "CF.EXISTS", Arity: 3, Categories: r, KeyPosition: modules.KeyAt(1), Run: cfExists},
		{Name: "CF.MEXISTS", Arity: -3, Categories: r, KeyPosition: modules.KeyAt(1), Run: cfMExists},
		{Name: "CF.DEL", Arity: 3, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cfDel},
		{Name: "CF.COUNT", Arity: 3, Categories: r, KeyPosition: modules.KeyAt(1), Run: cfCount},
		{Name: "CF.INFO", Arity: 2, Categories: r, KeyPosition: modules.KeyAt(1), Run: cfInfo},

		// ── Count-Min Sketch ───────────────────────────────────
		{Name: "CMS.INITBYDIM", Arity: 4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cmsInitByDim},
		{Name: "CMS.INITBYPROB", Arity: 4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cmsInitByProb},
		{Name: "CMS.INCRBY", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cmsIncrBy},
		{Name: "CMS.QUERY", Arity: -3, Categories: r, KeyPosition: modules.KeyAt(1), Run: cmsQuery},
		{Name: "CMS.MERGE", Arity: -4, Write: true, Categories: w, KeyPosition: modules.KeyAt(1), Run: cmsMerge},
		{Name: "CMS.INFO", Arity: 2, Categories: r, KeyPosition: modules.KeyAt(1), Run: cmsInfo},
	}
}

// ── Bloom command handlers ────────────────────────────────────────

func bfReserve(c *modules.Ctx, args []string) error {
	key := args[0]
	errRate, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		c.Reply.Error("invalid error rate")
		return nil
	}
	cap, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil {
		c.Reply.Error("invalid capacity")
		return nil
	}
	expansion := uint64(2)
	nonScaling := false
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "EXPANSION":
			if i+1 < len(args) {
				expansion, _ = strconv.ParseUint(args[i+1], 10, 64)
				i++
			}
		case "NONSCALING":
			nonScaling = true
		}
	}
	b, err := NewBloom(errRate, cap, expansion, nonScaling)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if err := c.Engine.SetCustomValue(key, bloomTypeID, b, 0); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	c.Reply.SimpleString("OK")
	return nil
}

func bfAdd(c *modules.Ctx, args []string) error {
	key, item := args[0], args[1]
	b, err := loadOrCreateBloom(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	added := b.Add([]byte(item))
	_ = c.Engine.SetCustomValue(key, bloomTypeID, b, 0)
	if added {
		c.Reply.Int(1)
	} else {
		c.Reply.Int(0)
	}
	return nil
}

func bfMAdd(c *modules.Ctx, args []string) error {
	key := args[0]
	b, err := loadOrCreateBloom(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	out := make([]any, 0, len(args)-1)
	for _, item := range args[1:] {
		if b.Add([]byte(item)) {
			out = append(out, int64(1))
		} else {
			out = append(out, int64(0))
		}
	}
	_ = c.Engine.SetCustomValue(key, bloomTypeID, b, 0)
	c.Reply.Array(out)
	return nil
}

func bfExists(c *modules.Ctx, args []string) error {
	key, item := args[0], args[1]
	v, ok, _ := c.Engine.GetCustomValue(key, bloomTypeID)
	if !ok {
		c.Reply.Int(0)
		return nil
	}
	if v.(*Bloom).Contains([]byte(item)) {
		c.Reply.Int(1)
	} else {
		c.Reply.Int(0)
	}
	return nil
}

func bfMExists(c *modules.Ctx, args []string) error {
	key := args[0]
	v, ok, _ := c.Engine.GetCustomValue(key, bloomTypeID)
	out := make([]any, len(args)-1)
	if !ok {
		for i := range out {
			out[i] = int64(0)
		}
		c.Reply.Array(out)
		return nil
	}
	b := v.(*Bloom)
	for i, item := range args[1:] {
		if b.Contains([]byte(item)) {
			out[i] = int64(1)
		} else {
			out[i] = int64(0)
		}
	}
	c.Reply.Array(out)
	return nil
}

// BF.INSERT key [CAPACITY n] [ERROR e] [EXPANSION x] [NOCREATE] [NONSCALING] ITEMS item [item ...]
func bfInsert(c *modules.Ctx, args []string) error {
	key := args[0]
	cap_ := uint64(100)
	errRate := 0.01
	expansion := uint64(2)
	nonScaling := false
	noCreate := false
	itemsAt := -1
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "CAPACITY":
			cap_, _ = strconv.ParseUint(args[i+1], 10, 64)
			i++
		case "ERROR":
			errRate, _ = strconv.ParseFloat(args[i+1], 64)
			i++
		case "EXPANSION":
			expansion, _ = strconv.ParseUint(args[i+1], 10, 64)
			i++
		case "NOCREATE":
			noCreate = true
		case "NONSCALING":
			nonScaling = true
		case "ITEMS":
			itemsAt = i + 1
			i = len(args)
		}
	}
	if itemsAt < 0 {
		c.Reply.Error("missing ITEMS")
		return nil
	}
	v, ok, _ := c.Engine.GetCustomValue(key, bloomTypeID)
	var b *Bloom
	if !ok {
		if noCreate {
			c.Reply.Error("ERR not found")
			return nil
		}
		var err error
		b, err = NewBloom(errRate, cap_, expansion, nonScaling)
		if err != nil {
			c.Reply.Error(err.Error())
			return nil
		}
	} else {
		b = v.(*Bloom)
	}
	out := make([]any, 0, len(args)-itemsAt)
	for _, item := range args[itemsAt:] {
		if b.Add([]byte(item)) {
			out = append(out, int64(1))
		} else {
			out = append(out, int64(0))
		}
	}
	_ = c.Engine.SetCustomValue(key, bloomTypeID, b, 0)
	c.Reply.Array(out)
	return nil
}

func bfInfo(c *modules.Ctx, args []string) error {
	v, ok, _ := c.Engine.GetCustomValue(args[0], bloomTypeID)
	if !ok {
		c.Reply.Error("ERR not found")
		return nil
	}
	b := v.(*Bloom)
	c.Reply.Array([]any{
		"Capacity", int64(b.Capacity),
		"Size", int64(b.Size()),
		"Number of filters", int64(len(b.Layers)),
		"Number of items inserted", int64(b.Inserted),
		"Expansion rate", int64(b.Expansion),
	})
	return nil
}

func bfCard(c *modules.Ctx, args []string) error {
	v, ok, _ := c.Engine.GetCustomValue(args[0], bloomTypeID)
	if !ok {
		c.Reply.Int(0)
		return nil
	}
	c.Reply.Int(int64(v.(*Bloom).Card()))
	return nil
}

func loadOrCreateBloom(c *modules.Ctx, key string) (*Bloom, error) {
	v, ok, err := c.Engine.GetCustomValue(key, bloomTypeID)
	if err != nil {
		return nil, err
	}
	if ok {
		return v.(*Bloom), nil
	}
	b, err := NewBloom(0.01, 100, 2, false)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// ── Cuckoo command handlers ───────────────────────────────────────

func cfReserve(c *modules.Ctx, args []string) error {
	key := args[0]
	cap, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		c.Reply.Error("invalid capacity")
		return nil
	}
	bucketSize := uint8(4)
	maxIter := uint16(500)
	expansion := uint8(1)
	for i := 2; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "BUCKETSIZE":
			n, _ := strconv.ParseUint(args[i+1], 10, 8)
			bucketSize = uint8(n)
			i++
		case "MAXITERATIONS":
			n, _ := strconv.ParseUint(args[i+1], 10, 16)
			maxIter = uint16(n)
			i++
		case "EXPANSION":
			n, _ := strconv.ParseUint(args[i+1], 10, 8)
			expansion = uint8(n)
			i++
		}
	}
	cf, err := NewCuckoo(cap, bucketSize, maxIter, expansion)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if err := c.Engine.SetCustomValue(key, cuckooTypeID, cf, 0); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	c.Reply.SimpleString("OK")
	return nil
}

func cfAdd(c *modules.Ctx, args []string) error {
	key, item := args[0], args[1]
	cf, err := loadOrCreateCuckoo(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if cf.Add([]byte(item)) {
		_ = c.Engine.SetCustomValue(key, cuckooTypeID, cf, 0)
		c.Reply.Int(1)
	} else {
		c.Reply.Int(0)
	}
	return nil
}

func cfAddNX(c *modules.Ctx, args []string) error {
	key, item := args[0], args[1]
	cf, err := loadOrCreateCuckoo(c, key)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if cf.AddNX([]byte(item)) {
		_ = c.Engine.SetCustomValue(key, cuckooTypeID, cf, 0)
		c.Reply.Int(1)
	} else {
		c.Reply.Int(0)
	}
	return nil
}

func cfInsert(c *modules.Ctx, args []string) error { return cfInsertImpl(c, args, false) }
func cfInsertNX(c *modules.Ctx, args []string) error { return cfInsertImpl(c, args, true) }

func cfInsertImpl(c *modules.Ctx, args []string, nx bool) error {
	key := args[0]
	cap_ := uint64(1024)
	noCreate := false
	itemsAt := -1
	for i := 1; i < len(args); i++ {
		switch strings.ToUpper(args[i]) {
		case "CAPACITY":
			cap_, _ = strconv.ParseUint(args[i+1], 10, 64)
			i++
		case "NOCREATE":
			noCreate = true
		case "ITEMS":
			itemsAt = i + 1
			i = len(args)
		}
	}
	if itemsAt < 0 {
		c.Reply.Error("missing ITEMS")
		return nil
	}
	v, ok, _ := c.Engine.GetCustomValue(key, cuckooTypeID)
	var cf *Cuckoo
	if !ok {
		if noCreate {
			c.Reply.Error("ERR not found")
			return nil
		}
		var err error
		cf, err = NewCuckoo(cap_, 4, 500, 1)
		if err != nil {
			c.Reply.Error(err.Error())
			return nil
		}
	} else {
		cf = v.(*Cuckoo)
	}
	out := make([]any, 0, len(args)-itemsAt)
	for _, item := range args[itemsAt:] {
		ok := false
		if nx {
			ok = cf.AddNX([]byte(item))
		} else {
			ok = cf.Add([]byte(item))
		}
		if ok {
			out = append(out, int64(1))
		} else {
			out = append(out, int64(0))
		}
	}
	_ = c.Engine.SetCustomValue(key, cuckooTypeID, cf, 0)
	c.Reply.Array(out)
	return nil
}

func cfExists(c *modules.Ctx, args []string) error {
	key, item := args[0], args[1]
	v, ok, _ := c.Engine.GetCustomValue(key, cuckooTypeID)
	if !ok {
		c.Reply.Int(0)
		return nil
	}
	if v.(*Cuckoo).Contains([]byte(item)) {
		c.Reply.Int(1)
	} else {
		c.Reply.Int(0)
	}
	return nil
}

func cfMExists(c *modules.Ctx, args []string) error {
	key := args[0]
	v, ok, _ := c.Engine.GetCustomValue(key, cuckooTypeID)
	out := make([]any, len(args)-1)
	if !ok {
		for i := range out {
			out[i] = int64(0)
		}
		c.Reply.Array(out)
		return nil
	}
	cf := v.(*Cuckoo)
	for i, item := range args[1:] {
		if cf.Contains([]byte(item)) {
			out[i] = int64(1)
		} else {
			out[i] = int64(0)
		}
	}
	c.Reply.Array(out)
	return nil
}

func cfDel(c *modules.Ctx, args []string) error {
	key, item := args[0], args[1]
	v, ok, _ := c.Engine.GetCustomValue(key, cuckooTypeID)
	if !ok {
		c.Reply.Int(0)
		return nil
	}
	cf := v.(*Cuckoo)
	if cf.Del([]byte(item)) {
		_ = c.Engine.SetCustomValue(key, cuckooTypeID, cf, 0)
		c.Reply.Int(1)
	} else {
		c.Reply.Int(0)
	}
	return nil
}

func cfCount(c *modules.Ctx, args []string) error {
	key, item := args[0], args[1]
	v, ok, _ := c.Engine.GetCustomValue(key, cuckooTypeID)
	if !ok {
		c.Reply.Int(0)
		return nil
	}
	c.Reply.Int(int64(v.(*Cuckoo).CountItem([]byte(item))))
	return nil
}

func cfInfo(c *modules.Ctx, args []string) error {
	v, ok, _ := c.Engine.GetCustomValue(args[0], cuckooTypeID)
	if !ok {
		c.Reply.Error("ERR not found")
		return nil
	}
	cf := v.(*Cuckoo)
	c.Reply.Array([]any{
		"Size", int64(cf.NumBuckets) * int64(cf.BucketSize) * 2,
		"Number of buckets", int64(cf.NumBuckets),
		"Number of items inserted", int64(cf.Count),
		"Bucket size", int64(cf.BucketSize),
		"Expansion rate", int64(cf.Expansion),
		"Max iterations", int64(cf.MaxIterations),
	})
	return nil
}

func loadOrCreateCuckoo(c *modules.Ctx, key string) (*Cuckoo, error) {
	v, ok, err := c.Engine.GetCustomValue(key, cuckooTypeID)
	if err != nil {
		return nil, err
	}
	if ok {
		return v.(*Cuckoo), nil
	}
	return NewCuckoo(1024, 4, 500, 1)
}

// ── CMS command handlers ──────────────────────────────────────────

func cmsInitByDim(c *modules.Ctx, args []string) error {
	key := args[0]
	width, err := strconv.ParseUint(args[1], 10, 64)
	if err != nil {
		c.Reply.Error("invalid width")
		return nil
	}
	depth, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil {
		c.Reply.Error("invalid depth")
		return nil
	}
	cms, err := NewCMSByDim(width, depth)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if err := c.Engine.SetCustomValue(key, cmsTypeID, cms, 0); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	c.Reply.SimpleString("OK")
	return nil
}

func cmsInitByProb(c *modules.Ctx, args []string) error {
	key := args[0]
	errRate, err := strconv.ParseFloat(args[1], 64)
	if err != nil {
		c.Reply.Error("invalid error rate")
		return nil
	}
	prob, err := strconv.ParseFloat(args[2], 64)
	if err != nil {
		c.Reply.Error("invalid probability")
		return nil
	}
	cms, err := NewCMSByProb(errRate, prob)
	if err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	if err := c.Engine.SetCustomValue(key, cmsTypeID, cms, 0); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	c.Reply.SimpleString("OK")
	return nil
}

func cmsIncrBy(c *modules.Ctx, args []string) error {
	if (len(args)-1)%2 != 0 {
		c.Reply.Error("CMS.INCRBY requires item/increment pairs")
		return nil
	}
	key := args[0]
	v, ok, _ := c.Engine.GetCustomValue(key, cmsTypeID)
	if !ok {
		c.Reply.Error("ERR CMS not initialised")
		return nil
	}
	cms := v.(*CMS)
	out := make([]any, 0, (len(args)-1)/2)
	for i := 1; i+1 < len(args); i += 2 {
		delta, _ := strconv.ParseInt(args[i+1], 10, 64)
		out = append(out, cms.IncrBy([]byte(args[i]), delta))
	}
	_ = c.Engine.SetCustomValue(key, cmsTypeID, cms, 0)
	c.Reply.Array(out)
	return nil
}

func cmsQuery(c *modules.Ctx, args []string) error {
	key := args[0]
	v, ok, _ := c.Engine.GetCustomValue(key, cmsTypeID)
	if !ok {
		c.Reply.Error("ERR CMS not initialised")
		return nil
	}
	cms := v.(*CMS)
	out := make([]any, 0, len(args)-1)
	for _, item := range args[1:] {
		out = append(out, cms.Query([]byte(item)))
	}
	c.Reply.Array(out)
	return nil
}

// CMS.MERGE dest numkeys src [src ...] [WEIGHTS w [w ...]]
func cmsMerge(c *modules.Ctx, args []string) error {
	dest := args[0]
	n, err := strconv.Atoi(args[1])
	if err != nil || n <= 0 {
		c.Reply.Error("invalid numkeys")
		return nil
	}
	if len(args) < 2+n {
		c.Reply.Error("too few source keys")
		return nil
	}
	srcKeys := args[2 : 2+n]
	weights := []uint64{}
	if len(args) > 2+n {
		if !strings.EqualFold(args[2+n], "WEIGHTS") {
			c.Reply.Error("expected WEIGHTS")
			return nil
		}
		for _, w := range args[3+n:] {
			wv, _ := strconv.ParseUint(w, 10, 64)
			weights = append(weights, wv)
		}
	}
	v, ok, _ := c.Engine.GetCustomValue(dest, cmsTypeID)
	if !ok {
		c.Reply.Error("ERR destination CMS not initialised")
		return nil
	}
	dst := v.(*CMS)
	srcs := make([]*CMS, 0, n)
	for _, k := range srcKeys {
		v, ok, _ := c.Engine.GetCustomValue(k, cmsTypeID)
		if !ok {
			c.Reply.Error("ERR source CMS not found: " + k)
			return nil
		}
		srcs = append(srcs, v.(*CMS))
	}
	if err := dst.Merge(srcs, weights); err != nil {
		c.Reply.Error(err.Error())
		return nil
	}
	_ = c.Engine.SetCustomValue(dest, cmsTypeID, dst, 0)
	c.Reply.SimpleString("OK")
	return nil
}

func cmsInfo(c *modules.Ctx, args []string) error {
	v, ok, _ := c.Engine.GetCustomValue(args[0], cmsTypeID)
	if !ok {
		c.Reply.Error("ERR not found")
		return nil
	}
	cms := v.(*CMS)
	c.Reply.Array([]any{
		"width", int64(cms.Width),
		"depth", int64(cms.Depth),
		"count", int64(cms.Count),
	})
	return nil
}
