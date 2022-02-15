package maelstrom

import (
	"fmt"
	"io/ioutil"
	"reflect"
	"sort"
	"strings"

	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/go-yaml/yaml"
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
	oldESByName := make(map[string]v1.EventSourceWithStatus)
	newESByName := make(map[string]v1.EventSourceWithStatus)

	for _, c := range oldProject.Components {
		oldCompByName[c.Component.Name] = c.Component
		for _, ess := range c.EventSources {
			oldESByName[ess.EventSource.Name] = ess
		}
	}

	for _, c := range newProject.Components {
		newCompByName[c.Component.Name] = c.Component
		for _, ess := range c.EventSources {
			newESByName[ess.EventSource.Name] = ess
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
				oldES, ok := oldESByName[newES.EventSource.Name]
				newES.EventSource.Version = oldES.EventSource.Version
				newES.EventSource.ModifiedAt = oldES.EventSource.ModifiedAt

				// put any event source not in old, or which is modified
				if !ok || !reflect.DeepEqual(oldES, newES) {
					esPut = append(esPut, newES.EventSource)
				}
			}
		} else {
			// in new, not old -- add to put
			compPut = append(compPut, c.Component)
			// put all event sources for this component
			for _, es := range c.EventSources {
				esPut = append(esPut, es.EventSource)
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

func ParseYamlFileAndInterpolateEnv(fname string, strict bool) (v1.Project, error) {
	data, err := ioutil.ReadFile(fname)
	if err != nil {
		return v1.Project{}, err
	}

	envVars := common.EnvVarMap()
	converted := common.InterpolateWithMap(string(data), envVars)
	return ParseProjectYaml(converted, strict)
}

func ParseProjectYaml(yamlStr string, strict bool) (v1.Project, error) {
	var yamlProj yamlProject
	var err error
	if strict {
		err = yaml.UnmarshalStrict([]byte(yamlStr), &yamlProj)
	} else {
		err = yaml.Unmarshal([]byte(yamlStr), &yamlProj)
	}
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
	sort.Sort(nameValueByName(nvs))
	return nvs
}

type yamlComponent struct {
	Image                       string
	Command                     []string
	Entrypoint                  []string
	Environment                 []string
	MinInstances                int64
	MaxInstances                int64
	MaxInstancesPerNode         int64
	MaxConcurrency              int64
	SoftConcurrencyLimit        bool
	ScaleDownConcurrencyPct     float64
	ScaleUpConcurrencyPct       float64
	MaxDurationSeconds          int64
	HttpPort                    int64
	HttpHealthCheckPath         string
	HttpStartHealthCheckSeconds int64
	HttpHealthCheckSeconds      int64
	HttpHealthCheckMaxFailures  int64
	HealthCheckFailedCommand    []string
	IdleTimeoutSeconds          int64
	Ports                       []string
	Volumes                     []v1.VolumeMount
	NetworkName                 string
	Hostname                    string
	Domainname                  string
	User                        string
	DNS                         []string
	DNSOptions                  []string
	DNSSearch                   []string
	LogDriver                   string
	LogDriverOptions            map[string]string
	EventSources                map[string]v1.EventSource
	Crontab                     string
	CpuShares                   int64
	ReserveMemory               int64
	LimitMemory                 int64
	ContainerInit               bool
	PullCommand                 []string
	PullUsername                string
	PullPassword                string
	PullImageOnPut              bool
	PullImageOnStart            bool
	Ulimits                     []string
	Capadd                      []string
	Capdrop                     []string
	StartParallelism            string
	RestartOrder                string
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

	eventSources := make([]v1.EventSourceWithStatus, 0)
	for evName, ev := range c.EventSources {
		ev.Name = evName
		ev.ComponentName = name
		ev.ProjectName = projectName
		eventSources = append(eventSources, v1.EventSourceWithStatus{
			EventSource: ev,
			Enabled:     true,
		})
	}

	if c.Crontab != "" {
		lines := strings.Split(c.Crontab, "\n")
		counter := 0
		for _, line := range lines {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "#") {
				cols := strings.Fields(line)
				if len(cols) > 1 && strings.HasPrefix(cols[len(cols)-1], "/") {
					ev := v1.EventSource{
						Name:          fmt.Sprintf("%s-cron-%d", name, counter),
						ComponentName: name,
						ProjectName:   projectName,
						Cron: &v1.CronEventSource{
							Schedule: strings.Join(cols[0:len(cols)-1], " "),
							Http: v1.CronHttpRequest{
								Method: "GET",
								Path:   cols[len(cols)-1],
							},
						},
					}
					eventSources = append(eventSources, v1.EventSourceWithStatus{
						EventSource: ev,
						Enabled:     true,
					})
					counter++
				}
			}
		}
	}

	return v1.ComponentWithEventSources{
		Component: v1.Component{
			Name:                    name,
			ProjectName:             projectName,
			Environment:             environment,
			MinInstances:            c.MinInstances,
			MaxInstances:            c.MaxInstances,
			MaxInstancesPerNode:     c.MaxInstancesPerNode,
			MaxConcurrency:          c.MaxConcurrency,
			SoftConcurrencyLimit:    c.SoftConcurrencyLimit,
			MaxDurationSeconds:      c.MaxDurationSeconds,
			ScaleDownConcurrencyPct: c.ScaleDownConcurrencyPct,
			ScaleUpConcurrencyPct:   c.ScaleUpConcurrencyPct,
			StartParallelism:        v1.StartParallelism(c.StartParallelism),
			RestartOrder:            v1.RestartOrder(c.RestartOrder),
			Docker: &v1.DockerComponent{
				Image:                       c.Image,
				Command:                     c.Command,
				Entrypoint:                  c.Entrypoint,
				HttpPort:                    c.HttpPort,
				HttpHealthCheckPath:         c.HttpHealthCheckPath,
				HttpStartHealthCheckSeconds: c.HttpStartHealthCheckSeconds,
				HttpHealthCheckSeconds:      c.HttpHealthCheckSeconds,
				HttpHealthCheckMaxFailures:  c.HttpHealthCheckMaxFailures,
				HealthCheckFailedCommand:    c.HealthCheckFailedCommand,
				IdleTimeoutSeconds:          c.IdleTimeoutSeconds,
				Ports:                       c.Ports,
				Volumes:                     c.Volumes,
				NetworkName:                 c.NetworkName,
				LogDriver:                   c.LogDriver,
				LogDriverOptions:            mapToNameValues(c.LogDriverOptions),
				CpuShares:                   c.CpuShares,
				ReserveMemoryMiB:            c.ReserveMemory,
				LimitMemoryMiB:              c.LimitMemory,
				PullCommand:                 c.PullCommand,
				PullUsername:                c.PullUsername,
				PullPassword:                c.PullPassword,
				PullImageOnPut:              c.PullImageOnPut,
				PullImageOnStart:            c.PullImageOnStart,
				Dns:                         c.DNS,
				DnsOptions:                  c.DNSOptions,
				DnsSearch:                   c.DNSSearch,
				Ulimits:                     c.Ulimits,
				Init:                        c.ContainerInit,
				Hostname:                    c.Hostname,
				Domainname:                  c.Domainname,
				User:                        c.User,
				Capadd:                      c.Capadd,
				Capdrop:                     c.Capdrop,
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
