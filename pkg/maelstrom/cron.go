package maelstrom

import (
	"bytes"
	"context"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/mgutz/logxi/v1"
	"github.com/robfig/cron/v3"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"time"
)

func NewCronService(db db.Db, gateway *Gateway, ctx context.Context, nodeId string, refreshRate time.Duration) *CronService {
	return &CronService{
		db:           db,
		gateway:      gateway,
		ctx:          ctx,
		nodeId:       nodeId,
		refreshRate:  refreshRate,
		acquiredRole: false,
	}
}

type CronService struct {
	db           db.Db
	gateway      *Gateway
	ctx          context.Context
	nodeId       string
	acquiredRole bool
	refreshRate  time.Duration
	cron         *cron.Cron
	eventSources []v1.EventSourceWithStatus
}

func (c *CronService) Run(wg *sync.WaitGroup, withSeconds bool) {
	defer wg.Done()
	log.Info("cron: starting cron service", "refreshRate", c.refreshRate.String())
	lockTicker := time.Tick(15 * time.Second)
	reloadTicker := time.Tick(c.refreshRate)
	c.reloadRulesAndStartCron(c.acquireRoleOrStop(), withSeconds)
	for {
		select {
		case <-c.ctx.Done():
			if c.cron != nil {
				c.cron.Stop()
			}
			log.Info("cron: shutdown gracefully")
			return

		case <-lockTicker:
			c.acquireRoleOrStop()

		case <-reloadTicker:
			c.reloadRulesAndStartCron(c.acquireRoleOrStop(), withSeconds)
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
				log.Warn("cron: invoke returned non 2xx status", "name", es.Name, "component", es.ComponentName,
					"status", rw.Code)
			}
		} else {
			log.Error("cron: http.NewRequest failed", "err", err, "name", es.Name, "component", es.ComponentName)
		}
	}
}

func (c *CronService) acquireRoleOrStop() bool {
	previous := c.acquiredRole
	c.acquiredRole = false
	roleOk, roleNode, err := c.db.AcquireOrRenewRole(roleCron, c.nodeId, time.Minute)
	if err == nil {
		c.acquiredRole = roleOk

		if previous && !roleOk {
			log.Info("cron: lost role lock", "node", c.nodeId, "newCronNode", roleNode)
		} else if !previous && roleOk {
			log.Info("cron: acquired role lock, starting cron")
		}

		if !roleOk {
			c.stopCron()
		}
	} else {
		log.Error("cron: db.AcquireOrRenewRole error", "err", err, "node", c.nodeId)
	}
	return c.acquiredRole
}

func (c *CronService) stopCron() {
	if c.cron != nil {
		log.Info("cron: stopping old cron scheduler")
		c.cron.Stop()
		c.cron = nil
	}
}

func (c *CronService) reloadRulesAndStartCron(hasRoleLock bool, withSeconds bool) {
	if !hasRoleLock {
		return
	}

	eventSources, err := c.loadAllCronEventSources()
	if err == nil {
		if c.cron == nil || c.eventSources == nil || !reflect.DeepEqual(eventSources, c.eventSources) {
			var newCron *cron.Cron
			if len(eventSources) > 0 {
				log.Info("cron: creating new cron scheduler", "eventSourceCount", len(eventSources))
				if withSeconds {
					newCron = cron.New(cron.WithSeconds())
				} else {
					newCron = cron.New()
				}
				for _, ess := range eventSources {
					es := ess.EventSource
					if es.Cron != nil && ess.Enabled {
						_, err = newCron.AddFunc(es.Cron.Schedule, c.createCronInvoker(es))
						if err != nil {
							log.Error("cron: error adding cron", "err", err, "schedule", es.Cron.Schedule)
						}
					}
				}
			}

			c.stopCron()

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

func (c *CronService) loadAllCronEventSources() ([]v1.EventSourceWithStatus, error) {
	eventSources := make([]v1.EventSourceWithStatus, 0)
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
