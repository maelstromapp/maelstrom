package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/coopernurse/barrister-go"
	"gitlab.com/coopernurse/maelstrom/pkg/db"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func mustStart(s *http.Server) {
	log.Printf("Starting HTTP server on port: %s", s.Addr)
	err := s.ListenAndServe()
	if err != nil {
		log.Printf("ERROR starting HTTP server: %s err: %v", s.Addr, err)
		os.Exit(2)
	}
}

func initDb(sqlDriver, sqlDSN string) db.Db {
	sqlDb, err := db.NewSqlDb(sqlDriver, sqlDSN)
	if err != nil {
		log.Printf("ERROR creating SqlDb using driver: %s err: %v", sqlDriver, err)
		os.Exit(2)
	}
	err = sqlDb.Migrate()
	if err != nil {
		log.Printf("ERROR running migrate using driver: %s err: %v", sqlDriver, err)
		os.Exit(2)
	}
	return sqlDb
}

func main() {
	var revProxyPort = flag.Int("revProxyPort", 80, "Port used for reverse proxying")
	var mgmtPort = flag.Int("mgmtPort", 8374, "Port used for management operations")
	var sqlDriver = flag.String("sqlDriver", "", "database/sql driver to use. If so, -sqlDSN is required")
	var sqlDSN = flag.String("sqlDSN", "", "DSN for sql database")
	flag.Parse()

	log.Printf("maelstromd starting")

	v1Idl := barrister.MustParseIdlJson([]byte(v1.IdlJsonRaw))
	v1Impl := v1.NewV1(initDb(*sqlDriver, *sqlDSN))
	v1Server := v1.NewJSONServer(v1Idl, true, v1Impl)
	mgmtMux := http.NewServeMux()
	mgmtMux.Handle("/v1", &v1Server)

	servers := []*http.Server{
		{
			Addr:         fmt.Sprintf(":%d", *revProxyPort),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			Handler:      &ReverseProxyHandler{},
		},
		{
			Addr:         fmt.Sprintf(":%d", *mgmtPort),
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			Handler:      mgmtMux,
		},
	}

	for _, s := range servers {
		go mustStart(s)
	}

	shutdownDone := make(chan struct{})
	go HandleShutdownSignal(servers, shutdownDone)
	<-shutdownDone
}

func HandleShutdownSignal(svrs []*http.Server, shutdownDone chan struct{}) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Printf("Received shutdown signal, stopping HTTP servers")

	for _, s := range svrs {
		err := s.Shutdown(context.Background())
		if err != nil {
			log.Printf("ERROR during HTTP server shutdown: %v", err)
		}
	}
	log.Printf("HTTP servers shutdown gracefully")
	close(shutdownDone)
}

type ReverseProxyHandler struct {
}

func (r *ReverseProxyHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(rw, "Hello reverse proxy server")
}
