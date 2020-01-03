package evsqs

import (
	"bytes"
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/sqs"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/evsource"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"github.com/pkg/errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"time"
)

type sqsMessage struct {
	msg      *sqs.Message
	queueUrl *string
}

func NewPollCreator(es v1.EventSource, awsSession *session.Session, gateway http.Handler) (evsource.PollCreator, error) {
	es = setDefaults(es)
	sqsClient, queueUrls, err := sqsQueueUrlsForPrefix(awsSession, es.Sqs.QueueName, es.Sqs.NameAsPrefix)
	if err != nil {
		return nil, errors.Wrapf(err, "evsqs: error loading queue urls for: %s", es.Sqs.QueueName)
	} else if len(queueUrls) <= 0 {
		if es.Sqs.NameAsPrefix {
			log.Warn("evpoller: No SQS queues found with name prefix", "queueNamePrefix", es.Sqs.QueueName)
		} else {
			log.Warn("evpoller: SQS queue not found", "queueName", es.Sqs.QueueName)
		}
		return nil, nil
	} else {
		return &SqsPollCreator{
			es:        es,
			sqsClient: sqsClient,
			gateway:   gateway,
			queueUrls: queueUrls,
		}, nil
	}
}

type SqsPollCreator struct {
	es        v1.EventSource
	sqsClient *sqs.SQS
	gateway   http.Handler
	queueUrls []*string
}

func (s *SqsPollCreator) NewPoller() evsource.Poller {
	return newPoller(s.es, s.queueUrls, s.sqsClient, s.gateway)
}

func (s *SqsPollCreator) ComponentName() string {
	return s.es.ComponentName
}

func (s *SqsPollCreator) RoleIdPrefix() string {
	return fmt.Sprintf("aws-sqs-%s", s.es.Name)
}

func (s *SqsPollCreator) MaxConcurrency() int {
	return int(s.es.Sqs.MaxConcurrency)
}

func (s *SqsPollCreator) MaxConcurrencyPerPoller() int {
	return int(s.es.Sqs.ConcurrencyPerPoller)
}

func newPoller(es v1.EventSource, queueUrls []*string, sqsClient *sqs.SQS, gateway http.Handler) evsource.Poller {
	return func(ctx context.Context, parentWg *sync.WaitGroup, concurrency int, roleId string) {
		defer parentWg.Done()

		resetIdxTicker := time.Tick(15 * time.Second)
		idx := 0

		log.Info("sqs: poller starting", "roleId", roleId, "concurrency", concurrency,
			"component", es.ComponentName)

		reqCh := make(chan *sqsMessage)
		wg := &sync.WaitGroup{}
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for m := range reqCh {
					req, err := http.NewRequest("POST", es.Sqs.Path, bytes.NewBufferString(*m.msg.Body))
					if err != nil {
						log.Error("sqs: error creating http req", "err", err, "queueUrl", m.queueUrl)
					} else {
						rw := httptest.NewRecorder()
						req.Header.Set("Maelstrom-Component", es.ComponentName)
						gateway.ServeHTTP(rw, req)
						if rw.Code == 200 {
							if log.IsDebug() {
								log.Debug("sqs: deleting message", "queueUrl", m.queueUrl)
							}
							_, err := sqsClient.DeleteMessage(&sqs.DeleteMessageInput{
								QueueUrl:      m.queueUrl,
								ReceiptHandle: m.msg.ReceiptHandle,
							})
							if err != nil {
								log.Error("sqs: error deleting message", "err", err, "queueUrl", m.queueUrl)
							}
						} else {
							log.Warn("sqs: non-200 status", "component", es.ComponentName, "queueUrl", m.queueUrl)
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
			case <-ctx.Done():
				close(reqCh)
				wg.Wait()
				return
			default:
				queueUrl := queueUrls[idx]
				msgs, err := poll(es, sqsClient, queueUrl)
				if err != nil {
					log.Error("sqs: poll err", "err", err, "component", es.ComponentName, "queueUrl", *queueUrl)
				} else {
					if len(msgs) == 0 {
						idx++
						if idx >= len(queueUrls) {
							idx = 0
						}
					} else {
						for _, msg := range msgs {
							reqCh <- &sqsMessage{
								msg:      msg,
								queueUrl: queueUrl,
							}
						}
					}
				}
			}
		}
	}
}

func poll(es v1.EventSource, sqsClient *sqs.SQS, queueUrl *string) ([]*sqs.Message, error) {
	if log.IsDebug() {
		log.Debug("sqs: polling queue", "queueUrl", *queueUrl)
	}
	maxMsgs := es.Sqs.MessagesPerPoll
	if maxMsgs < 1 || maxMsgs > 10 {
		maxMsgs = 1
	}
	visibilityTimeout := es.Sqs.VisibilityTimeout
	if visibilityTimeout <= 0 {
		visibilityTimeout = 300
	}
	out, err := sqsClient.ReceiveMessage(&sqs.ReceiveMessageInput{
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

func sqsQueueUrlsForPrefix(awsSession *session.Session,
	queueNameOrPrefix string, prefix bool) (*sqs.SQS, []*string, error) {
	if awsSession == nil {
		return nil, nil, fmt.Errorf("evpoller: cannot create sqs client - awsSession is nil")
	}

	sqsClient := sqs.New(awsSession)
	queueUrls := []*string{}
	if prefix {
		out, err := sqsClient.ListQueues(&sqs.ListQueuesInput{QueueNamePrefix: &queueNameOrPrefix})
		if err != nil {
			return nil, nil, err
		}
		sort.Sort(common.StringPtr(out.QueueUrls))
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

func setDefaults(es v1.EventSource) v1.EventSource {
	es.Sqs.MaxConcurrency = common.DefaultInt64(es.Sqs.MaxConcurrency, 10)
	es.Sqs.VisibilityTimeout = common.DefaultInt64(es.Sqs.VisibilityTimeout, 300)
	es.Sqs.MessagesPerPoll = common.DefaultInt64(es.Sqs.MessagesPerPoll, 1)
	es.Sqs.ConcurrencyPerPoller = common.DefaultInt64(es.Sqs.ConcurrencyPerPoller, es.Sqs.MessagesPerPoll)
	return es
}
