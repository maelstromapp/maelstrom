package v1

import "strings"

func PutInputToComponent(input PutComponentInput) Component {
	return Component{
		Name:   strings.TrimSpace(input.Name),
		Docker: &input.Docker,
	}
}
