package v1

import (
	"fmt"
)

func StrToEventSourceType(esType string) (EventSourceType, error) {
	es := EventSourceType(esType)
	all := []EventSourceType{EventSourceTypeSqs, EventSourceTypeCron, EventSourceTypeHttp,
		EventSourceTypeAwsstepfunc}
	for _, t := range all {
		if t == es {
			return t, nil
		}
	}
	return "", fmt.Errorf("invalid EventSourceType: %s", esType)
}

func GetEventSourceType(e EventSource) EventSourceType {
	if e.Http != nil {
		return EventSourceTypeHttp
	} else if e.Cron != nil {
		return EventSourceTypeCron
	} else if e.Sqs != nil {
		return EventSourceTypeSqs
	} else if e.Awsstepfunc != nil {
		return EventSourceTypeAwsstepfunc
	} else {
		panic("Unknown eventType for EventSource")
	}
}
