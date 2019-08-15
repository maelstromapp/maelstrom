package gateway

import (
	"bytes"
	"context"
	"fmt"
	"github.com/mgutz/logxi/v1"
	"github.com/robfig/cron"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"time"
)

func NewCronService(db v1.Db, gateway *Gateway, ctx context.Context, refreshRate time.Duration) *CronService {
	return &CronService{
		db:          db,
		gateway:     gateway,
		ctx:         ctx,
		refreshRate: refreshRate,
	}
}

type CronService struct {
	db           v1.Db
	gateway      *Gateway
	ctx          context.Context
	refreshRate  time.Duration
	cron         *cron.Cron
	eventSources []v1.EventSource
}

func (c *CronService) Run(wg *sync.WaitGroup) {
	defer wg.Done()
	log.Info("cron: starting cron service", "refreshRate", c.refreshRate.String())
	c.reloadRulesAndStartCron()
	for {
		reload := time.After(c.refreshRate)
		select {
		case <-c.ctx.Done():
			if c.cron != nil {
				c.cron.Stop()
			}
			log.Info("cron: stop received. exiting gracefully.")
			return

		case <-reload:
			c.reloadRulesAndStartCron()
		}
	}
}

func (c *CronService) createCronInvoker(es v1.EventSource) func() {
	url := fmt.Sprintf("http://127.0.0.1%s", es.Cron.Http.Path)
	includeBody := strings.ToLower(es.Cron.Http.Method) != "get"
	return func() {
		var body io.Reader
		if includeBody && es.Cron.Http.Data != "" {
			body = bytes.NewBufferString(es.Cron.Http.Data)
		}
		log.Info("cron: invoking component", "name", es.Name, "component", es.ComponentName, "url", url)
		rw := httptest.NewRecorder()
		req, err := http.NewRequest(es.Cron.Http.Method, url, body)
		if err == nil {
			for _, nv := range es.Cron.Http.Headers {
				req.Header.Add(nv.Name, nv.Value)
			}
			req.Header.Set("Maelstrom-Component", es.ComponentName)
			c.gateway.ServeHTTP(rw, req)
			if rw.Code < 200 || rw.Code > 299 {
				log.Error("cron: invoke returned non 2xx status", "name", es.Name, "component", es.ComponentName)
			}
		} else {
			log.Error("cron: http.NewRequest failed", "err", err, "name", es.Name, "component", es.ComponentName)
		}
	}
}

func (c *CronService) reloadRulesAndStartCron() {
	eventSources, err := c.loadAllCronEventSources()
	if err == nil {
		if c.cron == nil || c.eventSources == nil || !reflect.DeepEqual(eventSources, c.eventSources) {
			var newCron *cron.Cron
			if len(eventSources) > 0 {
				log.Info("cron: creating new cron scheduler", "eventSourceCount", len(eventSources))
				newCron = cron.New()
				for _, es := range eventSources {
					if es.Cron != nil {
						err = newCron.AddFunc(es.Cron.Schedule, c.createCronInvoker(es))
						if err != nil {
							log.Error("cron: error adding cron", "err", err, "schedule", es.Cron.Schedule)
						}
					}
				}
			}

			if c.cron != nil {
				log.Info("cron: stopping old cron scheduler")
				c.cron.Stop()
			}
			if newCron != nil {
				newCron.Start()
			}
			c.cron = newCron
			c.eventSources = eventSources
		}
	} else {
		log.Error("cron: error loading event sources", "err", err)
	}
}

func (c *CronService) loadAllCronEventSources() ([]v1.EventSource, error) {
	eventSources := make([]v1.EventSource, 0)
	nextToken := ""
	for {
		out, err := c.db.ListEventSources(v1.ListEventSourcesInput{
			EventSourceType: v1.EventSourceTypeCron,
			Limit:           1000,
			NextToken:       nextToken,
		})
		if err != nil {
			return nil, err
		}
		if len(out.EventSources) > 0 {
			eventSources = append(eventSources, out.EventSources...)
		}
		if out.NextToken == "" {
			return eventSources, nil
		}
		nextToken = out.NextToken
	}
}
