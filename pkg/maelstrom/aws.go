package maelstrom

import (
	"encoding/json"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	log "github.com/mgutz/logxi/v1"
	"net/http"
	"time"
)

type AwsSpotInstanceAction struct {
	Action string
	Time   string
}

func awsSpotInstanceTerminate() bool {
	// See docs:
	// https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/spot-interruptions.html
	//
	resp, err := http.Get("http://169.254.169.254/latest/meta-data/spot/instance-action")
	if err != nil {
		return false
	}
	defer common.CheckClose(resp.Body, &err)

	if resp.StatusCode == http.StatusOK {
		var inst AwsSpotInstanceAction
		err = json.NewDecoder(resp.Body).Decode(&inst)
		if err == nil {
			return inst.Action == "stop" || inst.Action == "terminate"
		}
	}
	return false
}

type AwsLifecycleHookMessage struct {
	QueueUrl             string
	MessageReceiptHandle string
	AccountId            string
	RequestId            string
	Time                 string
	Service              string
	AutoScalingGroupName string
	EC2InstanceId        string
	LifecycleActionToken string
	LifecycleHookName    string
}

func (h *AwsLifecycleHookMessage) ToAwsLifecycleHook() *v1.AwsLifecycleHook {
	return &v1.AwsLifecycleHook{
		QueueUrl:             h.QueueUrl,
		MessageReceiptHandle: h.MessageReceiptHandle,
		AutoScalingGroupName: h.AutoScalingGroupName,
		InstanceId:           h.EC2InstanceId,
		LifecycleActionToken: h.LifecycleActionToken,
		LifecycleHookName:    h.LifecycleHookName,
	}
}

func (h *AwsLifecycleHookMessage) TryParseAge() *time.Duration {
	t, err := time.Parse("2006-01-02T15:04:05.000Z", h.Time)
	if err == nil {
		dur := time.Now().Sub(t)
		return &dur
	} else {
		log.Warn("aws: unable to parse lifecycle hook time: " + h.Time)
		return nil
	}
}
