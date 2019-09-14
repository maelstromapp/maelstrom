package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/coopernurse/barrister-go"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/cert"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/config"
	"gitlab.com/coopernurse/maelstrom/pkg/gateway"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"sync"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
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
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGQUIT)
		buf := make([]byte, 1<<20)
		for {
			<-sigs
			stacklen := runtime.Stack(buf, true)
			fmt.Printf("=== received SIGQUIT ===\n*** goroutine dump...\n%s\n*** end\n", buf[:stacklen])
		}
	}()

	if os.Getenv("LOGXI") == "" {
		log.DefaultLog.SetLevel(log.LevelInfo)
	}
	if os.Getenv("LOGXI_FORMAT") == "" {
		log.ProcessLogxiFormatEnv("happy,maxcol=120")
	}

	var envConfigFile = flag.String("f", "", "Path to env config file. Optional.")
	flag.Parse()

	var conf config.Config
	var err error
	if *envConfigFile != "" {
		conf, err = config.FromEnvFile(*envConfigFile)
	} else {
		conf, err = config.FromEnv()
	}
	if err != nil {
		log.Error("maelstromd: cannot load config", "err", err)
		os.Exit(2)
	}

	if conf.SqlDriver == "" || conf.SqlDSN == "" {
		log.Error("maelstromd: MAEL_SQLDRIVER and MAEL_PUBLICPORT env vars are required")
		os.Exit(2)
	}

	if conf.LogGCSeconds > 0 {
		go func() {
			var stats debug.GCStats
			for {
				time.Sleep(time.Duration(conf.LogGCSeconds) * time.Second)
				debug.ReadGCStats(&stats)
				log.Info("stats", "last", stats.LastGC, "num", stats.NumGC, "pause", stats.PauseTotal.String())
			}
		}()
	}

	if conf.CpuProfileFilename != "" {
		f, err := os.Create(conf.CpuProfileFilename)
		if err != nil {
			log.Error("maelstromd: cannot create profile file", "err", err)
			os.Exit(2)
		}
		err = pprof.StartCPUProfile(f)
		defer pprof.StopCPUProfile()
		if err != nil {
			log.Error("maelstromd: cannot start profiler", "err", err)
			os.Exit(2)
		}
	}

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
	peerUrl := fmt.Sprintf("http://%s:%d", outboundIp, conf.PrivatePort)

	db := initDb(conf.SqlDriver, conf.SqlDSN)
	dockerClient, err := docker.NewEnvClient()
	if err != nil {
		log.Error("maelstromd: cannot create docker client", "err", err)
		os.Exit(2)
	}

	// to use its certificates and solve the TLS-ALPN challenge,
	// you can get a TLS config to use in a TLS listener!
	certWrapper := initCertMagic()

	cancelCtx, cancelFx := context.WithCancel(context.Background())
	daemonWG := &sync.WaitGroup{}

	resolver := gateway.NewDbResolver(db, certWrapper, time.Second)
	handlerFactory, err := gateway.NewDockerHandlerFactory(dockerClient, resolver, db, cancelCtx, conf.PrivatePort)
	if err != nil {
		log.Error("maelstromd: cannot create handler factory", "err", err)
		os.Exit(2)
	}

	dockerMonitor := common.NewDockerImageMonitor(dockerClient, handlerFactory, cancelCtx)
	dockerMonitor.RunAsync(daemonWG)

	nodeSvcImpl, err := gateway.NewNodeServiceImplFromDocker(handlerFactory, db, dockerClient, peerUrl)
	if err != nil {
		log.Error("maelstromd: cannot create NodeService", "err", err)
		os.Exit(2)
	}
	router := gateway.NewRouter(nodeSvcImpl, handlerFactory, nodeSvcImpl.NodeId(), outboundIp.String(), cancelCtx)
	nodeSvcImpl.Cluster().AddObserver(router)
	daemonWG.Add(2)
	go nodeSvcImpl.RunNodeStatusLoop(time.Second*30, cancelCtx, daemonWG)
	go nodeSvcImpl.RunAutoscaleLoop(time.Minute, cancelCtx, daemonWG)
	log.Info("maelstromd: created NodeService", nodeSvcImpl.LogPairs()...)

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
				Addr:         fmt.Sprintf(":%d", conf.PublicPort),
				ReadTimeout:  30 * time.Second,
				WriteTimeout: 600 * time.Second,
				Handler:      publicSvr,
			},
		}
		go mustStart(servers[0])
	} else {
		servers, err = certWrapper.Start(publicSvr, conf.PublicPort, conf.PublicHTTPSPort)
		if err != nil {
			log.Error("maelstromd: cannot start public server", "err", err)
			os.Exit(2)
		}
	}

	privateSvr := &http.Server{
		Addr:        fmt.Sprintf(":%d", conf.PrivatePort),
		ReadTimeout: 30 * time.Second,
		Handler:     privateSvrMux,
	}
	go mustStart(privateSvr)
	servers = append(servers, privateSvr)

	awsSession, err := session.NewSession()
	if awsSession != nil {
		log.Info("maelstromd: aws session initialized")
	}

	log.Info("maelstromd: starting HTTP servers", "publicPort", conf.PublicPort, "privatePort", conf.PrivatePort)

	cronSvc := gateway.NewCronService(db, privateGateway, cancelCtx, nodeSvcImpl.NodeId(),
		time.Second*time.Duration(conf.CronRefreshSeconds))
	daemonWG.Add(1)
	go cronSvc.Run(daemonWG)

	evPoller := gateway.NewEvPoller(nodeSvcImpl.NodeId(), cancelCtx, db, router, awsSession)
	daemonWG.Add(1)
	go evPoller.Run(daemonWG)

	daemonWG.Add(1)
	go HandleShutdownSignal(servers, cancelFx, daemonWG)

	daemonWG.Wait()
	err = db.ReleaseAllRoles(nodeSvcImpl.NodeId())
	if err != nil {
		log.Error("maelstromd: ReleaseAllRoles error", "err", err, "nodeId", nodeSvcImpl.NodeId())
	}
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
