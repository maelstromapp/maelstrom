package evsource

import (
	"context"
)

type PollCreator interface {
	NewPoller() Poller
	ComponentName() string
	RoleIdPrefix() string
	MaxConcurrency() int
	MaxConcurrencyPerPoller() int
}

type Poller func(ctx context.Context, concurrency int, roleId string)
