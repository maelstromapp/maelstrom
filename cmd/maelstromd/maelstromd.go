package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/coopernurse/barrister-go"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/cert"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/gateway"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func mustStart(s *http.Server) {
	err := s.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
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
	var publicHTTPSPort = flag.Int("publicHTTPSPort", 443, "HTTPS Port used for public reverse proxying")
	var privatePort = flag.Int("privatePort", 8374, "Port used for private routing and management operations")
	var sqlDriver = flag.String("sqlDriver", "", "database/sql driver to use. If so, -sqlDSN is required")
	var sqlDSN = flag.String("sqlDSN", "", "DSN for sql database")
	var cronRefreshSec = flag.Int("cronRefreshSec", 60, "Interval to refresh cron rules from db")
	flag.Parse()

	log.Info("maelstromd: starting")

	// increase default idle conns so that local roundtripper gets decent socket reuse
	// this prevents the creation of tons of new sockets when relaying under high load, resulting
	// in "cannot assign requested address" errors
	// see: https://stackoverflow.com/questions/39813587/go-client-program-generates-a-lot-a-sockets-in-time-wait-state
	http.DefaultTransport.(*http.Transport).MaxIdleConnsPerHost = 500

	outboundIp, err := common.GetOutboundIP()
	if err != nil {
		log.Error("maelstromd: cannot resolve outbound IP address", "err", err)
		os.Exit(2)
	}
	peerUrl := fmt.Sprintf("http://%s:%d", outboundIp, *privatePort)

	db := initDb(*sqlDriver, *sqlDSN)
	dockerClient, err := docker.NewEnvClient()
	if err != nil {
		log.Error("maelstromd: cannot create docker client", "err", err)
		os.Exit(2)
	}

	// to use its certificates and solve the TLS-ALPN challenge,
	// you can get a TLS config to use in a TLS listener!
	certWrapper := initCertMagic()

	resolver := gateway.NewDbResolver(db, certWrapper)
	handlerFactory, err := gateway.NewDockerHandlerFactory(dockerClient, resolver, *privatePort)
	if err != nil {
		log.Error("maelstromd: cannot create handler factory", "err", err)
		os.Exit(2)
	}

	cancelCtx, cancelFx := context.WithCancel(context.Background())
	daemonWG := &sync.WaitGroup{}
	dockerMonitor := common.NewDockerImageMonitor(dockerClient, handlerFactory, cancelCtx)
	dockerMonitor.RunAsync(daemonWG)

	nodeSvcImpl, err := gateway.NewNodeServiceImplFromDocker(handlerFactory, db, dockerClient, peerUrl)
	if err != nil {
		log.Error("maelstromd: cannot create NodeService", "err", err)
		os.Exit(2)
	}
	daemonWG.Add(1)
	go nodeSvcImpl.RunNodeStatusLoop(time.Minute, cancelCtx, daemonWG)
	log.Info("maelstromd: created NodeService", nodeSvcImpl.LogPairs()...)

	router := gateway.NewRouter(nodeSvcImpl.NodeId(), handlerFactory, nodeSvcImpl)
	nodeSvcImpl.Cluster().AddObserver(router)

	publicSvr := gateway.NewGateway(resolver, router, true)

	componentSubscribers := []v1.ComponentSubscriber{handlerFactory}

	v1Idl := barrister.MustParseIdlJson([]byte(v1.IdlJsonRaw))
	v1Impl := v1.NewV1(db, componentSubscribers, certWrapper)
	v1Server := v1.NewJSONServer(v1Idl, true, v1Impl, nodeSvcImpl)
	logsHandler := gateway.NewLogsHandler(dockerClient)

	privateGateway := gateway.NewGateway(resolver, router, false)
	privateSvrMux := http.NewServeMux()
	privateSvrMux.Handle("/_mael/v1", &v1Server)
	privateSvrMux.Handle("/_mael/logs", logsHandler)
	privateSvrMux.Handle("/", privateGateway)

	var servers []*http.Server

	if certWrapper == nil {
		servers = []*http.Server{
			{
				Addr:         fmt.Sprintf(":%d", *publicPort),
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 600 * time.Second,
				Handler:      publicSvr,
			},
		}
		go mustStart(servers[0])
	} else {
		servers, err = certWrapper.Start(publicSvr, *publicPort, *publicHTTPSPort)
		if err != nil {
			log.Error("maelstromd: cannot start public server", "err", err)
			os.Exit(2)
		}
	}

	privateSvr := &http.Server{
		Addr:        fmt.Sprintf(":%d", *privatePort),
		ReadTimeout: 30 * time.Second,
		Handler:     privateSvrMux,
	}
	go mustStart(privateSvr)
	servers = append(servers, privateSvr)

	log.Info("maelstromd: starting HTTP servers", "publicPort", publicPort, "privatePort", privatePort)

	cronSvc := gateway.NewCronService(db, privateGateway, cancelCtx, time.Second*time.Duration(*cronRefreshSec))
	daemonWG.Add(1)
	go cronSvc.Run(daemonWG)

	daemonWG.Add(1)
	go HandleShutdownSignal(servers, cancelFx, daemonWG)

	daemonWG.Wait()
	log.Info("maelstromd: exiting")
}

func HandleShutdownSignal(svrs []*http.Server, cancelFx context.CancelFunc, wg *sync.WaitGroup) {
	defer wg.Done()
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
}

func initCertMagic() *cert.CertMagicWrapper {
	email := os.Getenv("LETSENCRYPT_EMAIL")
	if email == "" {
		return nil
	}

	log.Info("malestromd: letsencrypt enabled", "email", email)

	return cert.NewCertMagicWrapper(cert.CertMagicOptions{
		Email: email,
	})
}
