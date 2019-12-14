package maelstrom

import (
	"bytes"
	"context"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/maelstrom/component"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"
)

type sqsMessage struct {
	msg       *sqs.Message
	queueUrl  *string
	component *v1.Component
}

func NewSqsPoller(roleId string, db db.Db, dispatcher *component.Dispatcher, es v1.EventSource, sqsClient *sqs.SQS,
	queueUrls []*string, ctx context.Context, parentWg *sync.WaitGroup) *SqsPoller {
	return &SqsPoller{
		roleId:     roleId,
		sqs:        sqsClient,
		db:         db,
		dispatcher: dispatcher,
		es:         es,
		queueUrls:  queueUrls,
		ctx:        ctx,
		parentWg:   parentWg,
		doneCh:     make(chan string, 1),
	}
}

type SqsPoller struct {
	roleId     string
	sqs        *sqs.SQS
	db         db.Db
	dispatcher *component.Dispatcher
	es         v1.EventSource
	queueUrls  []*string
	ctx        context.Context
	parentWg   *sync.WaitGroup
	doneCh     chan string
}

func (s *SqsPoller) Run(concurrency int) {
	defer s.done()

	reloadCompTicker := time.Tick(time.Minute)
	resetIdxTicker := time.Tick(15 * time.Second)
	idx := 0

	comp, err := s.db.GetComponent(s.es.ComponentName)
	if err != nil {
		log.Error("sqs: startup error in db.GetComponent", "err", err, "component", s.es.ComponentName)
		return
	}

	log.Info("sqs: poller starting", "roleId", s.roleId, "concurrency", concurrency,
		"component", s.es.ComponentName)

	reqCh := make(chan *sqsMessage)
	wg := &sync.WaitGroup{}
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range reqCh {
				req, err := http.NewRequest("POST", s.es.Sqs.Path, bytes.NewBufferString(*m.msg.Body))
				if err != nil {
					log.Error("sqs: error creating http req", "err", err, "queueUrl", m.queueUrl)
				} else {
					rw := httptest.NewRecorder()
					s.dispatcher.Route(rw, req, m.component, false)
					if rw.Code == 200 {
						if log.IsDebug() {
							log.Debug("sqs: deleting message", "queueUrl", m.queueUrl)
						}
						_, err := s.sqs.DeleteMessage(&sqs.DeleteMessageInput{
							QueueUrl:      m.queueUrl,
							ReceiptHandle: m.msg.ReceiptHandle,
						})
						if err != nil {
							log.Error("sqs: error deleting message", "err", err, "queueUrl", m.queueUrl)
						}
					} else {
						log.Warn("sqs: non-200 status", "component", m.component.Name, "queueUrl", m.queueUrl)
					}
				}
			}
		}()
	}

	for {
		select {
		case <-resetIdxTicker:
			// start polling from the front of the queue list again
			idx = 0
		case <-reloadCompTicker:
			c, err := s.db.GetComponent(s.es.ComponentName)
			if err != nil {
				log.Error("sqs: reload error in db.GetComponent", "err", err, "component", s.es.ComponentName)
			} else {
				comp = c
			}
		case <-s.ctx.Done():
			close(reqCh)
			wg.Wait()
			return
		default:
			queueUrl := s.queueUrls[idx]
			msgs, err := s.poll(queueUrl)
			if err != nil {
				log.Error("sqs: poll err", "err", err, "component", s.es.ComponentName, "queueUrl", *queueUrl)
			} else {
				if len(msgs) == 0 {
					idx++
					if idx >= len(s.queueUrls) {
						idx = 0
					}
				} else {
					for _, msg := range msgs {
						reqCh <- &sqsMessage{
							msg:       msg,
							queueUrl:  queueUrl,
							component: &comp,
						}
					}
				}
			}
		}
	}
}

func (s *SqsPoller) done() {
	s.doneCh <- s.roleId
	s.parentWg.Done()
	log.Info("sqs: poller exiting", "roleId", s.roleId)
}

func (s *SqsPoller) poll(queueUrl *string) ([]*sqs.Message, error) {
	if log.IsDebug() {
		log.Debug("sqs: polling queue", "queueUrl", *queueUrl)
	}
	maxMsgs := s.es.Sqs.MessagesPerPoll
	if maxMsgs < 1 || maxMsgs > 10 {
		maxMsgs = 1
	}
	visibilityTimeout := s.es.Sqs.VisibilityTimeout
	if visibilityTimeout <= 0 {
		visibilityTimeout = 300
	}
	out, err := s.sqs.ReceiveMessage(&sqs.ReceiveMessageInput{
		QueueUrl:            queueUrl,
		MaxNumberOfMessages: aws.Int64(maxMsgs),
		VisibilityTimeout:   aws.Int64(visibilityTimeout),
		WaitTimeSeconds:     aws.Int64(1),
	})
	if err == nil {
		return out.Messages, nil
	}
	return nil, err
}
