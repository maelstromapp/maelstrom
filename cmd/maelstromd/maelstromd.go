package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/coopernurse/barrister-go"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/gateway"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func mustStart(s *http.Server) {
	err := s.ListenAndServe()
	if err != nil {
		log.Error("maelstromd: unable to start HTTP server", "addr", s.Addr, "err", err)
		os.Exit(2)
	}
}

func initDb(sqlDriver, sqlDSN string) v1.Db {
	sqlDb, err := v1.NewSqlDb(sqlDriver, sqlDSN)
	if err != nil {
		log.Error("maelstromd: cannot create SqlDb", "driver", sqlDriver, "err", err)
		os.Exit(2)
	}
	err = sqlDb.Migrate()
	if err != nil {
		log.Error("maelstromd: cannot run migrate", "driver", sqlDriver, "err", err)
		os.Exit(2)
	}
	return sqlDb
}

func main() {
	if os.Getenv("LOGXI") == "" {
		log.DefaultLog.SetLevel(log.LevelInfo)
	}
	if os.Getenv("LOGXI_FORMAT") == "" {
		log.ProcessLogxiFormatEnv("happy,maxcol=120")
	}

	var publicPort = flag.Int("publicPort", 80, "Port used for public reverse proxying")
	var privatePort = flag.Int("privatePort", 8374, "Port used for private routing and management operations")
	var sqlDriver = flag.String("sqlDriver", "", "database/sql driver to use. If so, -sqlDSN is required")
	var sqlDSN = flag.String("sqlDSN", "", "DSN for sql database")
	var cronRefreshSec = flag.Int("cronRefreshSec", 60, "Interval to refresh cron rules from db")
	flag.Parse()

	log.Info("maelstromd: starting")

	db := initDb(*sqlDriver, *sqlDSN)
	dockerClient, err := docker.NewEnvClient()
	if err != nil {
		log.Error("maelstromd: cannot create docker client", "err", err)
		os.Exit(2)
	}

	resolver := gateway.NewDbResolver(db)
	handlerFactory, err := gateway.NewDockerHandlerFactory(dockerClient, resolver, *privatePort)
	if err != nil {
		log.Error("maelstromd: cannot create handler factory", "err", err)
		os.Exit(2)
	}
	publicSvr := gateway.NewGateway(resolver, handlerFactory, true)

	componentSubscribers := []v1.ComponentSubscriber{handlerFactory}

	v1Idl := barrister.MustParseIdlJson([]byte(v1.IdlJsonRaw))
	v1Impl := v1.NewV1(db, componentSubscribers)
	v1Server := v1.NewJSONServer(v1Idl, true, v1Impl)
	logsHandler := gateway.NewLogsHandler(dockerClient)

	privateGateway := gateway.NewGateway(resolver, handlerFactory, false)
	privateSvrMux := http.NewServeMux()
	privateSvrMux.Handle("/_mael/v1", &v1Server)
	privateSvrMux.Handle("/_mael/logs", logsHandler)
	privateSvrMux.Handle("/", privateGateway)

	servers := []*http.Server{
		{
			Addr:         fmt.Sprintf(":%d", *publicPort),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 600 * time.Second,
			Handler:      publicSvr,
		},
		{
			Addr:        fmt.Sprintf(":%d", *privatePort),
			ReadTimeout: 30 * time.Second,
			Handler:     privateSvrMux,
		},
	}

	log.Info("maelstromd: starting HTTP servers", "publicPort", publicPort, "privatePort", privatePort)
	for _, s := range servers {
		go mustStart(s)
	}

	cancelCtx, cancelFx := context.WithCancel(context.Background())
	dockerMonitor := common.NewDockerImageMonitor(dockerClient, handlerFactory, cancelCtx)
	dockerMonitor.RunAsync()

	cronSvc := gateway.NewCronService(db, privateGateway, cancelCtx, time.Second*time.Duration(*cronRefreshSec))
	go cronSvc.Run()

	shutdownDone := make(chan struct{})
	go HandleShutdownSignal(servers, cancelFx, shutdownDone)
	<-shutdownDone
}

func HandleShutdownSignal(svrs []*http.Server, cancelFx context.CancelFunc, shutdownDone chan struct{}) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info("maelstromd: received shutdown signal, stopping HTTP servers")

	cancelFx()

	for _, s := range svrs {
		err := s.Shutdown(context.Background())
		if err != nil {
			log.Error("maelstromd: during HTTP server shutdown", "err", err)
		}
	}
	log.Info("maelstromd: HTTP servers shutdown gracefully")
	close(shutdownDone)
}
