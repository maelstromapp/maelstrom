package test

import v1 "github.com/coopernurse/maelstrom/pkg/v1"

func ValidPutEventSourceInput(eventSourceName string, componentName string) v1.PutEventSourceInput {
	return v1.PutEventSourceInput{
		EventSource: v1.EventSource{
			Name:          eventSourceName,
			ComponentName: componentName,
			Http: &v1.HttpEventSource{
				Hostname: "www.example.com",
			},
		},
	}
}

func SanitizeEventSources(list []v1.EventSourceWithStatus) {
	for i, es := range list {
		list[i] = SanitizeEventSource(&es)
	}
}

func SanitizeEventSource(es *v1.EventSourceWithStatus) v1.EventSourceWithStatus {
	es.EventSource.Version = 0
	es.EventSource.ModifiedAt = 0
	return *es
}
