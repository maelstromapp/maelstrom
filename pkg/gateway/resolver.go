package gateway

import (
	"fmt"
	"gitlab.com/coopernurse/maelstrom/pkg/cert"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"strings"
)

type ComponentResolver interface {
	ByName(componentName string) (v1.Component, error)
	ByHTTPRequest(req *http.Request, public bool) (v1.Component, error)
}

func NewDbResolver(db v1.Db, certWrapper *cert.CertMagicWrapper) *DbComponentResolver {
	return &DbComponentResolver{
		db:          db,
		certWrapper: certWrapper,
	}
}

type DbComponentResolver struct {
	db          v1.Db
	certWrapper *cert.CertMagicWrapper
}

func (r *DbComponentResolver) ByName(componentName string) (v1.Component, error) {
	return r.db.GetComponent(componentName)
}

func (r *DbComponentResolver) ByHTTPRequest(req *http.Request, public bool) (v1.Component, error) {
	// private gateway allows component resolution by name or HTTP event source config
	// public gateway only routes by HTTP event source
	if !public {
		compName := req.Header.Get("MAELSTROM-COMPONENT")
		if compName != "" {
			return r.ByName(compName)
		}
	}

	httpEventSources, err := allHttpEventSources(r.db, r.certWrapper)
	if err != nil {
		return v1.Component{}, err
	}

	hostname := req.Host
	pos := strings.Index(hostname, ":")
	if pos > -1 {
		hostname = hostname[0:pos]
	}
	path := req.URL.Path

	for _, es := range httpEventSources {
		if httpEventSourceMatches(es, hostname, path) {
			return r.ByName(es.ComponentName)
		}
	}

	return v1.Component{}, v1.NotFound
}

func httpEventSourceMatches(es v1.EventSource, hostname string, path string) bool {
	return es.Http != nil && hostname == es.Http.Hostname &&
		(es.Http.PathPrefix == "" || strings.HasPrefix(path, es.Http.PathPrefix))
}

func allHttpEventSources(db v1.Db, certWrapper *cert.CertMagicWrapper) ([]v1.EventSource, error) {
	nextToken := ""
	input := v1.ListEventSourcesInput{EventSourceType: v1.EventSourceTypeHttp}
	allSources := make([]v1.EventSource, 0)
	for {
		input.NextToken = nextToken
		output, err := db.ListEventSources(input)
		if err != nil {
			return nil, fmt.Errorf("resolver ListEventSources error: %v", err)
		}
		for _, es := range output.EventSources {
			allSources = append(allSources, es)
			if certWrapper != nil && es.Http != nil && es.Http.Hostname != "" {
				certWrapper.AddHost(es.Http.Hostname)
			}
		}
		nextToken = output.NextToken
		if nextToken == "" {
			break
		}
	}
	return allSources, nil
}
