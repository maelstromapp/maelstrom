package maelstrom

import (
	"fmt"
	"github.com/coopernurse/barrister-go"
	"github.com/coopernurse/maelstrom/pkg/cert"
	"github.com/coopernurse/maelstrom/pkg/common"
	"github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/mgutz/logxi/v1"
	"regexp"
	"sort"
	"strings"
	"time"
)

var _ v1.MaelstromService = (*MaelServiceImpl)(nil)
var nameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// tests can swap out
var timeNow = func() time.Time { return time.Now() }

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

func NewMaelServiceImpl(db Db, componentSubscribers []ComponentSubscriber, certWrapper *cert.CertMagicWrapper,
	myNodeId string, cluster *Cluster) *MaelServiceImpl {
	return &MaelServiceImpl{
		db:                   db,
		componentSubscribers: componentSubscribers,
		certWrapper:          certWrapper,
		myNodeId:             myNodeId,
		cluster:              cluster,
	}
}

type MaelServiceImpl struct {
	db                   Db
	componentSubscribers []ComponentSubscriber
	certWrapper          *cert.CertMagicWrapper
	myNodeId             string
	cluster              *Cluster
}

func (v *MaelServiceImpl) onError(code ErrorCode, msg string, err error) error {
	log.Error(msg, "code", code, "err", err)
	return &barrister.JsonRpcError{Code: int(code), Message: msg}
}

func (v *MaelServiceImpl) transformPutError(prefix string, err error) error {
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

func (v *MaelServiceImpl) ListProjects(input v1.ListProjectsInput) (v1.ListProjectsOutput, error) {
	input.NamePrefix = strings.ToLower(input.NamePrefix)
	out, err := v.db.ListProjects(input)
	if err == nil {
		sort.Sort(projectInfoByName(out.Projects))
	}
	return out, err
}

func (v *MaelServiceImpl) PutProject(input v1.PutProjectInput) (v1.PutProjectOutput, error) {
	// force names lower
	input.Project.Name = strings.ToLower(input.Project.Name)

	// prepend project name to component and es names
	namePrefix := strings.TrimSpace(input.Project.Name) + "_"
	for x, c := range input.Project.Components {
		c.Component.Name = ensureStartsWith(namePrefix, strings.ToLower(c.Component.Name))
		for y, ess := range c.EventSources {
			ess.EventSource.Name = ensureStartsWith(namePrefix, strings.ToLower(ess.EventSource.Name))
			ess.EventSource.ComponentName = c.Component.Name
			c.EventSources[y] = ess
		}
		input.Project.Components[x] = c
	}

	projectName, err := validateProject("input.project", input.Project)
	if err != nil {
		return v1.PutProjectOutput{}, err
	}
	input.Project.Name = projectName

	oldProject, err := v.getProjectInternal(input.Project.Name)
	if err != nil {
		return v1.PutProjectOutput{}, err
	}

	diff := DiffProject(oldProject, input.Project)
	out := v1.PutProjectOutput{
		Name:                input.Project.Name,
		ComponentsAdded:     make([]v1.Component, 0),
		ComponentsUpdated:   make([]v1.Component, 0),
		ComponentsRemoved:   make([]string, 0),
		EventSourcesAdded:   make([]v1.EventSource, 0),
		EventSourcesUpdated: make([]v1.EventSource, 0),
		EventSourcesRemoved: make([]string, 0),
	}

	// remove first - in case components were renamed (which is a remove + put)
	for _, c := range diff.ComponentRemove {
		found, err := v.db.RemoveComponent(c)
		if err != nil {
			return v1.PutProjectOutput{}, v.onError(DbError, "RemoveComponent failed", err)
		}
		out.ComponentsRemoved = append(out.ComponentsRemoved, c)
		v.notifyRemoveComponent(&v1.RemoveComponentOutput{Name: c, Found: found}, true)
	}
	for _, ev := range diff.EventSourceRemove {
		_, err = v.db.RemoveEventSource(ev)
		if err != nil {
			return v1.PutProjectOutput{}, v.onError(DbError, "RemoveEventSource failed", err)
		}
		out.EventSourcesRemoved = append(out.EventSourcesRemoved, ev)
	}

	// then put
	for _, c := range diff.ComponentPut {
		_, err := v.db.PutComponent(c)
		if err != nil {
			return v1.PutProjectOutput{}, v.onError(DbError, "PutComponent failed", err)
		}
		if c.Version == 0 {
			out.ComponentsAdded = append(out.ComponentsAdded, c)
		} else {
			out.ComponentsUpdated = append(out.ComponentsUpdated, c)
		}
		v.notifyPutComponent(c.Name, true)
	}
	for _, ev := range diff.EventSourcePut {
		_, err = v.db.PutEventSource(ev)
		if err != nil {
			return v1.PutProjectOutput{}, v.onError(DbError, "PutEventSource failed", err)
		}
		if ev.Version == 0 {
			out.EventSourcesAdded = append(out.EventSourcesAdded, ev)
		} else {
			out.EventSourcesUpdated = append(out.EventSourcesUpdated, ev)
		}
	}

	return out, nil
}

func (v *MaelServiceImpl) GetProject(input v1.GetProjectInput) (v1.GetProjectOutput, error) {
	input.Name = strings.ToLower(input.Name)
	project, err := v.getProjectInternal(input.Name)
	if err != nil {
		return v1.GetProjectOutput{}, err
	}
	if len(project.Components) == 0 {
		return v1.GetProjectOutput{}, &barrister.JsonRpcError{
			Code:    1003,
			Message: "No Project found with name: " + input.Name}
	}
	return v1.GetProjectOutput{Project: project}, nil
}

func (v *MaelServiceImpl) getProjectInternal(projectName string) (v1.Project, error) {
	compOut, err := v.db.ListComponents(v1.ListComponentsInput{ProjectName: projectName})
	if err != nil {
		return v1.Project{}, v.onError(DbError, "ListComponents failed", err)
	}

	project := v1.Project{
		Name:       projectName,
		Components: make([]v1.ComponentWithEventSources, len(compOut.Components)),
	}

	for x, c := range compOut.Components {
		evOut, err := v.db.ListEventSources(v1.ListEventSourcesInput{ComponentName: c.Name})
		if err != nil {
			return v1.Project{}, v.onError(DbError, "ListEventSources failed", err)
		}
		project.Components[x] = v1.ComponentWithEventSources{
			Component:    c,
			EventSources: evOut.EventSources,
		}
	}

	return project, nil
}

func (v *MaelServiceImpl) RemoveProject(input v1.RemoveProjectInput) (v1.RemoveProjectOutput, error) {
	input.Name = strings.ToLower(input.Name)
	compOut, err := v.db.ListComponents(v1.ListComponentsInput{ProjectName: input.Name})
	if err != nil {
		return v1.RemoveProjectOutput{}, v.onError(DbError, "ListComponents failed", err)
	}
	for _, c := range compOut.Components {
		_, err = v.db.RemoveComponent(c.Name)
		if err != nil {
			return v1.RemoveProjectOutput{}, v.onError(DbError, "RemoveComponent failed for: "+c.Name, err)
		}
	}

	return v1.RemoveProjectOutput{
		Name:  input.Name,
		Found: len(compOut.Components) > 0,
	}, nil
}

func (v *MaelServiceImpl) PutComponent(input v1.PutComponentInput) (v1.PutComponentOutput, error) {
	input.Component.Name = strings.ToLower(input.Component.Name)
	input.Component.ProjectName = strings.ToLower(input.Component.ProjectName)
	name, err := validateComponent("input.name", input.Component)
	if err != nil {
		return v1.PutComponentOutput{}, err
	}

	// Save component to db
	newVersion, err := v.db.PutComponent(input.Component)
	if err != nil {
		return v1.PutComponentOutput{}, v.transformPutError("PutComponent", err)
	}

	output := v1.PutComponentOutput{
		Name:    name,
		Version: newVersion,
	}

	// Notify subscribers
	v.notifyPutComponent(name, true)

	return output, nil
}

func (v *MaelServiceImpl) GetComponent(input v1.GetComponentInput) (v1.GetComponentOutput, error) {
	input.Name = strings.ToLower(input.Name)
	c, err := v.db.GetComponent(input.Name)
	if err != nil {
		if err == NotFound {
			return v1.GetComponentOutput{}, &barrister.JsonRpcError{
				Code:    1003,
				Message: "No Component found with name: " + input.Name}
		}
		return v1.GetComponentOutput{}, v.onError(DbError, "v1.server: db.GetComponent error", err)
	}

	return v1.GetComponentOutput{Component: c}, nil
}

func (v *MaelServiceImpl) ListComponents(input v1.ListComponentsInput) (v1.ListComponentsOutput, error) {
	input.NamePrefix = strings.ToLower(input.NamePrefix)
	input.ProjectName = strings.ToLower(input.ProjectName)
	return v.db.ListComponents(input)
}

func (v *MaelServiceImpl) RemoveComponent(input v1.RemoveComponentInput) (v1.RemoveComponentOutput, error) {
	// * 1001 - input.name is invalid
	input.Name = strings.ToLower(input.Name)
	name, err := componentNameValid("input.name", input.Name)
	if err != nil {
		return v1.RemoveComponentOutput{}, err
	}

	found, err := v.db.RemoveComponent(name)
	if err != nil {
		return v1.RemoveComponentOutput{}, v.transformPutError("RemoveComponent", err)
	}

	output := v1.RemoveComponentOutput{
		Name:  name,
		Found: found,
	}

	// Notify subscribers
	v.notifyRemoveComponent(&output, true)

	return output, nil
}

func (v *MaelServiceImpl) PutEventSource(input v1.PutEventSourceInput) (v1.PutEventSourceOutput, error) {
	input.EventSource.Name = strings.ToLower(input.EventSource.Name)
	input.EventSource.ComponentName = strings.ToLower(input.EventSource.ComponentName)

	name, err := validateEventSource("input.eventSource", input.EventSource)
	if err != nil {
		return v1.PutEventSourceOutput{}, err
	}

	// * 1003 - input.componentName does not reference a component defined in the system
	component, err := v.db.GetComponent(input.EventSource.ComponentName)
	if err == NotFound {
		return v1.PutEventSourceOutput{},
			newRpcErr(1003, "eventSource.component not found: "+input.EventSource.ComponentName)
	}

	input.EventSource.Name = name
	input.EventSource.ProjectName = component.ProjectName
	input.EventSource.ModifiedAt = common.NowMillis()
	newVersion, err := v.db.PutEventSource(input.EventSource)
	if err != nil {
		return v1.PutEventSourceOutput{}, v.transformPutError("PutEventSource", err)
	}

	if input.EventSource.Http != nil && v.certWrapper != nil {
		v.certWrapper.AddHost(input.EventSource.Http.Hostname)
	}

	return v1.PutEventSourceOutput{Name: name, Version: newVersion}, nil
}

func (v *MaelServiceImpl) GetEventSource(input v1.GetEventSourceInput) (v1.GetEventSourceOutput, error) {
	input.Name = strings.ToLower(input.Name)
	es, err := v.db.GetEventSource(input.Name)
	if err != nil {
		if err == NotFound {
			return v1.GetEventSourceOutput{}, &barrister.JsonRpcError{
				Code:    1003,
				Message: "No event source found with name: " + input.Name}
		}
		return v1.GetEventSourceOutput{}, v.onError(DbError, "v1.server: db.GetEventSource error", err)
	}
	return v1.GetEventSourceOutput{EventSource: es}, nil
}

func (v *MaelServiceImpl) RemoveEventSource(input v1.RemoveEventSourceInput) (v1.RemoveEventSourceOutput, error) {
	input.Name = strings.ToLower(input.Name)
	// * 1001 - input.name is invalid
	name, err := eventSourceNameValid("input.name", input.Name)
	if err != nil {
		return v1.RemoveEventSourceOutput{}, err
	}

	found, err := v.db.RemoveEventSource(name)
	return v1.RemoveEventSourceOutput{
		Name:  name,
		Found: found,
	}, err
}

func (v *MaelServiceImpl) ToggleEventSources(input v1.ToggleEventSourcesInput) (v1.ToggleEventSourcesOutput, error) {
	eventSourceNames := make([]string, 0)

	nextToken := ""
	loop := true
	for loop {
		listOut, err := v.ListEventSources(v1.ListEventSourcesInput{
			NamePrefix:      input.NamePrefix,
			ComponentName:   input.ComponentName,
			ProjectName:     input.ProjectName,
			EventSourceType: input.EventSourceType,
			Limit:           1000,
			NextToken:       nextToken,
		})
		if err != nil {
			return v1.ToggleEventSourcesOutput{}, v.onError(DbError, "v1.server: db.ListEventSources error", err)
		}

		batchNames := make([]string, 0)
		for _, ess := range listOut.EventSources {
			es := ess.EventSource
			if ess.Enabled != input.Enabled {
				batchNames = append(batchNames, es.Name)
			}
		}
		if len(batchNames) > 0 {
			_, err = v.db.SetEventSourcesEnabled(batchNames, input.Enabled)
			if err != nil {
				return v1.ToggleEventSourcesOutput{},
					v.onError(DbError, "v1.server: db.SetEventSourceDisabled error", err)
			}
			eventSourceNames = append(eventSourceNames, batchNames...)
		}

		nextToken = listOut.NextToken
		if nextToken == "" {
			loop = false
		}
	}

	return v1.ToggleEventSourcesOutput{
		Enabled:          input.Enabled,
		EventSourceNames: eventSourceNames,
	}, nil
}

func (v *MaelServiceImpl) ListEventSources(input v1.ListEventSourcesInput) (v1.ListEventSourcesOutput, error) {
	input.NamePrefix = strings.ToLower(input.NamePrefix)
	input.ComponentName = strings.ToLower(input.ComponentName)
	input.ProjectName = strings.ToLower(input.ProjectName)
	return v.db.ListEventSources(input)
}

func (v *MaelServiceImpl) notifyPutComponent(componentName string, broadcast bool) {
	comp, err := v.db.GetComponent(componentName)
	if err != nil {
		log.Error("mael_svc: notifyPutComponent GetComponent failed", "component", componentName, "err", err)
		return
	}

	change := v1.DataChangedUnion{
		PutComponent: &comp,
	}
	for _, s := range v.componentSubscribers {
		go s.OnComponentNotification(change)
	}
	if broadcast && v.cluster != nil {
		v.cluster.BroadcastDataChanged(v1.NotifyDataChangedInput{
			NodeId:  v.myNodeId,
			Changes: []v1.DataChangedUnion{change},
		})
	}
}

func (v *MaelServiceImpl) notifyRemoveComponent(rmOutput *v1.RemoveComponentOutput, broadcast bool) {
	cn := v1.DataChangedUnion{
		RemoveComponent: rmOutput,
	}
	for _, s := range v.componentSubscribers {
		go s.OnComponentNotification(cn)
	}
	if broadcast && v.cluster != nil {
		v.cluster.BroadcastDataChanged(v1.NotifyDataChangedInput{
			NodeId:  v.myNodeId,
			Changes: []v1.DataChangedUnion{{RemoveComponent: rmOutput}},
		})
	}
}

func (v *MaelServiceImpl) NotifyDataChanged(input v1.NotifyDataChangedInput) (v1.NotifyDataChangedOutput, error) {
	for _, change := range input.Changes {
		if change.PutComponent != nil {
			v.notifyPutComponent(change.PutComponent.Name, false)
		}
		if change.RemoveComponent != nil {
			v.notifyRemoveComponent(change.RemoveComponent, false)
		}
	}
	return v1.NotifyDataChangedOutput{RespondingNodeId: v.myNodeId}, nil
}

func validateComponent(errPrefix string, component v1.Component) (string, error) {
	// * 1001 - input.name is invalid
	return componentNameValid(errPrefix, component.Name)
}

func validateEventSource(errPrefix string, es v1.EventSource) (string, error) {
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
	if es.Http == nil && es.Cron == nil && es.Sqs == nil {
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

func validateProject(errPrefix string, project v1.Project) (string, error) {
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
			_, err = validateEventSource(errPrefixES, es.EventSource)
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
