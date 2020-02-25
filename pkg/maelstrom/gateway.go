package maelstrom

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/revproxy"
	"github.com/coopernurse/maelstrom/pkg/router"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
)

func NewGateway(r ComponentResolver, routerReg *router.Registry, public bool,
	myIpAddr string) *Gateway {
	return &Gateway{
		compResolver: r,
		routerReg:    routerReg,
		public:       public,
		myIpAddr:     myIpAddr,
	}
}

type Gateway struct {
	compResolver ComponentResolver
	routerReg    *router.Registry
	public       bool
	myIpAddr     string
}

func (g *Gateway) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	// Handle health check URL
	if req.RequestURI == "/_mael_health_check" {
		respondText(rw, http.StatusOK, "OK")
		return
	}

	// Resolve Component based on hostname/path
	comp, err := g.compResolver.ByHTTPRequest(req, g.public)
	if err != nil {
		if err == db.NotFound {
			respondText(rw, http.StatusNotFound, "No component matches the request")
		} else {
			log.Error("gateway: compResolver.ByHTTPRequest", "err", err)
			respondText(rw, http.StatusInternalServerError, "Server Error")
		}
		return
	}

	// Set X-Forwarded-For header
	xForward := req.Header.Get("X-Forwarded-For")
	if xForward != "" {
		xForward += ", "
	}
	xForward += g.myIpAddr
	req.Header.Set("X-Forwarded-For", xForward)

	g.Route(rw, req, &comp, g.public)
}

func (g *Gateway) Route(rw http.ResponseWriter, req *http.Request, comp *v1.Component, publicGateway bool) {
	// Set Deadline
	var reqStartNano int64
	var deadlineNano int64
	if !publicGateway {
		deadlineStr := req.Header.Get("MAELSTROM-DEADLINE-NANO")
		if deadlineStr != "" {
			deadlineNano, _ = strconv.ParseInt(deadlineStr, 10, 64)
		}
		reqStartStr := req.Header.Get("MAELSTROM-START-NANO")
		if reqStartStr != "" {
			reqStartNano, _ = strconv.ParseInt(reqStartStr, 10, 64)
		}
	}

	deadline := componentReqDeadline(deadlineNano, comp)
	ctx, ctxCancel := context.WithDeadline(context.Background(), deadline)
	defer ctxCancel()

	if req.Header.Get("MAELSTROM-DEADLINE-NANO") == "" {
		req.Header.Set("MAELSTROM-DEADLINE-NANO", strconv.FormatInt(deadline.UnixNano(), 10))
	}
	req = req.WithContext(ctx)

	// Send request to dispatcher
	preferLocal := req.Header.Get("MAELSTROM-RELAY-PATH") != ""
	compReq := revproxy.NewRequest(req, rw, comp, preferLocal)
	if reqStartNano != 0 && reqStartNano < compReq.StartTime.UnixNano() {
		compReq.StartTime = time.Unix(0, reqStartNano)
	}
	if req.Header.Get("MAELSTROM-START-NANO") == "" {
		req.Header.Set("MAELSTROM-START-NANO", strconv.FormatInt(compReq.StartTime.UnixNano(), 10))
	}
	g.routerReg.ByComponent(comp.Name).Route(ctx, compReq)

	// Block on result, or timeout
	select {
	case <-compReq.Done:
		return
	case <-ctx.Done():
		msg := "gateway: Timeout proxying component: " + comp.Name
		log.Warn(msg, "component", comp.Name, "version", comp.Version)
		respondText(rw, http.StatusGatewayTimeout, msg)
		return
	}
}

func componentReqDeadline(deadlineNanoFromHeader int64, comp *v1.Component) time.Time {
	if deadlineNanoFromHeader == 0 {
		maxDur := comp.MaxDurationSeconds
		if maxDur <= 0 {
			maxDur = 300
		}
		startTime := time.Now()
		return startTime.Add(time.Duration(maxDur) * time.Second)
	} else {
		return time.Unix(0, deadlineNanoFromHeader)
	}
}

func respondText(rw http.ResponseWriter, statusCode int, body string) {
	rw.Header().Add("content-type", "text/plain")
	rw.WriteHeader(statusCode)
	_, err := rw.Write([]byte(body))
	if err != nil {
		log.Warn("gateway: respondText.Write error", "err", err.Error())
	}
}
