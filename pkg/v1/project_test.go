package v1

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

func newComponent(name string, image string, command ...string) Component {
	return Component{
		Name: name,
		Docker: &DockerComponent{
			Image:   image,
			Command: command,
		},
	}
}

func newEventSource(name string, componentName string, cronSchedule string) EventSource {
	return EventSource{
		Name:          name,
		ComponentName: componentName,
		Cron: &CronEventSource{
			Schedule: cronSchedule,
		},
	}
}

func TestDiffProject(t *testing.T) {
	oldProject := Project{
		Components: []ComponentWithEventSources{
			{
				Component: newComponent("component-a", "image1", "cmd1"),
				EventSources: []EventSource{
					newEventSource("a-es1", "component-a", "*"),
				},
			},
			{
				Component: newComponent("component-b", "image1", "cmd1"),
				EventSources: []EventSource{
					newEventSource("b-es1", "component-b", "*"),
				},
			},
			{
				Component: newComponent("component-c", "image1", "cmd1"),
				EventSources: []EventSource{
					newEventSource("c-es1", "component-c", "*"),
					newEventSource("c-es2", "component-c", "*"),
				},
			},
		},
	}
	newProject := Project{
		Components: []ComponentWithEventSources{
			{
				Component: newComponent("component-a", "image1", "cmd1"),
				EventSources: []EventSource{
					newEventSource("a-es1", "component-a", "* *"),
				},
			},
			{
				Component: newComponent("component-c", "image2", "cmd1"),
				EventSources: []EventSource{
					newEventSource("c-es1", "component-c", "*"),
					newEventSource("c-es3", "component-c", "*"),
				},
			},
			{
				Component:    newComponent("component-d", "image1", "cmd1"),
				EventSources: []EventSource{},
			},
		},
	}

	expected := ProjectDiff{
		ComponentPut: []Component{
			newProject.Components[1].Component,
			newProject.Components[2].Component,
		},
		ComponentRemove: []string{"component-b"},
		EventSourcePut: []EventSource{
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
    limitcpu: 1.5
    reservememory: 1024
    limitmemory: 2048
    eventsources:
      messages:
        sqs:
          queuenameprefix: ${ENV}_images
          visibilitytimeout: 300
          maxconcurrency: 10
          path: /message
`

	expected := Project{
		Name: "demo-object-detector",
		Components: []ComponentWithEventSources{
			{
				Component: Component{
					Name:        "detector",
					ProjectName: "demo-object-detector",
					Environment: []NameValue{
						{Name: "A", Value: "a"},
						{Name: "B", Value: "$$blah"},
						{Name: "HOME", Value: "${HOME}"},
					},
					Docker: &DockerComponent{
						Image:   "coopernurse/object-detector:latest",
						Command: []string{"python", "/app/detector.py"},
						Volumes: []VolumeMount{
							{Source: "${HOME}/.aws", Target: "/root/.aws"},
						},
						LogDriver: "syslog",
						LogDriverOptions: []NameValue{
							{Name: "syslog-host", Value: "localhost"},
						},
						LimitCpu:         1.5,
						LimitMemoryMiB:   2048,
						ReserveMemoryMiB: 1024,
					},
				},
				EventSources: []EventSource{
					{
						Name:          "messages",
						ComponentName: "detector",
						ProjectName:   "demo-object-detector",
						Sqs: &SqsEventSource{
							QueueNamePrefix:   "${ENV}_images",
							VisibilityTimeout: 300,
							MaxConcurrency:    10,
							Path:              "/message",
						},
					},
				},
			},
			{
				Component: Component{
					Name:        "gizmo",
					ProjectName: "demo-object-detector",
					Environment: []NameValue{
						{Name: "A", Value: "a"},
						{Name: "B", Value: "gizmoB"},
						{Name: "HOME", Value: "${HOME}"},
					},
					Docker: &DockerComponent{
						Image:            "coopernurse/gizmo",
						Command:          []string{"python", "gizmo.py"},
						LogDriverOptions: []NameValue{},
					},
					MinInstances:       1,
					MaxInstances:       5,
					MaxConcurrency:     3,
					MaxDurationSeconds: 30,
				},
				EventSources: []EventSource{},
			},
		},
	}

	project, err := ParseProjectYaml(yaml)
	assert.Equal(t, nil, err)
	assert.Equal(t, expected, project)
}
