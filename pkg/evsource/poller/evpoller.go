package poller

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/evsource"
	evsqs "github.com/coopernurse/maelstrom/pkg/evsource/aws/sqs"
	evstepfunc "github.com/coopernurse/maelstrom/pkg/evsource/aws/stepfunc"
	"github.com/coopernurse/maelstrom/pkg/router"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"math"
	"net/http"
	"sync"
	"time"
)

func NewEvPoller(myNodeId string, ctx context.Context, db db.Db, gateway http.Handler, routerReg *router.Registry,
	awsSession *session.Session) *EvPoller {
	return &EvPoller{
		myNodeId:    myNodeId,
		ctx:         ctx,
		db:          db,
		gateway:     gateway,
		routerReg:   routerReg,
		awsSession:  awsSession,
		activeRoles: make(map[string]context.CancelFunc),
		pollerDone:  make(chan string),
		pollerWg:    &sync.WaitGroup{},
	}
}

type EvPoller struct {
	myNodeId    string
	ctx         context.Context
	db          db.Db
	gateway     http.Handler
	routerReg   *router.Registry
	awsSession  *session.Session
	activeRoles map[string]context.CancelFunc
	pollerDone  chan string
	pollerWg    *sync.WaitGroup
}

func (e *EvPoller) Run(daemonWG *sync.WaitGroup) {
	defer daemonWG.Done()
	e.reload()
	ticker := time.Tick(time.Minute)
	for {
		select {
		case roleId := <-e.pollerDone:
			delete(e.activeRoles, roleId)
			log.Info("evpoller: removed active role", "roleId", roleId)
		case <-ticker:
			e.reload()
		case <-e.ctx.Done():
			log.Info("evpoller: shutting down pollers")
			for _, cancelFx := range e.activeRoles {
				cancelFx()
			}
			e.pollerWg.Wait()
			log.Info("evpoller: shutdown gracefully")
			return
		}
	}
}

func (e *EvPoller) reload() {
	nextToken := ""
	input := v1.ListEventSourcesInput{}
	validRoleIds := map[string]bool{}
	running := true
	for running {
		input.NextToken = nextToken
		output, err := e.db.ListEventSources(input)
		if err != nil {
			log.Error("evpoller: ListEventSources error", "err", err)
			return
		}

		// init pollers for event sources found
		for _, ess := range output.EventSources {
			if ess.Enabled {
				var pollCreator evsource.PollCreator
				var err error
				es := ess.EventSource
				if es.Sqs != nil && es.Sqs.QueueName != "" {
					pollCreator, err = evsqs.NewPollCreator(es, e.awsSession, e.gateway)
				} else if es.Awsstepfunc != nil && es.Awsstepfunc.ActivityName != "" {
					pollCreator, err = evstepfunc.NewPollCreator(es, e.awsSession, e.gateway)
				}
				if err != nil {
					log.Error("evpoller: create poller error", "err", err)
				}
				if pollCreator != nil {
					e.startPollerGroup(pollCreator, validRoleIds)
				}
			}
		}

		nextToken = output.NextToken
		running = nextToken != ""
	}

	// cancel any pollers that are no longer valid roles
	for roleId, cancelFx := range e.activeRoles {
		_, ok := validRoleIds[roleId]
		if !ok {
			go cancelFx()
		}
	}
}

func (e *EvPoller) startPollerGroup(pollCreator evsource.PollCreator, validRoleIds map[string]bool) {
	componentName := pollCreator.ComponentName()
	activeHandlers := e.routerReg.ByComponent(componentName).GetHandlerCount()

	maxConcurrency := toMaxConcurrency(pollCreator.MaxConcurrency(), int(activeHandlers))
	roleIdConcurs := toRoleIdConcurrency(pollCreator, maxConcurrency)

	for _, rc := range roleIdConcurs {
		roleId := rc.roleId
		validRoleIds[roleId] = true
		ok, _, err := e.db.AcquireOrRenewRole(roleId, e.myNodeId, 2*time.Minute)
		pollerOk := false
		if err != nil {
			log.Error("evpoller: AcquireOrRenewRole error", "err", err, "roleId", roleId)
		} else if ok {
			// acquired lock - start poller
			cancelFx := e.activeRoles[roleId]
			if cancelFx == nil {
				poller := pollCreator.NewPoller()
				ctx, cancelFunc := context.WithCancel(e.ctx)
				e.activeRoles[roleId] = cancelFunc
				e.pollerWg.Add(1)
				go poller(ctx, e.pollerWg, rc.concurrency, roleId)
				pollerOk = true
			} else {
				pollerOk = true
			}
		}

		if !pollerOk {
			// lost lock or no queues defined - cancel poller
			cancelFx := e.activeRoles[roleId]
			if cancelFx != nil {
				go cancelFx()
			}
		}
	}
}

func toRoleIdConcurrency(pollCreator evsource.PollCreator, maxConcurrency int) []roleIdConcurrency {
	roleIdConcur := make([]roleIdConcurrency, 0)

	num := int(math.Round(math.Ceil(float64(maxConcurrency) / float64(pollCreator.MaxConcurrencyPerPoller()))))
	concurRemain := pollCreator.MaxConcurrencyPerPoller() * num
	if concurRemain > maxConcurrency {
		concurRemain = maxConcurrency
	}
	for i := 0; i < num; i++ {
		c := pollCreator.MaxConcurrencyPerPoller()
		if c > concurRemain {
			c = concurRemain
		}
		concurRemain -= c
		if c > 0 {
			roleIdConcur = append(roleIdConcur, roleIdConcurrency{
				// store the concurrency value on the roleId
				// if maxConcurrency changes (due to event source modification or change in component instances)
				// then that will invalidate the roleId and we'll start a new one and turn off the old one
				roleId:      fmt.Sprintf("%s-%d-%d", pollCreator.RoleIdPrefix(), i, c),
				concurrency: c,
			})
		}
	}
	return roleIdConcur
}

func toMaxConcurrency(pollerMaxConcurrency int, activeHandlers int) int {
	if pollerMaxConcurrency <= 0 {
		pollerMaxConcurrency = 1
	}
	if activeHandlers <= 0 {
		activeHandlers = 1
	}
	maxConcur := activeHandlers
	if activeHandlers > pollerMaxConcurrency {
		maxConcur = pollerMaxConcurrency
	}
	return maxConcur
}

type roleIdConcurrency struct {
	roleId      string
	concurrency int
}
