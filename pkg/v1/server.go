package v1

import (
	"fmt"
	"github.com/coopernurse/barrister-go"
	"github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/cert"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"regexp"
	"strings"
)

var _ MaelstromService = (*V1)(nil)
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

type ErrorCode int

const (
	MiscError ErrorCode = -32000
	DbError             = -32001
)

func newRpcErr(code int, msg string) error {
	return &barrister.JsonRpcError{Code: code, Message: msg}
}

func nameValid(errPrefix string, name string, maxLen int) (string, error) {
	name = strings.TrimSpace(name)
	invalidNameMsg := ""
	if name == "" {
		invalidNameMsg = errPrefix + " is required"
	} else if len(name) > maxLen {
		invalidNameMsg = fmt.Sprintf("%s exceeds max length of %d bytes", errPrefix, maxLen)
	} else if !nameRE.MatchString(name) {
		invalidNameMsg = errPrefix + " is invalid (only alpha-numeric chars, _ - are valid)"
	}

	if invalidNameMsg != "" {
		return "", newRpcErr(1001, invalidNameMsg)
	}
	return name, nil
}

func componentNameValid(errPrefix string, name string) (string, error) {
	return nameValid(errPrefix, name, 60)
}

func eventSourceNameValid(errPrefix string, name string) (string, error) {
	return nameValid(errPrefix, name, 60)
}

func projectNameValid(errPrefix string, name string) (string, error) {
	return nameValid(errPrefix, name, 20)
}

func NewV1(db Db, componentSubscribers []ComponentSubscriber, certWrapper *cert.CertMagicWrapper) *V1 {
	return &V1{
		db:                   db,
		componentSubscribers: componentSubscribers,
		certWrapper:          certWrapper,
	}
}

type V1 struct {
	db                   Db
	componentSubscribers []ComponentSubscriber
	certWrapper          *cert.CertMagicWrapper
}

func (v *V1) onError(code ErrorCode, msg string, err error) error {
	log.Error(msg, "code", code, "err", err)
	return &barrister.JsonRpcError{Code: int(code), Message: msg}
}

func (v *V1) transformPutError(prefix string, err error) error {
	if err != nil {
		if err == IncorrectPreviousVersion {
			return &barrister.JsonRpcError{Code: 1004, Message: prefix + ": previousVersion is incorrect"}
		} else if err == AlreadyExists {
			return &barrister.JsonRpcError{Code: 1002, Message: prefix + ": already exists with key"}
		}
		return v.onError(DbError, "v1.server: transformPutError "+prefix, err)
	}
	return nil
}

func (v *V1) PutProject(input PutProjectInput) (PutProjectOutput, error) {
	// prepend project name to component and es names
	namePrefix := strings.TrimSpace(input.Project.Name) + "_"
	for x, c := range input.Project.Components {
		c.Component.Name = ensureStartsWith(namePrefix, c.Component.Name)
		for y, es := range c.EventSources {
			es.Name = ensureStartsWith(namePrefix, es.Name)
			es.ComponentName = c.Component.Name
			c.EventSources[y] = es
		}
		input.Project.Components[x] = c
	}

	projectName, err := validateProject("input.project", input.Project)
	if err != nil {
		return PutProjectOutput{}, err
	}
	input.Project.Name = projectName

	oldProject, err := v.getProjectInternal(input.Project.Name)
	if err != nil {
		return PutProjectOutput{}, err
	}

	diff := DiffProject(oldProject, input.Project)
	out := PutProjectOutput{
		Name:                input.Project.Name,
		ComponentsAdded:     make([]Component, 0),
		ComponentsUpdated:   make([]Component, 0),
		ComponentsRemoved:   make([]string, 0),
		EventSourcesAdded:   make([]EventSource, 0),
		EventSourcesUpdated: make([]EventSource, 0),
		EventSourcesRemoved: make([]string, 0),
	}

	for _, c := range diff.ComponentPut {
		_, err = v.db.PutComponent(c)
		if err != nil {
			return PutProjectOutput{}, v.onError(DbError, "PutComponent failed", err)
		}
		if c.Version == 0 {
			out.ComponentsAdded = append(out.ComponentsAdded, c)
		} else {
			out.ComponentsUpdated = append(out.ComponentsUpdated, c)
		}
	}
	for _, ev := range diff.EventSourcePut {
		_, err = v.db.PutEventSource(ev)
		if err != nil {
			return PutProjectOutput{}, v.onError(DbError, "PutEventSource failed", err)
		}
		if ev.Version == 0 {
			out.EventSourcesAdded = append(out.EventSourcesAdded, ev)
		} else {
			out.EventSourcesUpdated = append(out.EventSourcesUpdated, ev)
		}
	}
	for _, c := range diff.ComponentRemove {
		_, err = v.db.RemoveComponent(c)
		if err != nil {
			return PutProjectOutput{}, v.onError(DbError, "RemoveComponent failed", err)
		}
		out.ComponentsRemoved = append(out.ComponentsRemoved, c)
	}
	for _, ev := range diff.EventSourceRemove {
		_, err = v.db.RemoveEventSource(ev)
		if err != nil {
			return PutProjectOutput{}, v.onError(DbError, "RemoveEventSource failed", err)
		}
		out.EventSourcesRemoved = append(out.EventSourcesRemoved, ev)
	}

	return out, nil
}

func (v *V1) GetProject(input GetProjectInput) (GetProjectOutput, error) {
	project, err := v.getProjectInternal(input.Name)
	if err != nil {
		return GetProjectOutput{}, err
	}
	if len(project.Components) == 0 {
		return GetProjectOutput{}, &barrister.JsonRpcError{
			Code:    1003,
			Message: "No Project found with name: " + input.Name}
	}
	return GetProjectOutput{Project: project}, nil
}

func (v *V1) getProjectInternal(projectName string) (Project, error) {
	compOut, err := v.db.ListComponents(ListComponentsInput{ProjectName: projectName})
	if err != nil {
		return Project{}, v.onError(DbError, "ListComponents failed", err)
	}

	project := Project{
		Name:       projectName,
		Components: make([]ComponentWithEventSources, len(compOut.Components)),
	}

	for x, c := range compOut.Components {
		evOut, err := v.db.ListEventSources(ListEventSourcesInput{ComponentName: c.Name})
		if err != nil {
			return Project{}, v.onError(DbError, "ListEventSources failed", err)
		}
		project.Components[x] = ComponentWithEventSources{
			Component:    c,
			EventSources: evOut.EventSources,
		}
	}

	return project, nil
}

func (v *V1) RemoveProject(input RemoveProjectInput) (RemoveProjectOutput, error) {
	compOut, err := v.db.ListComponents(ListComponentsInput{ProjectName: input.Name})
	if err != nil {
		return RemoveProjectOutput{}, v.onError(DbError, "ListComponents failed", err)
	}
	for _, c := range compOut.Components {
		_, err = v.db.RemoveComponent(c.Name)
		if err != nil {
			return RemoveProjectOutput{}, v.onError(DbError, "RemoveComponent failed for: "+c.Name, err)
		}
	}

	return RemoveProjectOutput{
		Name:  input.Name,
		Found: len(compOut.Components) > 0,
	}, nil
}

func (v *V1) PutComponent(input PutComponentInput) (PutComponentOutput, error) {
	name, err := validateComponent("input.name", input.Component)
	if err != nil {
		return PutComponentOutput{}, err
	}

	// Save component to db
	newVersion, err := v.db.PutComponent(input.Component)
	if err != nil {
		return PutComponentOutput{}, v.transformPutError("PutComponent", err)
	}

	output := PutComponentOutput{
		Name:    name,
		Version: newVersion,
	}

	// Notify subscribers
	cn := ComponentNotification{
		PutComponent: &output,
	}
	for _, s := range v.componentSubscribers {
		go s.OnComponentNotification(cn)
	}

	return output, nil
}

func (v *V1) GetComponent(input GetComponentInput) (GetComponentOutput, error) {
	c, err := v.db.GetComponent(input.Name)
	if err != nil {
		if err == NotFound {
			return GetComponentOutput{}, &barrister.JsonRpcError{
				Code:    1003,
				Message: "No Component found with name: " + input.Name}
		}
		return GetComponentOutput{}, v.onError(DbError, "v1.server: db.GetComponent error", err)
	}

	return GetComponentOutput{Component: c}, nil
}

func (v *V1) ListComponents(input ListComponentsInput) (ListComponentsOutput, error) {
	return v.db.ListComponents(input)
}

func (v *V1) RemoveComponent(input RemoveComponentInput) (RemoveComponentOutput, error) {
	// * 1001 - input.name is invalid
	name, err := componentNameValid("input.name", input.Name)
	if err != nil {
		return RemoveComponentOutput{}, err
	}

	found, err := v.db.RemoveComponent(name)
	if err != nil {
		return RemoveComponentOutput{}, v.transformPutError("RemoveComponent", err)
	}

	output := RemoveComponentOutput{
		Name:  name,
		Found: found,
	}

	// Notify subscribers
	cn := ComponentNotification{
		RemoveComponent: &output,
	}
	for _, s := range v.componentSubscribers {
		go s.OnComponentNotification(cn)
	}

	return output, nil
}

func (v *V1) PutEventSource(input PutEventSourceInput) (PutEventSourceOutput, error) {
	name, err := validateEventSource("input.eventSource", input.EventSource)
	if err != nil {
		return PutEventSourceOutput{}, err
	}

	// * 1003 - input.componentName does not reference a component defined in the system
	component, err := v.db.GetComponent(input.EventSource.ComponentName)
	if err == NotFound {
		return PutEventSourceOutput{},
			newRpcErr(1003, "eventSource.component not found: "+input.EventSource.ComponentName)
	}

	input.EventSource.Name = name
	input.EventSource.ProjectName = component.ProjectName
	input.EventSource.ModifiedAt = common.NowMillis()
	newVersion, err := v.db.PutEventSource(input.EventSource)
	if err != nil {
		return PutEventSourceOutput{}, v.transformPutError("PutEventSource", err)
	}

	if input.EventSource.Http != nil && v.certWrapper != nil {
		v.certWrapper.AddHost(input.EventSource.Http.Hostname)
	}

	return PutEventSourceOutput{Name: name, Version: newVersion}, nil
}

func (v *V1) GetEventSource(input GetEventSourceInput) (GetEventSourceOutput, error) {
	es, err := v.db.GetEventSource(input.Name)
	if err != nil {
		if err == NotFound {
			return GetEventSourceOutput{}, &barrister.JsonRpcError{
				Code:    1003,
				Message: "No event source found with name: " + input.Name}
		}
		return GetEventSourceOutput{}, v.onError(DbError, "v1.server: db.GetEventSource error", err)
	}
	return GetEventSourceOutput{EventSource: es}, nil
}

func (v *V1) RemoveEventSource(input RemoveEventSourceInput) (RemoveEventSourceOutput, error) {
	// * 1001 - input.name is invalid
	name, err := eventSourceNameValid("input.name", input.Name)
	if err != nil {
		return RemoveEventSourceOutput{}, err
	}

	found, err := v.db.RemoveEventSource(name)
	return RemoveEventSourceOutput{
		Name:  name,
		Found: found,
	}, err
}

func (v *V1) ListEventSources(input ListEventSourcesInput) (ListEventSourcesOutput, error) {
	return v.db.ListEventSources(input)
}

func validateComponent(errPrefix string, component Component) (string, error) {
	// * 1001 - input.name is invalid
	return componentNameValid(errPrefix, component.Name)
}

func validateEventSource(errPrefix string, es EventSource) (string, error) {
	// * 1001 - input.name is invalid
	name, err := eventSourceNameValid(errPrefix+".name", es.Name)
	if err != nil {
		return "", err
	}
	// * 1001 - input.componentName is invalid
	_, err = componentNameValid(errPrefix+".componentName", es.ComponentName)
	if err != nil {
		return "", err
	}

	// * 1001 - No event source sub-type is provided
	if es.Http == nil && es.Cron == nil {
		return "", newRpcErr(1001, "No event source sub-type provided (e.g. http)")
	}

	// * 1001 - input.http is provided but has no hostname or pathPrefix
	if es.Http != nil {
		if es.Http.Hostname == "" && es.Http.PathPrefix == "" {
			return "", newRpcErr(1001, errPrefix+" http.hostname or http.pathPrefix must be provided")
		}
	}

	// * 1001 - input.cron is provided but has no method or path
	if es.Cron != nil {
		if es.Cron.Http.Method == "" {
			return "", newRpcErr(1001, errPrefix+" cron.http.method must be provided")
		}
		if es.Cron.Http.Path == "" {
			return "", newRpcErr(1001, errPrefix+" cron.http.path must be provided")
		}
	}

	return name, nil
}

func validateProject(errPrefix string, project Project) (string, error) {
	projectName, err := projectNameValid(errPrefix+".name", project.Name)
	if err != nil {
		return "", err
	}

	if len(project.Components) == 0 {
		return "", newRpcErr(1001, errPrefix+".components array cannot be empty")
	}

	for x, c := range project.Components {
		errPrefixC := fmt.Sprintf("%s.components[%d].component", errPrefix, x)
		_, err = validateComponent(errPrefixC, c.Component)
		if err != nil {
			return "", err
		}
		for y, es := range c.EventSources {
			errPrefixES := fmt.Sprintf("%s.components[%d].eventSources[%d]", errPrefix, x, y)
			_, err = validateEventSource(errPrefixES, es)
			if err != nil {
				return "", err
			}
		}
	}

	return projectName, nil
}

func ensureStartsWith(prefix string, s string) string {
	if !strings.HasPrefix(s, prefix) {
		s = prefix + s
	}
	return s
}
