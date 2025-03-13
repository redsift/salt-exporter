package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kpetremann/salt-exporter/internal/logging"
	"github.com/kpetremann/salt-exporter/internal/metrics"
	"github.com/kpetremann/salt-exporter/pkg/event"
	"github.com/kpetremann/salt-exporter/pkg/listener"
	"github.com/kpetremann/salt-exporter/pkg/parser"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/rs/zerolog/log"
)

var (
	version = "unknown"
	commit  = "unknown"
	date    = "unknown"
)

func quit() {
	log.Warn().Msg("Bye.")
}

func printInfo(config Config) {
	log.Info().Str("Version", version).Send()
	log.Info().Str("Commit", commit).Send()
	log.Info().Str("Build time", date).Send()

	if config.Metrics.HealthMinions {
		log.Info().Msgf("health-minions: functions filters: %s", config.Metrics.SaltFunctionStatus.Filters.Functions)
		log.Info().Msgf("health-minions: states filters: %s", config.Metrics.SaltFunctionStatus.Filters.States)
	}

	if config.Metrics.Global.Filters.IgnoreTest {
		log.Info().Msg("test=True events will be ignored")
	}
	if config.Metrics.Global.Filters.IgnoreMock {
		log.Info().Msg("mock=True events will be ignored")
	}
}

func start(config Config) {
	listenSocket := fmt.Sprint(config.ListenAddress, ":", config.ListenPort)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Info().Msg("listening for events...")
	eventChan := make(chan event.SaltEvent)
	watchChan := make(chan event.WatchEvent)

	// listen and expose metric
	parser := parser.NewEventParser(false)
	eventListener := listener.NewEventListener(ctx, parser, eventChan)
	eventListener.SetIPCFilepath(config.IPCFile)

	if config.Metrics.HealthMinions {
		pkiWatcher, err := listener.NewPKIWatcher(ctx, config.PKIDir, watchChan)
		if err != nil {
			log.Fatal().Msgf("unable to watch PKI for minions change: %v", err) //nolint:gocritic // force exit
		}

		go pkiWatcher.StartWatching()
	}

	eventListener.Open()

	go eventListener.ListenEvents()
	go metrics.ExposeMetrics(ctx, eventChan, watchChan, config.Metrics)

	// start http server
	log.Info().Msg("exposing metrics on " + listenSocket + "/metrics")

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	httpServer := http.Server{Addr: listenSocket, Handler: mux, ReadHeaderTimeout: 2 * time.Second}

	go func() {
		var err error

		if !config.TLS.Enabled {
			err = httpServer.ListenAndServe()
		} else {
			err = httpServer.ListenAndServeTLS(config.TLS.Certificate, config.TLS.Key)
		}

		if err != nil {
			log.Error().Err(err).Send()
			stop()
		}
	}()

	// exiting
	<-ctx.Done()
	if err := httpServer.Shutdown(context.Background()); err != nil {
		log.Error().Err(err).Send()
	}
}

func main() {
	defer quit()
	logging.Configure()

	config, err := ReadConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to load settings during initialization") //nolint:gocritic // force exit
	}

	logging.SetLevel(config.LogLevel)
	printInfo(config)
	start(config)
}
