package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/dhiravpatel/neurocache/apps/api/internal/config"
	"github.com/dhiravpatel/neurocache/apps/api/internal/engine"
	httpapi "github.com/dhiravpatel/neurocache/apps/api/internal/http"
	"github.com/dhiravpatel/neurocache/apps/api/internal/logger"
	"github.com/dhiravpatel/neurocache/apps/api/internal/resp"
	"github.com/dhiravpatel/neurocache/apps/api/internal/webui"

	// Built-in modules — imported for side effects so MODULE LOAD can
	// activate them by name. Add new modules here to compile them in.
	_ "github.com/dhiravpatel/neurocache/apps/api/internal/modules/builtin/echo"
	_ "github.com/dhiravpatel/neurocache/apps/api/internal/modules/builtin/jsonmod"
	_ "github.com/dhiravpatel/neurocache/apps/api/internal/modules/builtin/probmod"
	_ "github.com/dhiravpatel/neurocache/apps/api/internal/modules/builtin/searchmod"
	_ "github.com/dhiravpatel/neurocache/apps/api/internal/modules/builtin/tsmod"
)

// tuneGC adjusts Go runtime GC defaults to better fit a long-running
// in-memory data store. Two knobs:
//
//   - GOGC sets the heap-growth percentage that triggers GC. The Go
//     default is 100 (collect when heap doubles), which fires far more
//     often than a cache with a stable working set actually needs and
//     inflates p99 tail latency 1-3x vs Redis (no GC). We raise to 200
//     so GC runs about half as often. Operators with strict memory
//     ceilings can override via the GOGC env var.
//
//   - GOMEMLIMIT (Go 1.19+) is a soft heap budget. When the heap
//     approaches it, GC pressure ramps up to keep RSS in check —
//     dramatically smoother than letting GOGC alone drive the heap.
//     We default it to MaxMemoryMB + 25% slack so the cache can use
//     its configured budget plus modest goroutine + GC overhead.
//
// Both defaults are skipped when the operator has set the env var
// themselves, matching Go's standard "user > program > default" rule.
// Returns the resolved (gogc, memLimitBytes) for the boot log.
func tuneGC(maxMemoryMB int) (int, int64) {
	gogc := 200
	if envGOGC := os.Getenv("GOGC"); envGOGC != "" {
		// honour user override; SetGCPercent(-1) reads the current value
		// after the runtime has already parsed GOGC at startup.
		gogc = debug.SetGCPercent(-1)
		debug.SetGCPercent(gogc)
	} else {
		debug.SetGCPercent(gogc)
	}
	var memLimit int64
	if os.Getenv("GOMEMLIMIT") == "" && maxMemoryMB > 0 {
		// 25% slack covers goroutine stacks, small allocs, and the
		// per-shard map metadata. Cache values themselves stay within
		// MaxMemoryMB because the eviction loop enforces it.
		memLimit = int64(maxMemoryMB) * 1024 * 1024 * 5 / 4
		debug.SetMemoryLimit(memLimit)
	}
	return gogc, memLimit
}

// tuneGOMAXPROCS detects the container's effective CPU quota (cgroup v2
// or v1) and pins GOMAXPROCS to it. The Go runtime's default reads
// runtime.NumCPU() — i.e. the host machine's CPU count — which on a
// 4-vCPU pod inside a 64-core box launches 64 P's, oversubscribes the
// scheduler, and inflates p99 latency under load. On bare metal this
// is a no-op (NumCPU == quota). Honours user override via GOMAXPROCS env.
func tuneGOMAXPROCS() int {
	if os.Getenv("GOMAXPROCS") != "" {
		// Runtime already parsed the env var; just return current.
		return runtime.GOMAXPROCS(0)
	}
	if quota := readCgroupCPUQuota(); quota > 0 && quota < runtime.NumCPU() {
		runtime.GOMAXPROCS(quota)
	}
	return runtime.GOMAXPROCS(0)
}

// readCgroupCPUQuota returns the integer CPU quota the current cgroup
// allows. Reads cgroup v2 (`cpu.max`) first, falling back to v1
// (`cpu.cfs_quota_us` / `cpu.cfs_period_us`). Returns 0 when no quota
// is set or the cgroup files aren't readable.
func readCgroupCPUQuota() int {
	// cgroup v2: /sys/fs/cgroup/cpu.max → "<quota> <period>" or "max <period>".
	if b, err := os.ReadFile("/sys/fs/cgroup/cpu.max"); err == nil {
		parts := strings.Fields(string(b))
		if len(parts) == 2 && parts[0] != "max" {
			q, qErr := strconv.Atoi(parts[0])
			p, pErr := strconv.Atoi(parts[1])
			if qErr == nil && pErr == nil && p > 0 {
				n := q / p
				if q%p > 0 {
					n++
				}
				if n >= 1 {
					return n
				}
			}
		}
	}
	// cgroup v1.
	q, err := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_quota_us")
	if err != nil {
		return 0
	}
	p, err := os.ReadFile("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	if err != nil {
		return 0
	}
	qi, qErr := strconv.Atoi(strings.TrimSpace(string(q)))
	pi, pErr := strconv.Atoi(strings.TrimSpace(string(p)))
	if qErr != nil || pErr != nil || qi <= 0 || pi <= 0 {
		return 0
	}
	n := qi / pi
	if qi%pi > 0 {
		n++
	}
	return n
}

func main() {
	cfg := config.Load()
	gogc, memLimit := tuneGC(cfg.MaxMemoryMB)
	maxprocs := tuneGOMAXPROCS()
	log := logger.New(cfg.LogLevel, cfg.LogFormat)
	log.Info("neurocache starting",
		"version", "0.3.0",
		"http_port", cfg.HTTPPort,
		"resp_port", cfg.RESPPort,
		"gogc", gogc,
		"gomemlimit_bytes", memLimit,
		"gomaxprocs", maxprocs,
	)

	eng := engine.New(cfg, log)
	// Persistence must be loaded before accepting connections so that
	// AOF replay and RDB restore observe a quiescent engine.
	replayer := httpapi.NewReplayer(eng, cfg, log)
	if err := eng.EnablePersistence(replayer); err != nil {
		log.Error("persistence init failed", "err", err)
	}
	// The replica-apply path reuses the same HTTP-style dispatcher so
	// master → replica commands execute identically to a local call.
	eng.SetReplayRunner(replayer)
	eng.Start()
	defer eng.Stop()

	apiHandler := httpapi.NewRouter(eng, cfg, log)
	// Serve embedded dashboard; delegate /api/* to the API router.
	httpSrv := &http.Server{
		Addr:              ":" + cfg.HTTPPort,
		Handler:           webui.Handler(apiHandler, "/api/"),
		ReadHeaderTimeout: 5 * time.Second,
	}

	respSrv := resp.NewServer(":"+cfg.RESPPort, eng, log)

	go func() {
		log.Info("http api listening", "addr", httpSrv.Addr)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server failed", "err", err)
			os.Exit(1)
		}
	}()

	go func() {
		log.Info("resp server listening", "addr", respSrv.Addr())
		if err := respSrv.ListenAndServe(); err != nil {
			log.Error("resp server failed", "err", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("shutdown signal received")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown error", "err", err)
	}
	respSrv.Close()
	log.Info("bye")
}
