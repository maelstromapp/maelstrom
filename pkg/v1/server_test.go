package v1

import (
	"fmt"
	"github.com/coopernurse/barrister-go"
	"github.com/google/gofuzz"
	"testing"

	. "github.com/franela/goblin"
	_ "github.com/mattn/go-sqlite3"
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

	return NewV1(sqlDb, nil), sqlDb
}

func TestComponent(t *testing.T) {
	g := Goblin(t)
	f := fuzz.New()
	svc, sqlDb := createV1()

	g.Describe("Component CRUD", func() {
		g.It("Put saves a new component", func() {
			out := putComponentOK(g, svc, "abc")
			g.Assert(out.Version).Eql(int64(1))
		})
		g.It("Put updates an existing component with the same name", func() {
			input := validComponent("c2")
			_, err := svc.PutComponent(input)
			g.Assert(err == nil).IsTrue()

			expected := GetComponentOutput{
				Component: Component{
					Name:    input.Component.Name,
					Version: 1,
					Docker:  input.Component.Docker,
				},
			}
			out, err := svc.GetComponent(GetComponentInput{Name: "c2"})
			g.Assert(err == nil).IsTrue()
			out.Component.ModifiedAt = 0
			g.Assert(out).Eql(expected)

		})
		g.It("Put and Get are symmetric", func() {
			for i := 0; i < 10; i++ {
				var input PutComponentInput
				for !nameRE.MatchString(input.Component.Name) || len(input.Component.Name) < 3 {
					f.Fuzz(&input)
				}
				input.Component.Version = 0
				out, err := svc.PutComponent(input)
				g.Assert(err == nil).IsTrue(fmt.Sprintf("Got err: %v", err))

				expected := GetComponentOutput{
					Component: Component{
						Name:    input.Component.Name,
						Version: 1,
						Docker:  input.Component.Docker,
					},
				}

				getIn := GetComponentInput{Name: out.Name}
				getOut, err := svc.GetComponent(getIn)
				g.Assert(err == nil).IsTrue()
				getOut.Component.ModifiedAt = 0
				g.Assert(getOut).Eql(expected)
			}
		})
		g.It("Remove deletes a component", func() {
			// Save component
			putComponentOK(g, svc, "deleteme")

			// Get - should return component
			_, err := svc.GetComponent(GetComponentInput{Name: "deleteme"})
			g.Assert(err == nil).IsTrue()

			// Remove
			out, err := svc.RemoveComponent(RemoveComponentInput{Name: "deleteme"})
			g.Assert(err == nil).IsTrue()
			g.Assert(out).Eql(RemoveComponentOutput{Name: "deleteme", Found: true})

			// Get - should raise 1003 error
			_, err = svc.GetComponent(GetComponentInput{Name: "deleteme"})
			g.Assert(err != nil).IsTrue()
			rpcErr, _ := err.(*barrister.JsonRpcError)
			g.Assert(rpcErr.Code).Eql(1003)

			// Remove again - should not error, but found=false
			out, err = svc.RemoveComponent(RemoveComponentInput{Name: "deleteme"})
			g.Assert(err == nil).IsTrue()
			g.Assert(out).Eql(RemoveComponentOutput{Name: "deleteme", Found: false})
		})
	})

	g.Describe("PutComponent Validation", func() {
		g.It("Raises 1001 if name is invalid", func() {
			invalid := []string{"", " ", "\t\n", "hello)"}
			for _, name := range invalid {
				i := validComponent(name)
				out, err := svc.PutComponent(i)
				g.Assert(out).Eql(PutComponentOutput{})
				g.Assert(err != nil).IsTrue(fmt.Sprintf("input: %+v", i))
				rpcErr, ok := err.(*barrister.JsonRpcError)
				g.Assert(ok).IsTrue()
				g.Assert(rpcErr.Code).Eql(1001)
			}

			valid := []string{"aBc", "92dak_-9s9"}
			for _, name := range valid {
				i := PutComponentInput{Component: Component{Name: name}}
				_, err := svc.PutComponent(i)
				g.Assert(err == nil).IsTrue(fmt.Sprintf("input: %+v", i))
			}
		})
		g.It("Raises 1002 if name already exists and previousVersion is zero", func() {
			input := validComponent("1002test")

			// first put should succeed
			_, err := svc.PutComponent(input)
			g.Assert(err == nil).IsTrue()

			// put again - should raise 1002
			_, err = svc.PutComponent(input)
			g.Assert(err != nil).IsTrue(fmt.Sprintf("Didn't get an error on 2nd put: %v", err))
			rpcErr, ok := err.(*barrister.JsonRpcError)
			g.Assert(ok).IsTrue()
			g.Assert(rpcErr.Code).Eql(1002)
		})
		g.It("Raises 1004 if previousVersion is not current", func() {
			input := validComponent("1004test")

			// first put should succeed
			out, err := svc.PutComponent(input)
			g.Assert(err == nil).IsTrue()
			g.Assert(out.Version).Eql(int64(1))

			// put again - should raise 1004
			input.Component.Version = out.Version + 1
			out, err = svc.PutComponent(input)
			g.Assert(err != nil).IsTrue(fmt.Sprintf("expected err, got: %+v", out))
			rpcErr, ok := err.(*barrister.JsonRpcError)
			g.Assert(ok).IsTrue()
			g.Assert(rpcErr.Code).Eql(1004)
		})
	})

	g.Describe("GetComponent Validation", func() {
		g.It("Raises 1003 if no component has that name", func() {
			input := GetComponentInput{
				Name: "notfound",
			}
			_, err := svc.GetComponent(input)
			g.Assert(err != nil).IsTrue()
			rpcErr, _ := err.(*barrister.JsonRpcError)
			g.Assert(rpcErr.Code).Eql(1003)
		})
	})

	g.Describe("ListComponents", func() {

		g.Assert(sqlDb.DeleteAll()).Eql(nil)

		// insert a bunch of components with name starting with "list-"
		components := make([]string, 0)
		for i := 0; i < 5; i++ {
			name := fmt.Sprintf("list-%d", i)
			putComponentOK(g, svc, name)
			components = append(components, name)
		}

		g.It("Returns components in alphabetical order by name", func() {
			out, err := svc.ListComponents(ListComponentsInput{})
			g.Assert(err == nil).IsTrue()
			g.Assert(len(out.Components)).Eql(len(components))
			g.Assert(out.NextToken).Eql("")
			for x, cname := range components {
				g.Assert(out.Components[x].Name).Eql(cname)
				g.Assert(out.Components[x].Version).Eql(int64(1))
			}
		})
		g.It("Optionally filters by name prefix", func() {
			putComponentOK(g, svc, "other1")

			// should get all 6 items
			out, err := svc.ListComponents(ListComponentsInput{})
			g.Assert(err == nil).IsTrue()
			g.Assert(componentNames(out.Components)).Eql(
				[]string{"list-0", "list-1", "list-2", "list-3", "list-4", "other1"})

			// load with "list" prefix - should get 5
			out, err = svc.ListComponents(ListComponentsInput{NamePrefix: "list"})
			g.Assert(err == nil).IsTrue()
			g.Assert(componentNames(out.Components)).Eql([]string{"list-0", "list-1", "list-2", "list-3", "list-4"})

			// load with "other" prefix - should get 1
			out, err = svc.ListComponents(ListComponentsInput{NamePrefix: "other"})
			g.Assert(err == nil).IsTrue()
			g.Assert(componentNames(out.Components)).Eql([]string{"other1"})
		})
		g.It("Returns an empty list if no components exist", func() {
			out, err := svc.ListComponents(ListComponentsInput{NamePrefix: "bogusprefix"})
			g.Assert(err == nil).IsTrue()
			g.Assert(out.Components).Eql([]Component{})
			g.Assert(out.NextToken).Eql("")
		})
		g.It("Sets nextToken if there are more results to return", func() {
			out, err := svc.ListComponents(ListComponentsInput{Limit: 2})
			g.Assert(err == nil).IsTrue()
			g.Assert(componentNames(out.Components)).Eql([]string{"list-0", "list-1"})
			g.Assert(out.NextToken != "").IsTrue()
			out, err = svc.ListComponents(ListComponentsInput{Limit: 2, NextToken: out.NextToken})
			g.Assert(err == nil).IsTrue()
			g.Assert(componentNames(out.Components)).Eql([]string{"list-2", "list-3"})
			g.Assert(out.NextToken != "").IsTrue()
			out, err = svc.ListComponents(ListComponentsInput{Limit: 4, NextToken: out.NextToken})
			g.Assert(err == nil).IsTrue()
			g.Assert(componentNames(out.Components)).Eql([]string{"list-4", "other1"})
			g.Assert(out.NextToken).Eql("")
		})
	})
}

func TestEventSource(t *testing.T) {
	g := Goblin(t)
	svc, sqlDb := createV1()

	g.Describe("EventSource CRUD", func() {

		// create component we can use as a FK in event sources
		putComponentOK(g, svc, "comp1")

		g.It("Can put, get, remove an event source", func() {
			// put event source
			putIn, _ := putEventSourceOK(g, svc, "ev1", "comp1")

			// get event source and verify equality
			getOut, err := svc.GetEventSource(GetEventSourceInput{Name: "ev1"})
			g.Assert(err == nil).IsTrue(fmt.Sprintf("GetEventSource err: %v", err))
			putIn.EventSource.Version = 1
			putIn.EventSource.ModifiedAt = getOut.EventSource.ModifiedAt
			g.Assert(getOut.EventSource).Eql(putIn.EventSource)

			// remove event source
			rmOut, err := svc.RemoveEventSource(RemoveEventSourceInput{Name: "ev1"})
			g.Assert(err == nil).IsTrue()
			g.Assert(rmOut.Found).IsTrue()

			// get event source - verify a 1003 is raised
			_, err = svc.GetEventSource(GetEventSourceInput{Name: "ev1"})
			g.Assert(err != nil).IsTrue()
			rpcErr, _ := err.(*barrister.JsonRpcError)
			g.Assert(rpcErr.Code).Eql(1003)

			// remove event source again - should no-op and return found=false
			rmOut, err = svc.RemoveEventSource(RemoveEventSourceInput{Name: "ev1"})
			g.Assert(err == nil).IsTrue()
			g.Assert(rmOut.Found).IsFalse()
		})
	})

	g.Describe("PutEventSource Validation", func() {
		g.It("Validates event source name and component name", func() {
			invalid := []string{"", "  ", "\tstuff and space", "bad&chars%here"}
			for _, name := range invalid {
				putEventSourceFailsWithCode(g, svc, 1001, func(input *PutEventSourceInput) {
					input.EventSource.Name = name
				})
				putEventSourceFailsWithCode(g, svc, 1001, func(input *PutEventSourceInput) {
					input.EventSource.ComponentName = name
				})
			}
		})
		g.It("Validates event source sub-type is present", func() {
			putEventSourceFailsWithCode(g, svc, 1001, func(input *PutEventSourceInput) {
				input.EventSource.Http = nil
			})
		})
		g.It("Raises 1003 if componentName is not found", func() {
			putEventSourceFailsWithCode(g, svc, 1003, func(input *PutEventSourceInput) {
				input.EventSource.ComponentName = "valid-but-not-in-db"
			})
		})
		g.It("Raises 1004 if previousVersion is not current", func() {
			putEventSourceFailsWithCode(g, svc, 1004, func(input *PutEventSourceInput) {
				input.EventSource.Version = 50000
			})
		})
	})

	g.Describe("ListEventSources", func() {

		g.Assert(sqlDb.DeleteAll()).Eql(nil)

		// create component we can use as a FK in event sources
		compName := "comp1"
		putComponentOK(g, svc, compName)

		// insert a bunch of event sources with name starting with "list-"
		eventSources := make([]string, 0)
		for i := 0; i < 5; i++ {
			name := fmt.Sprintf("list-%d", i)
			putEventSourceOK(g, svc, name, compName)
			eventSources = append(eventSources, name)
		}

		g.It("Returns components in alphabetical order by name", func() {
			out, err := svc.ListEventSources(ListEventSourcesInput{})
			g.Assert(err == nil).IsTrue()
			g.Assert(len(out.EventSources)).Eql(len(eventSources))
			g.Assert(out.NextToken).Eql("")
			for x, cname := range eventSources {
				g.Assert(out.EventSources[x].Name).Eql(cname)
				g.Assert(out.EventSources[x].Version).Eql(int64(1))
			}
		})
		g.It("Optionally filters by name prefix", func() {
			putEventSourceOK(g, svc, "other1", compName)

			// should get all items
			listEventSourcesOK(g, svc, ListEventSourcesInput{},
				[]string{"list-0", "list-1", "list-2", "list-3", "list-4", "other1"})

			// load with "list" prefix - should get 5
			listEventSourcesOK(g, svc, ListEventSourcesInput{NamePrefix: "list"},
				[]string{"list-0", "list-1", "list-2", "list-3", "list-4"})

			// load with "other" prefix - should get 1
			listEventSourcesOK(g, svc, ListEventSourcesInput{NamePrefix: "other"}, []string{"other1"})

			// no match - empty list
			listEventSourcesOK(g, svc, ListEventSourcesInput{NamePrefix: "zzzz"}, []string{})
		})
		g.It("Optionally filters by component name", func() {
			// matches all
			listEventSourcesOK(g, svc, ListEventSourcesInput{ComponentName: compName},
				[]string{"list-0", "list-1", "list-2", "list-3", "list-4", "other1"})

			// combine namePrefix and compName
			listEventSourcesOK(g, svc,
				ListEventSourcesInput{NamePrefix: "other", ComponentName: compName}, []string{"other1"})

			// no match - empty list
			listEventSourcesOK(g, svc, ListEventSourcesInput{ComponentName: "zzzz"}, []string{})
			listEventSourcesOK(g, svc, ListEventSourcesInput{ComponentName: compName, NamePrefix: "zzz"}, []string{})
		})
		g.It("Optionally filters by event source type", func() {
			listEventSourcesOK(g, svc, ListEventSourcesInput{EventSourceType: EventSourceTypeHttp},
				[]string{"list-0", "list-1", "list-2", "list-3", "list-4", "other1"})

			listEventSourcesOK(g, svc, ListEventSourcesInput{NamePrefix: "other", EventSourceType: EventSourceTypeHttp},
				[]string{"other1"})
		})
		g.It("Returns an empty list if no components exist", func() {
			out := listEventSourcesOK(g, svc, ListEventSourcesInput{NamePrefix: "bogusprefix"}, []string{})
			g.Assert(out.EventSources).Eql([]EventSource{})
			g.Assert(out.NextToken).Eql("")
		})
		g.It("Sets nextToken if there are more results to return", func() {
			out := listEventSourcesOK(g, svc, ListEventSourcesInput{Limit: 2}, []string{"list-0", "list-1"})
			out = listEventSourcesOK(g, svc, ListEventSourcesInput{Limit: 2, NextToken: out.NextToken},
				[]string{"list-2", "list-3"})
			out = listEventSourcesOK(g, svc, ListEventSourcesInput{Limit: 2, NextToken: out.NextToken},
				[]string{"list-4", "other1"})
			g.Assert(out.NextToken).Eql("")
		})
	})

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

func putComponentOK(g *G, svc *V1, name string) PutComponentOutput {
	input := validComponent(name)
	out, err := svc.PutComponent(input)
	g.Assert(err == nil).IsTrue(fmt.Sprintf("PutComponent failed for: %s - %v", name, err))
	g.Assert(out.Name).Eql(input.Component.Name)
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

func putEventSourceFailsWithCode(g *G, svc *V1, errCode int, mutator func(i *PutEventSourceInput)) {
	input := validEventSource("esname", "comp1")
	mutator(&input)
	_, err := svc.PutEventSource(input)
	g.Assert(err != nil).IsTrue(fmt.Sprintf("PutEventSource didn't error for input: %v", input))
	rpcErr, _ := err.(*barrister.JsonRpcError)
	g.Assert(rpcErr.Code).Eql(errCode)
}

func putEventSourceOK(g *G, svc *V1, eventSourceName string,
	componentName string) (PutEventSourceInput, PutEventSourceOutput) {
	input := validEventSource(eventSourceName, componentName)
	out, err := svc.PutEventSource(input)
	g.Assert(err == nil).IsTrue(fmt.Sprintf("PutEventSource failed for: %s - %v", eventSourceName, err))
	g.Assert(out.Name).Eql(eventSourceName)
	return input, out
}

func listEventSourcesOK(g *G, svc *V1, input ListEventSourcesInput,
	expectedEventSourceNames []string) ListEventSourcesOutput {
	out, err := svc.ListEventSources(input)
	g.Assert(err == nil).IsTrue()
	g.Assert(eventSourceNames(out.EventSources)).Eql(expectedEventSourceNames)
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
