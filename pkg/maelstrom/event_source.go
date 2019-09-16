package maelstrom

import "gitlab.com/coopernurse/maelstrom/pkg/v1"

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
