package maelstrom

import (
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/v1"
	"time"
)

var NotFound = fmt.Errorf("Not Found")
var AlreadyExists = fmt.Errorf("Entity already exists")
var IncorrectPreviousVersion = fmt.Errorf("Incorrect PreviousVersion")

type Db interface {
	Migrate() error

	AcquireOrRenewRole(roleId string, nodeId string, lockDur time.Duration) (bool, string, error)
	ReleaseRole(roleId string, nodeId string) error
	ReleaseAllRoles(nodeId string) error

	ListProjects(input v1.ListProjectsInput) (v1.ListProjectsOutput, error)

	PutComponent(component v1.Component) (int64, error)
	GetComponent(componentName string) (v1.Component, error)
	ListComponents(input v1.ListComponentsInput) (v1.ListComponentsOutput, error)
	RemoveComponent(componentName string) (bool, error)

	GetComponentDeployCount(componentName string, version int64) (int, error)
	IncrementComponentDeployCount(componentName string, version int64) error

	PutEventSource(eventSource v1.EventSource) (int64, error)
	GetEventSource(eventSourceName string) (v1.EventSource, error)
	ListEventSources(input v1.ListEventSourcesInput) (v1.ListEventSourcesOutput, error)
	RemoveEventSource(eventSourceName string) (bool, error)
	SetEventSourcesEnabled(eventSourceNames []string, enabled bool) (int64, error)

	PutNodeStatus(status v1.NodeStatus) error
	ListNodeStatus() ([]v1.NodeStatus, error)
	RemoveNodeStatusOlderThan(observedAt time.Time) (int64, error)
	RemoveNodeStatus(nodeId string) (bool, error)
}
