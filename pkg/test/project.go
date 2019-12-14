package test

import (
	"fmt"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"math/rand"
)

func ValidProject(projectName string) v1.PutProjectInput {
	num := rand.Intn(5) + 1
	components := make([]v1.ComponentWithEventSources, num)
	for i := 0; i < num; i++ {
		compName := fmt.Sprintf("comp-%d", i)
		comp := ValidComponent(compName)
		comp.Component.ProjectName = projectName
		esNum := rand.Intn(5)
		eventSources := make([]v1.EventSourceWithStatus, esNum)
		for x := 0; x < esNum; x++ {
			esName := fmt.Sprintf("es-%d-%d", i, x)
			es := ValidPutEventSourceInput(esName, compName).EventSource
			es.ProjectName = projectName
			eventSources[x] = v1.EventSourceWithStatus{
				EventSource: es,
				Enabled:     true,
			}
		}
		components[i] = v1.ComponentWithEventSources{
			Component:    comp.Component,
			EventSources: eventSources,
		}
	}

	return v1.PutProjectInput{
		Project: v1.Project{
			Name:       projectName,
			Components: components,
		},
	}
}
