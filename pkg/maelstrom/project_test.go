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

func newEventSource(name string, componentName string, cronSchedule string) v1.EventSourceWithStatus {
	return v1.EventSourceWithStatus{
		EventSource: v1.EventSource{
			Name:          name,
			ComponentName: componentName,
			Cron: &v1.CronEventSource{
				Schedule: cronSchedule,
			},
		},
		Enabled: true,
	}
}

func TestDiffProject(t *testing.T) {
	oldProject := v1.Project{
		Components: []v1.ComponentWithEventSources{
			{
				Component: newComponent("component-a", "image1", "cmd1"),
				EventSources: []v1.EventSourceWithStatus{
					newEventSource("a-es1", "component-a", "*"),
				},
			},
			{
				Component: newComponent("component-b", "image1", "cmd1"),
				EventSources: []v1.EventSourceWithStatus{
					newEventSource("b-es1", "component-b", "*"),
				},
			},
			{
				Component: newComponent("component-c", "image1", "cmd1"),
				EventSources: []v1.EventSourceWithStatus{
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
				EventSources: []v1.EventSourceWithStatus{
					newEventSource("a-es1", "component-a", "* *"),
				},
			},
			{
				Component: newComponent("component-c", "image2", "cmd1"),
				EventSources: []v1.EventSourceWithStatus{
					newEventSource("c-es1", "component-c", "*"),
					newEventSource("c-es3", "component-c", "*"),
				},
			},
			{
				Component:    newComponent("component-d", "image1", "cmd1"),
				EventSources: []v1.EventSourceWithStatus{},
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
			newProject.Components[0].EventSources[0].EventSource,
			newProject.Components[1].EventSources[1].EventSource,
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
    restartorder: startstop
  detector:
    image: coopernurse/object-detector:latest
    command: ["python", "/app/detector.py"]
    startparallelism: series
    volumes:
      - source: ${HOME}/.aws
        target: /root/.aws
    logdriver: syslog
    logdriveroptions:
      syslog-host: localhost
    cpushares: 250
    reservememory: 1024
    limitmemory: 2048
    ulimits:
      - foo:23:55
      - bar:123:456
    crontab: |
       # comment should be ignored
       1 2 * * *   /cron/one
       5   6 */2  ?  9   /cron/two?a=b
       # another comment..
       @every 1h   /cron/three
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
					Name:             "detector",
					ProjectName:      "demo-object-detector",
					StartParallelism: v1.StartParallelismSeries,
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
						Ulimits: []string{
							"foo:23:55",
							"bar:123:456",
						},
					},
				},
				EventSources: []v1.EventSourceWithStatus{
					{
						EventSource: v1.EventSource{
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
						Enabled: true,
					},
					{
						EventSource: v1.EventSource{
							Name:          "detector-cron-0",
							ComponentName: "detector",
							ProjectName:   "demo-object-detector",
							Cron: &v1.CronEventSource{
								Schedule: "1 2 * * *",
								Http: v1.CronHttpRequest{
									Method: "GET",
									Path:   "/cron/one",
								},
							},
						},
						Enabled: true,
					},
					{
						EventSource: v1.EventSource{
							Name:          "detector-cron-1",
							ComponentName: "detector",
							ProjectName:   "demo-object-detector",
							Cron: &v1.CronEventSource{
								Schedule: "5 6 */2 ? 9",
								Http: v1.CronHttpRequest{
									Method: "GET",
									Path:   "/cron/two?a=b",
								},
							},
						},
						Enabled: true,
					},
					{
						EventSource: v1.EventSource{
							Name:          "detector-cron-2",
							ComponentName: "detector",
							ProjectName:   "demo-object-detector",
							Cron: &v1.CronEventSource{
								Schedule: "@every 1h",
								Http: v1.CronHttpRequest{
									Method: "GET",
									Path:   "/cron/three",
								},
							},
						},
						Enabled: true,
					},
				},
			},
			{
				Component: v1.Component{
					Name:         "gizmo",
					ProjectName:  "demo-object-detector",
					RestartOrder: v1.RestartOrderStartstop,
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
				EventSources: []v1.EventSourceWithStatus{},
			},
		},
	}

	project, err := ParseProjectYaml(yaml, true)
	assert.Equal(t, nil, err)
	assert.Equal(t, expected, project)
}
