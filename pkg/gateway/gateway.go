package gateway

import (
	"github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
)

func NewGateway(r ComponentResolver, router *Router, public bool) *Gateway {
	return &Gateway{
		compResolver: r,
		router:       router,
		public:       public,
	}
}

type Gateway struct {
	compResolver ComponentResolver
	router       *Router
	public       bool
}

func (g *Gateway) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	if req.RequestURI == "/_mael_health_check" {
		respondText(rw, http.StatusOK, "OK")
		return
	}

	comp, err := g.compResolver.ByHTTPRequest(req, g.public)
	if err != nil {
		if err == v1.NotFound {
			respondText(rw, http.StatusNotFound, "No component matches the request")
		} else {
			log.Error("gateway: compResolver.ByHTTPRequest", "err", err)
			respondText(rw, http.StatusInternalServerError, "Server Error")
		}
		return
	}

	// TODO: need to look for a request header with deadline and adjust accordingly
	// should only look for header if gateway is private

	g.router.Route(rw, req, comp)
}

func respondText(rw http.ResponseWriter, statusCode int, body string) {
	rw.Header().Add("content-type", "text/plain")
	rw.WriteHeader(statusCode)
	_, err := rw.Write([]byte(body))
	if err != nil {
		log.Warn("gateway: respondText.Write error", "err", err.Error())
	}
}
