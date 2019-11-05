package maelstrom

import (
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/cert"
	"github.com/coopernurse/maelstrom/pkg/v1"
	"net/http"
	"sort"
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

func (r *DbComponentResolver) OnComponentNotification(cn v1.DataChangedUnion) {
	r.lock.Lock()

	// remove component by name to force reload
	if cn.PutComponent != nil {
		delete(r.componentsByName, cn.PutComponent.Name)
	}
	if cn.RemoveComponent != nil {
		delete(r.componentsByName, cn.RemoveComponent.Name)
	}

	// force reload of event sources
	r.eventSources = nil

	r.lock.Unlock()
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

func (r *DbComponentResolver) allEnabledHttpEventSources() (sources []v1.EventSource, err error) {
	r.lock.Lock()
	now := time.Now()
	if now.After(r.eventSourcesExpires) {
		r.eventSources = nil
		r.eventSourcesExpires = now.Add(r.cacheDuration)
	} else {
		sources = r.eventSources
	}

	if r.eventSources == nil {
		sources, err = allEnabledHttpEventSources(r.db, r.certWrapper)
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

	httpEventSources, err := r.allEnabledHttpEventSources()
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

			if es.Http.StripPrefix && es.Http.PathPrefix != "" {
				req.URL.Path = path[len(es.Http.PathPrefix):]
			}

			return r.ByName(es.ComponentName)
		}
	}

	return v1.Component{}, NotFound
}

func httpEventSourceMatches(es v1.EventSource, hostname string, path string) bool {
	if es.Http == nil || (es.Http.Hostname == "" && es.Http.PathPrefix == "") {
		return false
	}
	if es.Http.Hostname == "" && strings.HasPrefix(path, es.Http.PathPrefix) {
		return true
	}
	return hostname == es.Http.Hostname &&
		(es.Http.PathPrefix == "" || strings.HasPrefix(path, es.Http.PathPrefix))
}

func allEnabledHttpEventSources(db Db, certWrapper *cert.CertMagicWrapper) ([]v1.EventSource, error) {
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
			if es.Enabled {
				allSources = append(allSources, es.EventSource)
				if certWrapper != nil && es.EventSource.Http != nil && es.EventSource.Http.Hostname != "" {
					certWrapper.AddHost(es.EventSource.Http.Hostname)
				}
			}
		}
		nextToken = output.NextToken
		if nextToken == "" {
			break
		}
	}
	sort.Sort(httpEventSourcesForResolver(allSources))
	return allSources, nil
}
