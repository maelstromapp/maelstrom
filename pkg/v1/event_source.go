package v1

func getEventSourceType(e EventSource) EventSourceType {
	if e.Http != nil {
		return EventSourceTypeHttp
	} else if e.Cron != nil {
		return EventSourceTypeCron
	} else if e.Sqs != nil {
		return EventSourceTypeSqs
	} else {
		panic("Unknown eventType for EventSource")
	}
}
