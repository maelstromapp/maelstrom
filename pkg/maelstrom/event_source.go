package maelstrom

import (
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/v1"
)

func StrToEventSourceType(esType string) (v1.EventSourceType, error) {
	es := v1.EventSourceType(esType)
	all := []v1.EventSourceType{v1.EventSourceTypeSqs, v1.EventSourceTypeCron, v1.EventSourceTypeHttp}
	for _, t := range all {
		if t == es {
			return t, nil
		}
	}
	return "", fmt.Errorf("invalid EventSourceType: %s", esType)
}
func getEventSourceType(e v1.EventSource) v1.EventSourceType {
	if e.Http != nil {
		return v1.EventSourceTypeHttp
	} else if e.Cron != nil {
		return v1.EventSourceTypeCron
	} else if e.Sqs != nil {
		return v1.EventSourceTypeSqs
	} else {
		panic("Unknown eventType for EventSource")
	}
}
