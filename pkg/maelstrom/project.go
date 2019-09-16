package maelstrom

import (
	"fmt"
	"github.com/go-yaml/yaml"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"gitlab.com/coopernurse/maelstrom/pkg/v1"
	"io/ioutil"
	"reflect"
	"sort"
	"strings"
)

func PutProjectOutputEmpty(out v1.PutProjectOutput) bool {
	return len(out.EventSourcesRemoved) == 0 && len(out.EventSourcesAdded) == 0 && len(out.EventSourcesUpdated) == 0 &&
		len(out.ComponentsRemoved) == 0 && len(out.ComponentsAdded) == 0 && len(out.ComponentsUpdated) == 0
}

type ProjectDiff struct {
	ComponentPut      []v1.Component
	ComponentRemove   []string
	EventSourcePut    []v1.EventSource
	EventSourceRemove []string
}

func DiffProject(oldProject v1.Project, newProject v1.Project) ProjectDiff {
	compPut := make([]v1.Component, 0)
	compRemove := make([]string, 0)
	esPut := make([]v1.EventSource, 0)
	esRemove := make([]string, 0)

	oldCompByName := make(map[string]v1.Component)
	newCompByName := make(map[string]v1.Component)
	oldESByName := make(map[string]v1.EventSource)
	newESByName := make(map[string]v1.EventSource)

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
			c.Component.Version = oldComp.Version
			c.Component.ModifiedAt = oldComp.ModifiedAt

			// put component if modified
			if !reflect.DeepEqual(oldComp, c.Component) {
				compPut = append(compPut, c.Component)
			}

			for _, newES := range c.EventSources {
				oldES, ok := oldESByName[newES.Name]
				newES.Version = oldES.Version
				newES.ModifiedAt = oldES.ModifiedAt
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

func ParseYamlFileAndInterpolateEnv(fname string) (v1.Project, error) {
	data, err := ioutil.ReadFile(fname)
	if err != nil {
		return v1.Project{}, err
	}

	envVars := common.EnvVarMap()
	converted := common.InterpolateWithMap(string(data), envVars)
	return ParseProjectYaml(converted)
}

func ParseProjectYaml(yamlStr string) (v1.Project, error) {
	var yamlProj yamlProject
	err := yaml.Unmarshal([]byte(yamlStr), &yamlProj)
	if err != nil {
		return v1.Project{}, err
	}
	return yamlProj.toProject()
}

func mapToNameValues(m map[string]string) []v1.NameValue {
	nvs := make([]v1.NameValue, len(m))
	i := 0
	for k, v := range m {
		nvs[i] = v1.NameValue{Name: k, Value: v}
		i++
	}
	return nvs
}

type yamlComponent struct {
	Image                       string
	Command                     []string
	Environment                 []string
	MinInstances                int64
	MaxInstances                int64
	MaxConcurrency              int64
	ScaleDownConcurrencyPct     float64
	ScaleUpConcurrencyPct       float64
	MaxDurationSeconds          int64
	HttpPort                    int64
	HttpHealthCheckPath         string
	HttpStartHealthCheckSeconds int64
	HttpHealthCheckSeconds      int64
	IdleTimeoutSeconds          int64
	Volumes                     []v1.VolumeMount
	NetworkName                 string
	LogDriver                   string
	LogDriverOptions            map[string]string
	EventSources                map[string]v1.EventSource
	LimitCpu                    float64
	ReserveMemory               int64
	LimitMemory                 int64
}

func (c yamlComponent) toComponentWithEventSources(name string, projectName string,
	projectEnv map[string]string) v1.ComponentWithEventSources {

	// copy project env vars first
	envVars := make(map[string]string)
	for k, v := range projectEnv {
		envVars[k] = v
	}
	// merge in env vars from component, overriding any overlapping project env vars
	for k, v := range common.ParseEnvVarMap(c.Environment) {
		envVars[k] = v
	}
	environment := make([]v1.NameValue, 0)
	for k, v := range envVars {
		environment = append(environment, v1.NameValue{Name: k, Value: v})
	}
	sort.Sort(nameValueByName(environment))

	eventSources := make([]v1.EventSource, 0)
	for evName, ev := range c.EventSources {
		ev.Name = evName
		ev.ComponentName = name
		ev.ProjectName = projectName
		eventSources = append(eventSources, ev)
	}
	return v1.ComponentWithEventSources{
		Component: v1.Component{
			Name:                    name,
			ProjectName:             projectName,
			Environment:             environment,
			MinInstances:            c.MinInstances,
			MaxInstances:            c.MaxInstances,
			MaxConcurrency:          c.MaxConcurrency,
			MaxDurationSeconds:      c.MaxDurationSeconds,
			ScaleDownConcurrencyPct: c.ScaleDownConcurrencyPct,
			ScaleUpConcurrencyPct:   c.ScaleUpConcurrencyPct,
			Docker: &v1.DockerComponent{
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
				ReserveMemoryMiB:            c.ReserveMemory,
				LimitMemoryMiB:              c.LimitMemory,
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

func (p yamlProject) toProject() (v1.Project, error) {
	envVars := make(map[string]string)
	if p.EnvFile != "" {
		envFile, err := ioutil.ReadFile(p.EnvFile)
		if err != nil {
			return v1.Project{}, fmt.Errorf("project: unable to read env file: %s - %v", p.EnvFile, err)
		}
		envVars = common.ParseEnvVarMap(strings.Split(string(envFile), "\n"))
	}

	for k, v := range common.ParseEnvVarMap(p.Environment) {
		envVars[k] = v
	}

	components := make([]v1.ComponentWithEventSources, 0)
	for name, c := range p.Components {
		components = append(components, c.toComponentWithEventSources(name, p.Name, envVars))
	}
	sort.Sort(componentWithEventSourcesByName(components))

	return v1.Project{
		Name:       p.Name,
		Components: components,
	}, nil
}
