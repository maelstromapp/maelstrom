package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/coopernurse/barrister-go"
	"github.com/coopernurse/maelstrom/pkg/cert"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/config"
	"github.com/coopernurse/maelstrom/pkg/converge"
	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/evsource/cron"
	"github.com/coopernurse/maelstrom/pkg/evsource/poller"
	"github.com/coopernurse/maelstrom/pkg/maelstrom"
	"github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"net/http"
	httppprof "net/http/pprof"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
	_ "github.com/mattn/go-sqlite3"
)

var version string
var builddate string
var gitsha string

func mustStart(s *http.Server) {
	err := s.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		log.Error("maelstromd: unable to start HTTP server", "addr", s.Addr, "err", err)
		os.Exit(2)
	}
}

func initDb(sqlDriver, sqlDSN string) db.Db {
	sqlDb, err := db.NewSqlDb(sqlDriver, sqlDSN)
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

	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("maelstromd v%s built on %s commit %s\n", version, builddate, gitsha)
		os.Exit(0)
	}

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

	if conf.SqlDriver == "" || conf.SqlDsn == "" {
		log.Error("maelstromd: MAEL_SQL_DRIVER and MAEL_SQL_DSN env vars are required")
		os.Exit(2)
	}

	if conf.LogGcSeconds > 0 {
		go func() {
			var stats debug.GCStats
			for {
				time.Sleep(time.Duration(conf.LogGcSeconds) * time.Second)
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

	// channel that accepts shutdown requests
	shutdownCh := make(chan maelstrom.ShutdownFunc, 1)

	outboundIp, err := common.GetOutboundIP()
	if err != nil {
		log.Error("maelstromd: cannot resolve outbound IP address", "err", err)
		os.Exit(2)
	}
	peerUrl := fmt.Sprintf("http://%s:%d", outboundIp, conf.PrivatePort)

	db := initDb(conf.SqlDriver, conf.SqlDsn)
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

	resolver := maelstrom.NewDbResolver(db, certWrapper, time.Second)

	awsSession, err := session.NewSession()
	if err != nil {
		log.Warn("maelstromd: unable to init aws session", "err", err.Error())
	}

	pullState := maelstrom.NewPullState(dockerClient)
	imagePuller := maelstrom.NewImagePuller(dockerClient, db, pullState)

	nodeSvcImpl, err := maelstrom.NewNodeServiceImplFromDocker(db, dockerClient, conf.PrivatePort, peerUrl,
		conf.TotalMemory, conf.InstanceId, shutdownCh, awsSession, conf.TerminateCommand, pullState)
	if err != nil {
		log.Error("maelstromd: cannot create NodeService", "err", err)
		os.Exit(2)
	}

	convergeReg := nodeSvcImpl.GetConvergeRegistry()
	routerReg := convergeReg.GetRouterRegistry()

	if conf.DockerPruneMinutes > 0 {
		dockerPruner := maelstrom.NewDockerPruner(dockerClient, db, cancelCtx, conf.DockerPruneUnregImages,
			strings.Split(conf.DockerPruneUnregKeep, ","))
		daemonWG.Add(1)
		go dockerPruner.Run(time.Minute*time.Duration(conf.DockerPruneMinutes), daemonWG)
	}

	if conf.NodeLivenessSeconds > 0 {
		nodeSvcImpl.NodeLiveness = time.Second * time.Duration(conf.NodeLivenessSeconds)
	}

	daemonWG.Add(2)
	go nodeSvcImpl.RunNodeStatusLoop(time.Second*30, cancelCtx, daemonWG)
	go nodeSvcImpl.RunAutoscaleLoop(time.Minute, cancelCtx, daemonWG)
	if conf.AwsTerminateQueueUrl != "" {
		daemonWG.Add(1)
		go nodeSvcImpl.RunAwsAutoScaleTerminatePollerLoop(conf.AwsTerminateQueueUrl, conf.AwsTerminateMaxAgeSeconds,
			cancelCtx, daemonWG)
		log.Info("maelstromd: started AWS autoscale termination poller", "queueUrl", conf.AwsTerminateQueueUrl,
			"instanceId", conf.InstanceId)
	}
	if conf.AwsSpotTerminatePollSeconds > 0 {
		daemonWG.Add(1)
		interval := time.Second * time.Duration(conf.AwsSpotTerminatePollSeconds)
		go nodeSvcImpl.RunAwsSpotTerminatePollerLoop(interval, cancelCtx, daemonWG)
		log.Info("maelstromd: started AWS spot termination poller", "interval", interval.String())
	}
	log.Info("maelstromd: created NodeService", nodeSvcImpl.LogPairs()...)

	dockerMonitor := common.NewDockerImageMonitor(dockerClient, convergeReg, cancelCtx)
	dockerMonitor.RunAsync(daemonWG)

	publicSvr := maelstrom.NewGateway(resolver, routerReg, true, outboundIp.String())

	componentSubscribers := []maelstrom.ComponentSubscriber{convergeReg, resolver, imagePuller}

	v1Idl := barrister.MustParseIdlJson([]byte(v1.IdlJsonRaw))
	v1Impl := maelstrom.NewMaelServiceImpl(db, componentSubscribers, certWrapper, nodeSvcImpl.NodeId(),
		nodeSvcImpl.Cluster())
	v1Server := v1.NewJSONServer(v1Idl, true, v1Impl, nodeSvcImpl)
	logsHandler := maelstrom.NewLogsHandler(dockerClient)

	nodeSvcImpl.Cluster().SetLocalMaelstromService(v1Impl)

	privateGateway := maelstrom.NewGateway(resolver, routerReg, false, outboundIp.String())
	privateSvrMux := http.NewServeMux()
	if conf.Pprof {
		log.Info("maelstromd: binding pprof routes to /_mael/pprof/")
		privateSvrMux.Handle("/_mael/pprof/heap", httppprof.Handler("heap"))
		privateSvrMux.Handle("/_mael/pprof/cmdline", http.HandlerFunc(httppprof.Cmdline))
		privateSvrMux.Handle("/_mael/pprof/profile", http.HandlerFunc(httppprof.Profile))
		privateSvrMux.Handle("/_mael/pprof/symbol", http.HandlerFunc(httppprof.Symbol))
		privateSvrMux.Handle("/_mael/pprof/trace", http.HandlerFunc(httppprof.Trace))
	}
	privateSvrMux.Handle("/_mael/v1", &v1Server)
	privateSvrMux.Handle("/_mael/logs", logsHandler)
	privateSvrMux.Handle("/", privateGateway)

	var servers []*http.Server

	readTimeout := time.Duration(conf.HTTPReadTimeout) * time.Second
	writeTimeout := time.Duration(conf.HTTPWriteTimeout) * time.Second
	idleTimeout := time.Duration(conf.HTTPIdleTimeout) * time.Second

	if certWrapper == nil {
		servers = []*http.Server{
			{
				Addr:         fmt.Sprintf(":%d", conf.PublicPort),
				ReadTimeout:  readTimeout,
				WriteTimeout: writeTimeout,
				IdleTimeout:  idleTimeout,
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
		Addr:         fmt.Sprintf(":%d", conf.PrivatePort),
		ReadTimeout:  readTimeout,
		WriteTimeout: writeTimeout,
		IdleTimeout:  idleTimeout,
		Handler:      privateSvrMux,
	}
	go mustStart(privateSvr)
	servers = append(servers, privateSvr)

	log.Info("maelstromd: starting HTTP servers", "publicPort", conf.PublicPort, "privatePort", conf.PrivatePort)

	cronSvc := evcron.NewCronService(db, privateGateway, cancelCtx, nodeSvcImpl.NodeId(),
		time.Second*time.Duration(conf.CronRefreshSeconds))
	daemonWG.Add(1)
	go cronSvc.Run(daemonWG, false)

	evPoller := poller.NewEvPoller(nodeSvcImpl.NodeId(), cancelCtx, db, privateGateway, routerReg, awsSession)
	daemonWG.Add(1)
	go evPoller.Run(daemonWG)

	daemonWG.Add(1)
	go HandleShutdownSignal(servers, conf.ShutdownPauseSeconds, convergeReg, cancelFx, shutdownCh, daemonWG)

	daemonWG.Wait()
	err = db.ReleaseAllRoles(nodeSvcImpl.NodeId())
	if err != nil {
		log.Error("maelstromd: ReleaseAllRoles error", "err", err, "nodeId", nodeSvcImpl.NodeId())
	}
	log.Info("maelstromd: exiting")
}

func HandleShutdownSignal(svrs []*http.Server, pauseSeconds int, convergeReg *converge.Registry,
	cancelFx context.CancelFunc, shutdownCh chan maelstrom.ShutdownFunc, wg *sync.WaitGroup) {
	defer wg.Done()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var onShutdownFx func()
	select {
	case <-sigCh:
		break
	case onShutdownFx = <-shutdownCh:
		break
	}

	log.Info("maelstromd: received shutdown signal, stopping background goroutines")

	cancelFx()

	if pauseSeconds > 0 {
		log.Info("maelstromd: pausing before stopping HTTP servers", "seconds", pauseSeconds)
		time.Sleep(time.Second * time.Duration(pauseSeconds))
	}

	for _, s := range svrs {
		err := s.Shutdown(context.Background())
		if err != nil {
			log.Error("maelstromd: during HTTP server shutdown", "err", err)
		}
	}
	log.Info("maelstromd: HTTP servers shutdown gracefully")

	if pauseSeconds > 0 {
		log.Info("maelstromd: pausing before stopping containers", "seconds", pauseSeconds)
		time.Sleep(time.Second * time.Duration(pauseSeconds))
	}

	log.Info("maelstromd: stopping converge registry and containers")
	convergeReg.Shutdown()

	if onShutdownFx != nil {
		onShutdownFx()
		log.Info("maelstromd: shutdown callback called successfully")
	}
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
