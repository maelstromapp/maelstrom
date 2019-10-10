package maelstrom

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/coopernurse/maelstrom/pkg/maelstrom/component"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"math"
	"sort"
	"sync"
	"time"
)

func NewEvPoller(myNodeId string, ctx context.Context, db Db, dispatcher *component.Dispatcher,
	awsSession *session.Session) *EvPoller {
	return &EvPoller{
		myNodeId:    myNodeId,
		ctx:         ctx,
		db:          db,
		dispatcher:  dispatcher,
		awsSession:  awsSession,
		activeRoles: make(map[string]context.CancelFunc),
		pollerDone:  make(chan string),
		pollerWg:    &sync.WaitGroup{},
	}
}

type EvPoller struct {
	myNodeId    string
	ctx         context.Context
	db          Db
	dispatcher  *component.Dispatcher
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

func (e *EvPoller) sqsQueueUrlsForPrefix(queueNameOrPrefix string, prefix bool) (*sqs.SQS, []*string, error) {
	if e.awsSession == nil {
		return nil, nil, fmt.Errorf("evpoller: cannot create sqs client - awsSession is nil")
	}

	sqsClient := sqs.New(e.awsSession)
	queueUrls := []*string{}
	if prefix {
		out, err := sqsClient.ListQueues(&sqs.ListQueuesInput{QueueNamePrefix: &queueNameOrPrefix})
		if err != nil {
			return nil, nil, err
		}
		sort.Sort(StringPtr(out.QueueUrls))
		queueUrls = out.QueueUrls
	} else {
		out, err := sqsClient.GetQueueUrl(&sqs.GetQueueUrlInput{QueueName: &queueNameOrPrefix})
		if err != nil {
			returnErr := true
			if aerr, ok := err.(awserr.Error); ok {
				switch aerr.Code() {
				case sqs.ErrCodeQueueDoesNotExist:
					returnErr = false
				}
			}
			if returnErr {
				return nil, nil, err
			}
		} else {
			queueUrls = []*string{out.QueueUrl}
		}
	}
	return sqsClient, queueUrls, nil
}

func (e *EvPoller) initSqsEventSource(es v1.EventSource, validRoleIds map[string]bool) {
	comp, err := e.db.GetComponent(es.ComponentName)
	if err != nil {
		log.Error("evpoller: Unable to load component", "component", es.ComponentName, "err", err)
		return
	}
	instancesRunning := e.dispatcher.InstanceCountForComponent(comp)
	roleIdConcurs := sqsRoleIdConcurrency(es, int(comp.MaxConcurrency), instancesRunning)
	for _, rc := range roleIdConcurs {
		roleId := rc.roleId
		validRoleIds[roleId] = true
		ok, _, err := e.db.AcquireOrRenewRole(roleId, e.myNodeId, 2*time.Minute)
		pollerOk := false
		if err != nil {
			log.Error("evpoller: AcquireOrRenewRole error", "err", err, "roleId", roleId)
		} else if ok {
			// acquired lock - start sqs poller
			cancelFx := e.activeRoles[roleId]
			if cancelFx == nil {
				sqsClient, queueUrls, err := e.sqsQueueUrlsForPrefix(es.Sqs.QueueName, es.Sqs.NameAsPrefix)
				if err != nil {
					log.Error("evpoller: error loading queue urls", "err", err, "roleId", roleId,
						"queueName", es.Sqs.QueueName)
				} else if len(queueUrls) > 0 {
					ctx, cancelFunc := context.WithCancel(e.ctx)
					e.activeRoles[roleId] = cancelFunc
					sqsPoller := NewSqsPoller(roleId, e.db, e.dispatcher, es, sqsClient, queueUrls,
						ctx, e.pollerWg)
					e.pollerWg.Add(1)
					go sqsPoller.Run(rc.concurrency)
					pollerOk = true
				} else {
					if es.Sqs.NameAsPrefix {
						log.Warn("evpoller: No SQS queues found with name prefix", "queueNamePrefix", es.Sqs.QueueName)
					} else {
						log.Warn("evpoller: SQS queue not found", "queueName", es.Sqs.QueueName)
					}
				}
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
		for _, es := range output.EventSources {
			if es.Sqs != nil && es.Sqs.QueueName != "" {
				e.initSqsEventSource(setSqsDefaults(es), validRoleIds)
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

func defaultInt64(v int64, defaultVal int64) int64 {
	if v == 0 {
		return defaultVal
	}
	return v
}

func setSqsDefaults(es v1.EventSource) v1.EventSource {
	es.Sqs.MaxConcurrency = defaultInt64(es.Sqs.MaxConcurrency, 10)
	es.Sqs.VisibilityTimeout = defaultInt64(es.Sqs.VisibilityTimeout, 300)
	es.Sqs.MessagesPerPoll = defaultInt64(es.Sqs.MessagesPerPoll, 1)
	es.Sqs.ConcurrencyPerPoller = defaultInt64(es.Sqs.ConcurrencyPerPoller, es.Sqs.MessagesPerPoll)
	return es
}

func sqsRoleIdConcurrency(es v1.EventSource, maxConcurPerInst int, instancesRunning int) []roleIdConcurrency {
	roleIdConcur := make([]roleIdConcurrency, 0)
	if maxConcurPerInst <= 0 {
		maxConcurPerInst = 1
	}
	if instancesRunning <= 0 {
		instancesRunning = 1
	}
	maxConcur := instancesRunning * maxConcurPerInst
	if maxConcur > int(es.Sqs.MaxConcurrency) {
		maxConcur = int(es.Sqs.MaxConcurrency)
	}
	num := int(math.Round(math.Ceil(float64(maxConcur) / float64(es.Sqs.ConcurrencyPerPoller))))
	concurRemain := int(es.Sqs.ConcurrencyPerPoller) * num
	if concurRemain > maxConcur {
		concurRemain = maxConcur
	}
	for i := 0; i < num; i++ {
		c := int(es.Sqs.ConcurrencyPerPoller)
		if c > concurRemain {
			c = concurRemain
		}
		concurRemain -= c
		roleIdConcur = append(roleIdConcur, roleIdConcurrency{
			roleId:      fmt.Sprintf("aws-sqs-%s-%d", es.Name, i),
			concurrency: c,
		})
	}
	return roleIdConcur
}

type roleIdConcurrency struct {
	roleId      string
	concurrency int
}
