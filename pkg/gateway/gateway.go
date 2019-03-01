package gateway

import (
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"log"
	"net/http"
)

func NewGateway(r ComponentResolver, f HandlerFactory) *Gateway {
	return &Gateway{
		compResolver:   r,
		handlerFactory: f,
	}
}

type Gateway struct {
	compResolver   ComponentResolver
	handlerFactory HandlerFactory
}

func (g *Gateway) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	comp, err := g.compResolver.ByHTTPRequest(req)
	if err != nil {
		if err == v1.NotFound {
			respondText(rw, http.StatusNotFound, "No component matches the request")
		} else {
			log.Printf("ERROR ByHTTPRequest: %v", err)
			respondText(rw, http.StatusInternalServerError, "Server Error")
		}
		return
	}

	handler, err := g.handlerFactory.GetHandlerAndRegisterRequest(comp)
	if err != nil {
		if err == v1.NotFound {
			respondText(rw, http.StatusNotFound, "No handler found for component: "+comp.Name)
		} else {
			log.Printf("ERROR handlerFactory.ForComponent: %v", err)
			respondText(rw, http.StatusInternalServerError, "Server Error")
		}
		return
	}
	if handler == nil {
		log.Printf("ERROR handlerFactory returned nil handler for component: %s\n", comp.Name)
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
		log.Printf("WARNING respondText.Write error: %v", err)
	}
}
