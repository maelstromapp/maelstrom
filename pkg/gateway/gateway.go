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

	handler, err := g.router.GetHandlerAndRegisterRequest(comp)
	if err != nil {
		if err == v1.NotFound {
			respondText(rw, http.StatusNotFound, "No handler found for component: "+comp.Name)
		} else {
			log.Error("gateway: router.GetHandlerAndRegisterRequest", "err", err)
			respondText(rw, http.StatusInternalServerError, "Server Error")
		}
		return
	}
	if handler == nil {
		log.Error("gateway: router returned nil handler", "component", comp.Name)
		respondText(rw, http.StatusInternalServerError, "Server Error")
		return
	}

	handler.ServeHTTP(rw, req)
}

func respondText(rw http.ResponseWriter, statusCode int, body string) {
	rw.Header().Add("content-type", "text/plain")
	rw.WriteHeader(statusCode)
	_, err := rw.Write([]byte(body))
	if err != nil {
		log.Warn("gateway: respondText.Write error", "err", err.Error())
	}
}
