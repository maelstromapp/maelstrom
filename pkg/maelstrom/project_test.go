package maelstrom

import (
	"github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/stretchr/testify/assert"
	"testing"
)

func newComponent(name string, image string, command ...string) v1.Component {
	return v1.Component{
		Name: name,
		Docker: &v1.DockerComponent{
			Image:   image,
			Command: command,
		},
	}
}

func newEventSource(name string, componentName string, cronSchedule string) v1.EventSource {
	return v1.EventSource{
		Name:          name,
		ComponentName: componentName,
		Cron: &v1.CronEventSource{
			Schedule: cronSchedule,
		},
	}
}

func TestDiffProject(t *testing.T) {
	oldProject := v1.Project{
		Components: []v1.ComponentWithEventSources{
			{
				Component: newComponent("component-a", "image1", "cmd1"),
				EventSources: []v1.EventSource{
					newEventSource("a-es1", "component-a", "*"),
				},
			},
			{
				Component: newComponent("component-b", "image1", "cmd1"),
				EventSources: []v1.EventSource{
					newEventSource("b-es1", "component-b", "*"),
				},
			},
			{
				Component: newComponent("component-c", "image1", "cmd1"),
				EventSources: []v1.EventSource{
					newEventSource("c-es1", "component-c", "*"),
					newEventSource("c-es2", "component-c", "*"),
				},
			},
		},
	}
	newProject := v1.Project{
		Components: []v1.ComponentWithEventSources{
			{
				Component: newComponent("component-a", "image1", "cmd1"),
				EventSources: []v1.EventSource{
					newEventSource("a-es1", "component-a", "* *"),
				},
			},
			{
				Component: newComponent("component-c", "image2", "cmd1"),
				EventSources: []v1.EventSource{
					newEventSource("c-es1", "component-c", "*"),
					newEventSource("c-es3", "component-c", "*"),
				},
			},
			{
				Component:    newComponent("component-d", "image1", "cmd1"),
				EventSources: []v1.EventSource{},
			},
		},
	}

	expected := ProjectDiff{
		ComponentPut: []v1.Component{
			newProject.Components[1].Component,
			newProject.Components[2].Component,
		},
		ComponentRemove: []string{"component-b"},
		EventSourcePut: []v1.EventSource{
			newProject.Components[0].EventSources[0],
			newProject.Components[1].EventSources[1],
		},
		EventSourceRemove: []string{"b-es1", "c-es2"},
	}

	assert.Equal(t, expected, DiffProject(oldProject, newProject))
}

func TestParseProjectYaml(t *testing.T) {
	yaml := `---
# maelstrom project file - image object detector demo
#
#
name: demo-object-detector
environment:
  - A=a
  - B=$$blah
  - HOME=${HOME}
components:
  gizmo:
    image: coopernurse/gizmo
    command: ["python", "gizmo.py"]
    environment:
      - B=gizmoB
    mininstances: 1
    maxinstances: 5
    maxconcurrency: 3
    maxdurationseconds: 30
  detector:
    image: coopernurse/object-detector:latest
    command: ["python", "/app/detector.py"]
    volumes:
      - source: ${HOME}/.aws
        target: /root/.aws
    logdriver: syslog
    logdriveroptions:
      syslog-host: localhost
    cpushares: 250
    reservememory: 1024
    limitmemory: 2048
    eventsources:
      messages:
        sqs:
          queuename: ${ENV}_images
          nameasprefix: true
          visibilitytimeout: 300
          maxconcurrency: 10
          path: /message
`

	expected := v1.Project{
		Name: "demo-object-detector",
		Components: []v1.ComponentWithEventSources{
			{
				Component: v1.Component{
					Name:        "detector",
					ProjectName: "demo-object-detector",
					Environment: []v1.NameValue{
						{Name: "A", Value: "a"},
						{Name: "B", Value: "$$blah"},
						{Name: "HOME", Value: "${HOME}"},
					},
					Docker: &v1.DockerComponent{
						Image:   "coopernurse/object-detector:latest",
						Command: []string{"python", "/app/detector.py"},
						Volumes: []v1.VolumeMount{
							{Source: "${HOME}/.aws", Target: "/root/.aws"},
						},
						LogDriver: "syslog",
						LogDriverOptions: []v1.NameValue{
							{Name: "syslog-host", Value: "localhost"},
						},
						CpuShares:        250,
						LimitMemoryMiB:   2048,
						ReserveMemoryMiB: 1024,
					},
				},
				EventSources: []v1.EventSource{
					{
						Name:          "messages",
						ComponentName: "detector",
						ProjectName:   "demo-object-detector",
						Sqs: &v1.SqsEventSource{
							QueueName:         "${ENV}_images",
							NameAsPrefix:      true,
							VisibilityTimeout: 300,
							MaxConcurrency:    10,
							Path:              "/message",
						},
					},
				},
			},
			{
				Component: v1.Component{
					Name:        "gizmo",
					ProjectName: "demo-object-detector",
					Environment: []v1.NameValue{
						{Name: "A", Value: "a"},
						{Name: "B", Value: "gizmoB"},
						{Name: "HOME", Value: "${HOME}"},
					},
					Docker: &v1.DockerComponent{
						Image:            "coopernurse/gizmo",
						Command:          []string{"python", "gizmo.py"},
						LogDriverOptions: []v1.NameValue{},
					},
					MinInstances:       1,
					MaxInstances:       5,
					MaxConcurrency:     3,
					MaxDurationSeconds: 30,
				},
				EventSources: []v1.EventSource{},
			},
		},
	}

	project, err := ParseProjectYaml(yaml)
	assert.Equal(t, nil, err)
	assert.Equal(t, expected, project)
}
