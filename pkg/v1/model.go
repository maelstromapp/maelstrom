package v1

import "strings"

type Component struct {
	Name            string
	DockerComponent DockerComponent
}

func PutInputToComponent(input PutComponentInput) Component {
	return Component{
		Name:            strings.TrimSpace(input.Name),
		DockerComponent: input.Docker,
	}
}
