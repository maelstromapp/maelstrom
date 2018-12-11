package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	var revProxyPort = flag.Int("revProxyPort", 80, "Port used for reverse proxying")
	var mgmtPort = flag.Int("mgmtPort", 8374, "Port used for management operations")
	flag.Parse()

	log.Printf("maelstromd starting")

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
			Handler:      &ManagementHandler{},
		},
	}

	for _, s := range servers {
		log.Printf("Starting HTTP server on port: %s", s.Addr)
		go func() {
			err := s.ListenAndServe()
			if err != nil {
				log.Printf("ERROR starting HTTP server: %s err: %v", s.Addr, err)
			}
		}()
	}

	shutdownDone := make(chan struct{})
	go HandleShutdownSignal(servers, shutdownDone)
	<-shutdownDone
}

func HandleShutdownSignal(svrs []*http.Server, shutdownDone chan (struct{})) {
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

type ManagementHandler struct {
}

func (r *ManagementHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	fmt.Fprintf(rw, "Hello mgmt server")
}
