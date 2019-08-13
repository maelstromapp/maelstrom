package v1

import (
	"fmt"
	"github.com/go-yaml/yaml"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"io/ioutil"
	"reflect"
	"sort"
	"strings"
)

func PutProjectOutputEmpty(out PutProjectOutput) bool {
	return len(out.EventSourcesRemoved) == 0 && len(out.EventSourcesAdded) == 0 && len(out.EventSourcesUpdated) == 0 &&
		len(out.ComponentsRemoved) == 0 && len(out.ComponentsAdded) == 0 && len(out.ComponentsUpdated) == 0
}

type ProjectDiff struct {
	ComponentPut      []Component
	ComponentRemove   []string
	EventSourcePut    []EventSource
	EventSourceRemove []string
}

func DiffProject(oldProject Project, newProject Project) ProjectDiff {
	compPut := make([]Component, 0)
	compRemove := make([]string, 0)
	esPut := make([]EventSource, 0)
	esRemove := make([]string, 0)

	oldCompByName := make(map[string]Component)
	newCompByName := make(map[string]Component)
	oldESByName := make(map[string]EventSource)
	newESByName := make(map[string]EventSource)

	for _, c := range oldProject.Components {
		oldCompByName[c.Component.Name] = c.Component
		for _, es := range c.EventSources {
			oldESByName[es.Name] = es
		}
	}

	for _, c := range newProject.Components {
		newCompByName[c.Component.Name] = c.Component
		for _, es := range c.EventSources {
			newESByName[es.Name] = es
		}

		oldComp, ok := oldCompByName[c.Component.Name]
		if ok {
			oldComp.Version = c.Component.Version
			oldComp.ModifiedAt = c.Component.ModifiedAt

			// put component if modified
			if !reflect.DeepEqual(oldComp, c.Component) {
				compPut = append(compPut, c.Component)
			}

			for _, newES := range c.EventSources {
				oldES, ok := oldESByName[newES.Name]
				oldES.Version = newES.Version
				oldES.ModifiedAt = newES.ModifiedAt
				// put any event source not in old, or which is modified
				if !ok || !reflect.DeepEqual(oldES, newES) {
					esPut = append(esPut, newES)
				}
			}
		} else {
			// in new, not old -- add to put
			compPut = append(compPut, c.Component)
			// put all event sources for this component
			for _, es := range c.EventSources {
				esPut = append(esPut, es)
			}
		}
	}

	// find components in old, not in new and delete them
	for cname, _ := range oldCompByName {
		_, ok := newCompByName[cname]
		if !ok {
			compRemove = append(compRemove, cname)
		}
	}

	// find event sources in old, not in new and delete them
	for esname, _ := range oldESByName {
		_, ok := newESByName[esname]
		if !ok {
			esRemove = append(esRemove, esname)
		}
	}

	sort.Strings(compRemove)
	sort.Strings(esRemove)

	return ProjectDiff{
		ComponentPut:      compPut,
		ComponentRemove:   compRemove,
		EventSourcePut:    esPut,
		EventSourceRemove: esRemove,
	}
}

func ParseYamlFileAndInterpolateEnv(fname string) (Project, error) {
	data, err := ioutil.ReadFile(fname)
	if err != nil {
		return Project{}, err
	}

	envVars := common.EnvVarMap()
	converted := common.InterpolateWithMap(string(data), envVars)
	return ParseProjectYaml(converted)
}

func ParseProjectYaml(yamlStr string) (Project, error) {
	var yamlProj yamlProject
	err := yaml.Unmarshal([]byte(yamlStr), &yamlProj)
	if err != nil {
		return Project{}, err
	}
	return yamlProj.toProject()
}

func mapToNameValues(m map[string]string) []NameValue {
	nvs := make([]NameValue, len(m))
	i := 0
	for k, v := range m {
		nvs[i] = NameValue{Name: k, Value: v}
		i++
	}
	return nvs
}

type yamlComponent struct {
	Image                       string
	Command                     string
	Environment                 []string
	HttpPort                    int64
	HttpHealthCheckPath         string
	HttpStartHealthCheckSeconds int64
	HttpHealthCheckSeconds      int64
	IdleTimeoutSeconds          int64
	Volumes                     []VolumeMount
	NetworkName                 string
	LogDriver                   string
	LogDriverOptions            map[string]string
	EventSources                map[string]EventSource
	LimitCpu                    float64
	LimitMemory                 string
}

func (c yamlComponent) toComponentWithEventSources(name string, projectName string,
	projectEnv map[string]string) ComponentWithEventSources {

	// copy project env vars first
	envVars := make(map[string]string)
	for k, v := range projectEnv {
		envVars[k] = v
	}
	// merge in env vars from component, overriding any overlapping project env vars
	for k, v := range common.ParseEnvVarMap(c.Environment) {
		envVars[k] = v
	}
	environment := make([]NameValue, 0)
	for k, v := range envVars {
		environment = append(environment, NameValue{Name: k, Value: v})
	}
	sort.Sort(nameValueByName(environment))

	eventSources := make([]EventSource, 0)
	for evName, ev := range c.EventSources {
		ev.Name = evName
		ev.ComponentName = name
		ev.ProjectName = projectName
		eventSources = append(eventSources, ev)
	}
	return ComponentWithEventSources{
		Component: Component{
			Name:        name,
			ProjectName: projectName,
			Environment: environment,
			Docker: &DockerComponent{
				Image:                       c.Image,
				Command:                     c.Command,
				HttpPort:                    c.HttpPort,
				HttpHealthCheckPath:         c.HttpHealthCheckPath,
				HttpStartHealthCheckSeconds: c.HttpStartHealthCheckSeconds,
				HttpHealthCheckSeconds:      c.HttpHealthCheckSeconds,
				IdleTimeoutSeconds:          c.IdleTimeoutSeconds,
				Volumes:                     c.Volumes,
				NetworkName:                 c.NetworkName,
				LogDriver:                   c.LogDriver,
				LogDriverOptions:            mapToNameValues(c.LogDriverOptions),
				LimitCpu:                    c.LimitCpu,
				LimitMemory:                 c.LimitMemory,
			},
		},
		EventSources: eventSources,
	}
}

type yamlProject struct {
	Name        string
	Environment []string
	EnvFile     string
	Components  map[string]yamlComponent
}

func (p yamlProject) toProject() (Project, error) {
	envVars := make(map[string]string)
	if p.EnvFile != "" {
		envFile, err := ioutil.ReadFile(p.EnvFile)
		if err != nil {
			return Project{}, fmt.Errorf("project: unable to read env file: %s - %v", p.EnvFile, err)
		}
		envVars = common.ParseEnvVarMap(strings.Split(string(envFile), "\n"))
	}

	for k, v := range common.ParseEnvVarMap(p.Environment) {
		envVars[k] = v
	}

	components := make([]ComponentWithEventSources, 0)
	for name, c := range p.Components {
		components = append(components, c.toComponentWithEventSources(name, p.Name, envVars))
	}
	sort.Sort(componentWithEventSourcesByName(components))

	return Project{
		Name:       p.Name,
		Components: components,
	}, nil
}
