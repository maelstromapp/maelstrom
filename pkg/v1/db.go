package v1

import (
	"fmt"
)

var NotFound = fmt.Errorf("Not Found")
var AlreadyExists = fmt.Errorf("Entity already exists")
var IncorrectPreviousVersion = fmt.Errorf("Incorrect PreviousVersion")

type Db interface {
	Migrate() error

	PutComponent(component Component) (int64, error)
	GetComponent(componentName string) (Component, error)
	ListComponents(input ListComponentsInput) (ListComponentsOutput, error)
	RemoveComponent(componentName string) (bool, error)

	PutEventSource(eventSource EventSource) (int64, error)
	GetEventSource(eventSourceName string) (EventSource, error)
	ListEventSources(input ListEventSourcesInput) (ListEventSourcesOutput, error)
	RemoveEventSource(eventSourceName string) (bool, error)
}
