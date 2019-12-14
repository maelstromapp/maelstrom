package maelstrom

import (
	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/maelstrom/component"
	"github.com/mgutz/logxi/v1"
	"net/http"
)

func NewGateway(r ComponentResolver, dispatcher *component.Dispatcher, public bool,
	myIpAddr string) *Gateway {
	return &Gateway{
		compResolver: r,
		dispatcher:   dispatcher,
		public:       public,
		myIpAddr:     myIpAddr,
	}
}

type Gateway struct {
	compResolver ComponentResolver
	dispatcher   *component.Dispatcher
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

	g.dispatcher.Route(rw, req, &comp, g.public)
}

func respondText(rw http.ResponseWriter, statusCode int, body string) {
	rw.Header().Add("content-type", "text/plain")
	rw.WriteHeader(statusCode)
	_, err := rw.Write([]byte(body))
	if err != nil {
		log.Warn("gateway: respondText.Write error", "err", err.Error())
	}
}
