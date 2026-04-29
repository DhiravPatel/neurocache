package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
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

func main() {
	cfg := config.Load()
	log := logger.New(cfg.LogLevel, cfg.LogFormat)
	log.Info("neurocache starting", "version", "0.3.0", "http_port", cfg.HTTPPort, "resp_port", cfg.RESPPort)

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
