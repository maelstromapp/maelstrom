package maelstrom

import (
	"fmt"
	"gitlab.com/coopernurse/maelstrom/pkg/cert"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"strings"
	"sync"
	"time"
)

type ComponentResolver interface {
	ByName(componentName string) (v1.Component, error)
	ByHTTPRequest(req *http.Request, public bool) (v1.Component, error)
}

func NewDbResolver(db Db, certWrapper *cert.CertMagicWrapper, cacheDuration time.Duration) *DbComponentResolver {
	return &DbComponentResolver{
		db:            db,
		certWrapper:   certWrapper,
		cacheDuration: cacheDuration,
		lock:          &sync.Mutex{},
	}
}

type DbComponentResolver struct {
	db            Db
	certWrapper   *cert.CertMagicWrapper
	cacheDuration time.Duration
	lock          *sync.Mutex

	// caches
	eventSources        []v1.EventSource
	eventSourcesExpires time.Time
	componentsByName    map[string]v1.Component
	componentsExpires   time.Time
}

func (r *DbComponentResolver) ByName(componentName string) (comp v1.Component, err error) {
	ok := false
	r.lock.Lock()
	now := time.Now()
	if now.After(r.componentsExpires) {
		r.componentsByName = make(map[string]v1.Component)
		r.componentsExpires = now.Add(r.cacheDuration)
	} else {
		comp, ok = r.componentsByName[componentName]
	}

	if !ok {
		comp, err = r.db.GetComponent(componentName)
		if err == nil {
			r.componentsByName[componentName] = comp
		}
	}

	r.lock.Unlock()
	return
}

func (r *DbComponentResolver) allHttpEventSources() (sources []v1.EventSource, err error) {
	r.lock.Lock()
	now := time.Now()
	if now.After(r.eventSourcesExpires) {
		r.eventSources = nil
		r.eventSourcesExpires = now.Add(r.cacheDuration)
	} else {
		sources = r.eventSources
	}

	if r.eventSources == nil {
		sources, err = allHttpEventSources(r.db, r.certWrapper)
		if err == nil {
			r.eventSources = sources
		}
	}

	r.lock.Unlock()
	return
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

	httpEventSources, err := r.allHttpEventSources()
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

	return v1.Component{}, NotFound
}

func httpEventSourceMatches(es v1.EventSource, hostname string, path string) bool {
	return es.Http != nil && hostname == es.Http.Hostname &&
		(es.Http.PathPrefix == "" || strings.HasPrefix(path, es.Http.PathPrefix))
}

func allHttpEventSources(db Db, certWrapper *cert.CertMagicWrapper) ([]v1.EventSource, error) {
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
