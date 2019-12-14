package test

import v1 "github.com/coopernurse/maelstrom/pkg/v1"

func ValidComponent(componentName string) v1.PutComponentInput {
	return v1.PutComponentInput{
		Component: v1.Component{
			Name: componentName,
			Docker: &v1.DockerComponent{
				Image:    "coopernurse/foo",
				HttpPort: 8080,
			},
		},
	}
}

func SanitizeComponentsWithEventSources(list []v1.ComponentWithEventSources) {
	for i, ces := range list {
		list[i].Component = SanitizeComponent(&ces.Component)
		for x, es := range ces.EventSources {
			ces.EventSources[x] = SanitizeEventSource(&es)
		}
	}
}

func SanitizeComponent(c *v1.Component) v1.Component {
	c.Version = 0
	c.ModifiedAt = 0
	return *c
}
