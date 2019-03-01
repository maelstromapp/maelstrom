package v1

type ComponentSubscriber interface {
	OnComponentNotification(cn ComponentNotification)
}

type ComponentNotification struct {
	PutComponent    *PutComponentOutput
	RemoveComponent *RemoveComponentOutput
}
