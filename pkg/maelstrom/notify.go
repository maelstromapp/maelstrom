package maelstrom

import "gitlab.com/coopernurse/maelstrom/pkg/v1"

type ComponentSubscriber interface {
	OnComponentNotification(cn ComponentNotification)
}

type ComponentNotification struct {
	PutComponent    *v1.PutComponentOutput
	RemoveComponent *v1.RemoveComponentOutput
}
