package maelstrom

import v1 "github.com/coopernurse/maelstrom/pkg/v1"

type ComponentSubscriber interface {
	OnComponentNotification(change v1.DataChangedUnion)
}
