package v1

import (
	"fmt"
	"github.com/coopernurse/barrister-go"
	"github.com/google/gofuzz"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"math/rand"
	"testing"
)

func panicOnErr(err error) {
	if err != nil {
		panic(err)
	}
}

func createV1() (*V1, *SqlDb) {
	sqlDb, err := NewSqlDb("sqlite3", "file:test.db?cache=shared&_journal_mode=MEMORY&mode=rwc")
	panicOnErr(err)

	// run sql migrations and delete any existing data
	panicOnErr(sqlDb.Migrate())
	panicOnErr(sqlDb.DeleteAll())

	return NewV1(sqlDb, nil, nil), sqlDb
}

func TestComponentCRUD(t *testing.T) {
	f := fuzz.New()
	svc, _ := createV1()

	// Put saves a new component
	out := putComponentOK(t, svc, "abc")
	assert.Equal(t, int64(1), out.Version)

	// Put updates an existing component with the same name
	input := validComponent("c2")
	_, err := svc.PutComponent(input)
	assert.Nil(t, err)

	expected := GetComponentOutput{
		Component: Component{
			Name:    input.Component.Name,
			Version: 1,
			Docker:  input.Component.Docker,
		},
	}
	getOut, err := svc.GetComponent(GetComponentInput{Name: "c2"})
	assert.Nil(t, err)
	getOut.Component.ModifiedAt = 0
	assert.Equal(t, expected, getOut)

	// Put and Get are symmetric
	for i := 0; i < 10; i++ {
		var input PutComponentInput
		for !nameRE.MatchString(input.Component.Name) || len(input.Component.Name) < 3 {
			f.Fuzz(&input)
		}
		input.Component.Version = 0
		out, err := svc.PutComponent(input)
		assert.Nil(t, err, "Got err: %v", err)

		expected := GetComponentOutput{
			Component: Component{
				Name:        input.Component.Name,
				Version:     1,
				Docker:      input.Component.Docker,
				ProjectName: input.Component.ProjectName,
				Environment: input.Component.Environment,
			},
		}

		getIn := GetComponentInput{Name: out.Name}
		getOut, err := svc.GetComponent(getIn)
		assert.Nil(t, err)
		getOut.Component.ModifiedAt = 0
		assert.Equal(t, expected, getOut)
	}

	// Remove deletes a component

	// Save component
	putComponentOK(t, svc, "deleteme")

	// Get - should return component
	_, err = svc.GetComponent(GetComponentInput{Name: "deleteme"})
	assert.Nil(t, err)

	// Remove
	rmOut, err := svc.RemoveComponent(RemoveComponentInput{Name: "deleteme"})
	assert.Nil(t, err)
	assert.Equal(t, RemoveComponentOutput{Name: "deleteme", Found: true}, rmOut)

	// Get - should raise 1003 error
	_, err = svc.GetComponent(GetComponentInput{Name: "deleteme"})
	assertRpcErr(t, err, 1003)

	// Remove again - should not error, but found=false
	rmOut, err = svc.RemoveComponent(RemoveComponentInput{Name: "deleteme"})
	assert.Nil(t, err)
	assert.Equal(t, RemoveComponentOutput{Name: "deleteme", Found: false}, rmOut)
}

func TestComponentValidation(t *testing.T) {
	svc, _ := createV1()

	// Raises 1001 if name is invalid
	invalid := []string{"", " ", "\t\n", "hello)"}
	for _, name := range invalid {
		i := validComponent(name)
		out, err := svc.PutComponent(i)
		assert.Equal(t, PutComponentOutput{}, out)
		assertRpcErr(t, err, 1001)
	}

	valid := []string{"aBc", "92dak_-9s9"}
	for _, name := range valid {
		i := PutComponentInput{Component: Component{Name: name}}
		_, err := svc.PutComponent(i)
		assert.Nil(t, err, "input: %+v", i)
	}

	// Raises 1002 if name already exists and previousVersion is zero
	input := validComponent("1002test")

	// first put should succeed
	_, err := svc.PutComponent(input)
	assert.Nil(t, err)

	// put again - should raise 1002
	_, err = svc.PutComponent(input)
	assertRpcErr(t, err, 1002)

	// Raises 1004 if previousVersion is not current
	input = validComponent("1004test")

	// first put should succeed
	out, err := svc.PutComponent(input)
	assert.Nil(t, err)
	assert.Equal(t, int64(1), out.Version)

	// put again - should raise 1004
	input.Component.Version = out.Version + 1
	out, err = svc.PutComponent(input)
	assertRpcErr(t, err, 1004)

	// Raises 1003 if no component has that name
	getInput := GetComponentInput{
		Name: "notfound",
	}
	_, err = svc.GetComponent(getInput)
	assertRpcErr(t, err, 1003)
}

func TestComponentList(t *testing.T) {
	svc, sqlDb := createV1()
	assert.Nil(t, sqlDb.DeleteAll())

	// insert a bunch of components with name starting with "list-"
	components := make([]string, 0)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("list-%d", i)
		putComponentOK(t, svc, name)
		components = append(components, name)
	}

	// Returns components in alphabetical order by name
	out, err := svc.ListComponents(ListComponentsInput{})
	assert.Nil(t, err)
	assert.Equal(t, len(components), len(out.Components))
	assert.Equal(t, "", out.NextToken)
	for x, cname := range components {
		assert.Equal(t, cname, out.Components[x].Name)
		assert.Equal(t, int64(1), out.Components[x].Version)
	}

	// Optionally filters by name prefix
	putComponentOK(t, svc, "other1")

	// should get all 6 items
	out, err = svc.ListComponents(ListComponentsInput{})
	assert.Nil(t, err)
	assert.Equal(t, []string{"list-0", "list-1", "list-2", "list-3", "list-4", "other1"},
		componentNames(out.Components))

	// load with "list" prefix - should get 5
	out, err = svc.ListComponents(ListComponentsInput{NamePrefix: "list"})
	assert.Nil(t, err)
	assert.Equal(t, []string{"list-0", "list-1", "list-2", "list-3", "list-4"}, componentNames(out.Components))

	// load with "other" prefix - should get 1
	out, err = svc.ListComponents(ListComponentsInput{NamePrefix: "other"})
	assert.Nil(t, err)
	assert.Equal(t, []string{"other1"}, componentNames(out.Components))

	// Returns an empty list if no components exist
	out, err = svc.ListComponents(ListComponentsInput{NamePrefix: "bogusprefix"})
	assert.Nil(t, err)
	assert.Equal(t, []Component{}, out.Components)
	assert.Equal(t, "", out.NextToken)

	// Sets nextToken if there are more results to return
	out, err = svc.ListComponents(ListComponentsInput{Limit: 2})
	assert.Nil(t, err)
	assert.Equal(t, []string{"list-0", "list-1"}, componentNames(out.Components))
	assert.NotEqual(t, "", out.NextToken)
	out, err = svc.ListComponents(ListComponentsInput{Limit: 2, NextToken: out.NextToken})
	assert.Nil(t, err)
	assert.Equal(t, []string{"list-2", "list-3"}, componentNames(out.Components))
	assert.NotEqual(t, "", out.NextToken)
	out, err = svc.ListComponents(ListComponentsInput{Limit: 4, NextToken: out.NextToken})
	assert.Nil(t, err)
	assert.Equal(t, []string{"list-4", "other1"}, componentNames(out.Components))
	assert.Equal(t, "", out.NextToken)
}

func TestEventSourceCRUD(t *testing.T) {
	svc, _ := createV1()
	putComponentOK(t, svc, "comp1")

	putIn, _ := putEventSourceOK(t, svc, "ev1", "comp1")

	// get event source and verify equality
	getOut, err := svc.GetEventSource(GetEventSourceInput{Name: "ev1"})
	assert.Nil(t, err, "GetEventSource err: %v", err)
	putIn.EventSource.Version = 1
	putIn.EventSource.ModifiedAt = getOut.EventSource.ModifiedAt
	assert.Equal(t, putIn.EventSource, getOut.EventSource)

	// remove event source
	rmOut, err := svc.RemoveEventSource(RemoveEventSourceInput{Name: "ev1"})
	assert.Nil(t, err)
	assert.True(t, rmOut.Found)

	// get event source - verify a 1003 is raised
	_, err = svc.GetEventSource(GetEventSourceInput{Name: "ev1"})
	assertRpcErr(t, err, 1003)

	// remove event source again - should no-op and return found=false
	rmOut, err = svc.RemoveEventSource(RemoveEventSourceInput{Name: "ev1"})
	assert.Nil(t, err)
	assert.False(t, rmOut.Found)
}

func TestEventSourceValidation(t *testing.T) {
	svc, _ := createV1()
	putComponentOK(t, svc, "comp1")

	// Validates event source name and component name
	invalid := []string{"", "  ", "\tstuff and space", "bad&chars%here"}
	for _, name := range invalid {
		putEventSourceFailsWithCode(t, svc, 1001, func(input *PutEventSourceInput) {
			input.EventSource.Name = name
		})
		putEventSourceFailsWithCode(t, svc, 1001, func(input *PutEventSourceInput) {
			input.EventSource.ComponentName = name
		})
	}

	// Validates event source sub-type is present
	putEventSourceFailsWithCode(t, svc, 1001, func(input *PutEventSourceInput) {
		input.EventSource.Http = nil
	})

	// Raises 1003 if componentName is not found
	putEventSourceFailsWithCode(t, svc, 1003, func(input *PutEventSourceInput) {
		input.EventSource.ComponentName = "valid-but-not-in-db"
	})

	// Raises 1004 if previousVersion is not current
	putEventSourceFailsWithCode(t, svc, 1004, func(input *PutEventSourceInput) {
		input.EventSource.Version = 50000
	})
}

func TestEventSourceList(t *testing.T) {
	svc, sqlDb := createV1()
	assert.Nil(t, sqlDb.DeleteAll())

	// create component we can use as a FK in event sources
	compName := "comp1"
	putComponentOK(t, svc, compName)

	// insert a bunch of event sources with name starting with "list-"
	eventSources := make([]string, 0)
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("list-%d", i)
		putEventSourceOK(t, svc, name, compName)
		eventSources = append(eventSources, name)
	}

	// Returns components in alphabetical order by name
	out, err := svc.ListEventSources(ListEventSourcesInput{})
	assert.Nil(t, err)
	assert.Equal(t, len(eventSources), len(out.EventSources))
	assert.Equal(t, "", out.NextToken)
	for x, cname := range eventSources {
		assert.Equal(t, cname, out.EventSources[x].Name)
		assert.Equal(t, int64(1), out.EventSources[x].Version)
	}

	// Optionally filters by name prefix
	putEventSourceOK(t, svc, "other1", compName)

	// should get all items
	listEventSourcesOK(t, svc, ListEventSourcesInput{},
		[]string{"list-0", "list-1", "list-2", "list-3", "list-4", "other1"})

	// load with "list" prefix - should get 5
	listEventSourcesOK(t, svc, ListEventSourcesInput{NamePrefix: "list"},
		[]string{"list-0", "list-1", "list-2", "list-3", "list-4"})

	// load with "other" prefix - should get 1
	listEventSourcesOK(t, svc, ListEventSourcesInput{NamePrefix: "other"}, []string{"other1"})

	// no match - empty list
	listEventSourcesOK(t, svc, ListEventSourcesInput{NamePrefix: "zzzz"}, []string{})

	// Optionally filters by component name

	// matches all
	listEventSourcesOK(t, svc, ListEventSourcesInput{ComponentName: compName},
		[]string{"list-0", "list-1", "list-2", "list-3", "list-4", "other1"})

	// combine namePrefix and compName
	listEventSourcesOK(t, svc,
		ListEventSourcesInput{NamePrefix: "other", ComponentName: compName}, []string{"other1"})

	// no match - empty list
	listEventSourcesOK(t, svc, ListEventSourcesInput{ComponentName: "zzzz"}, []string{})
	listEventSourcesOK(t, svc, ListEventSourcesInput{ComponentName: compName, NamePrefix: "zzz"}, []string{})

	// Optionally filters by event source type
	listEventSourcesOK(t, svc, ListEventSourcesInput{EventSourceType: EventSourceTypeHttp},
		[]string{"list-0", "list-1", "list-2", "list-3", "list-4", "other1"})
	listEventSourcesOK(t, svc, ListEventSourcesInput{NamePrefix: "other", EventSourceType: EventSourceTypeHttp},
		[]string{"other1"})

	// Returns an empty list if no components exist
	out = listEventSourcesOK(t, svc, ListEventSourcesInput{NamePrefix: "bogusprefix"}, []string{})
	assert.Equal(t, []EventSource{}, out.EventSources)
	assert.Equal(t, "", out.NextToken)

	// Sets nextToken if there are more results to return
	out = listEventSourcesOK(t, svc, ListEventSourcesInput{Limit: 2}, []string{"list-0", "list-1"})
	out = listEventSourcesOK(t, svc, ListEventSourcesInput{Limit: 2, NextToken: out.NextToken},
		[]string{"list-2", "list-3"})
	out = listEventSourcesOK(t, svc, ListEventSourcesInput{Limit: 2, NextToken: out.NextToken},
		[]string{"list-4", "other1"})
	assert.Equal(t, "", out.NextToken)
}

func TestProjectCRUD(t *testing.T) {
	svc, _ := createV1()

	// put project
	projName := "proj1"
	putIn, _ := putProjectOK(t, svc, projName)

	// get project and verify equality
	getOut, err := svc.GetProject(GetProjectInput{Name: projName})
	assert.Nil(t, err, "GetProject err: %v", err)
	sanitizeComponentsWithEventSources(getOut.Project.Components)
	assert.Equal(t, putIn.Project, getOut.Project)

	// remove project
	rmOut, err := svc.RemoveProject(RemoveProjectInput{Name: projName})
	assert.Nil(t, err)
	assert.True(t, rmOut.Found)

	// get project - verify a 1003 is raised
	_, err = svc.GetProject(GetProjectInput{Name: projName})
	assertRpcErr(t, err, 1003)

	// remove project again - should no-op and return found=false
	rmOut, err = svc.RemoveProject(RemoveProjectInput{Name: projName})
	assert.Nil(t, err)
	assert.False(t, rmOut.Found)
}

func TestProjectValidation(t *testing.T) {
	svc, _ := createV1()

	// Raises 1001 if name is invalid
	invalid := []string{"", " ", "\t\n", "hello)", "toolong00000000000000"}
	for _, name := range invalid {
		i := PutProjectInput{
			Project: Project{
				Name: name,
				Components: []ComponentWithEventSources{
					{
						Component: validComponent("componentName").Component,
					},
				},
			},
		}
		out, err := svc.PutProject(i)
		assert.Equal(t, PutProjectOutput{}, out)
		assertRpcErr(t, err, 1001)
	}

	invalidInputs := []PutProjectInput{
		// must have at least one component
		{
			Project: Project{
				Name:       "projectName",
				Components: []ComponentWithEventSources{},
			},
		},
		// invalid component
		{
			Project: Project{
				Name: "projectName",
				Components: []ComponentWithEventSources{
					{
						Component: validComponent("co!@mponentName").Component,
					},
				},
			},
		},
		// invalid event source
		{
			Project: Project{
				Name: "projectName",
				Components: []ComponentWithEventSources{
					{
						Component: validComponent("okName").Component,
						EventSources: []EventSource{
							{
								Name: "okName",
							},
						},
					},
				},
			},
		},
	}
	for _, input := range invalidInputs {
		out, err := svc.PutProject(input)
		assert.Equal(t, PutProjectOutput{}, out)
		assertRpcErr(t, err, 1001)
	}

	// valid cases
	valid := []string{"aBc", "92dak_-9s9"}
	for _, name := range valid {
		i := PutProjectInput{
			Project: Project{
				Name: name,
				Components: []ComponentWithEventSources{
					{
						Component: validComponent("componentName").Component,
					},
				},
			},
		}
		_, err := validateProject("", i.Project)
		assert.Nil(t, err, "input: %+v", i)
	}
}

////////////////////////////////////////////////////////

func putProjectOK(t *testing.T, svc *V1, projectName string) (PutProjectInput, PutProjectOutput) {
	input := validProject(projectName)
	out, err := svc.PutProject(input)
	assert.Nil(t, err, "PutProject failed for: %s - %v", projectName, err)
	assert.Equal(t, input.Project.Name, out.Name)
	return input, out
}

func validProject(projectName string) PutProjectInput {
	num := rand.Intn(5) + 1
	components := make([]ComponentWithEventSources, num)
	for i := 0; i < num; i++ {
		compName := fmt.Sprintf("comp-%d", i)
		comp := validComponent(compName)
		comp.Component.ProjectName = projectName
		esNum := rand.Intn(5)
		eventSources := make([]EventSource, esNum)
		for x := 0; x < esNum; x++ {
			esName := fmt.Sprintf("es-%d-%d", i, x)
			eventSources[x] = validEventSource(esName, compName).EventSource
			eventSources[x].ProjectName = projectName
		}
		components[i] = ComponentWithEventSources{
			Component:    comp.Component,
			EventSources: eventSources,
		}
	}

	return PutProjectInput{
		Project: Project{
			Name:       projectName,
			Components: components,
		},
	}
}

func validComponent(componentName string) PutComponentInput {
	return PutComponentInput{
		Component: Component{
			Name: componentName,
			Docker: &DockerComponent{
				Image:    "coopernurse/foo",
				HttpPort: 8080,
			},
		},
	}
}

func putComponentOK(t *testing.T, svc *V1, name string) PutComponentOutput {
	input := validComponent(name)
	out, err := svc.PutComponent(input)
	assert.Nil(t, err, "PutComponent failed for: %s - %v", name, err)
	assert.Equal(t, input.Component.Name, out.Name)
	return out
}

func validEventSource(eventSourceName string, componentName string) PutEventSourceInput {
	return PutEventSourceInput{
		EventSource: EventSource{
			Name:          eventSourceName,
			ComponentName: componentName,
			Http: &HttpEventSource{
				Hostname: "www.example.com",
			},
		},
	}
}

func putEventSourceFailsWithCode(t *testing.T, svc *V1, errCode int, mutator func(i *PutEventSourceInput)) {
	input := validEventSource("esname", "comp1")
	mutator(&input)
	_, err := svc.PutEventSource(input)
	assert.NotNil(t, err, "PutEventSource didn't error for input: %v", input)
	rpcErr, _ := err.(*barrister.JsonRpcError)
	assert.Equal(t, errCode, rpcErr.Code)
}

func putEventSourceOK(t *testing.T, svc *V1, eventSourceName string,
	componentName string) (PutEventSourceInput, PutEventSourceOutput) {
	input := validEventSource(eventSourceName, componentName)
	out, err := svc.PutEventSource(input)
	assert.Nil(t, err, "PutEventSource failed for: %s - %v", eventSourceName, err)
	assert.Equal(t, eventSourceName, out.Name)
	return input, out
}

func listEventSourcesOK(t *testing.T, svc *V1, input ListEventSourcesInput,
	expectedEventSourceNames []string) ListEventSourcesOutput {
	out, err := svc.ListEventSources(input)
	assert.Nil(t, err)
	assert.Equal(t, expectedEventSourceNames, eventSourceNames(out.EventSources))
	return out
}

func componentNames(list []Component) []string {
	names := make([]string, len(list))
	for x, c := range list {
		names[x] = c.Name
	}
	return names
}

func eventSourceNames(list []EventSource) []string {
	names := make([]string, len(list))
	for x, c := range list {
		names[x] = c.Name
	}
	return names
}

func sanitizeComponentsWithEventSources(list []ComponentWithEventSources) {
	for i, ces := range list {
		list[i].Component = sanitizeComponent(&ces.Component)
		for x, es := range ces.EventSources {
			ces.EventSources[x] = sanitizeEventSource(&es)
		}
	}
}

func sanitizeComponent(c *Component) Component {
	c.Version = 0
	c.ModifiedAt = 0
	return *c
}

func sanitizeEventSource(es *EventSource) EventSource {
	es.Version = 0
	es.ModifiedAt = 0
	return *es
}

func assertRpcErr(t *testing.T, err error, errCode int) {
	assert.NotNil(t, err)
	rpcErr, ok := err.(*barrister.JsonRpcError)
	assert.True(t, ok)
	assert.Equal(t, errCode, rpcErr.Code)
}
