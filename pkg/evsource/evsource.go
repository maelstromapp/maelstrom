package evsource

import (
	"context"
	"sync"
)

type PollCreator interface {
	NewPoller() Poller
	ComponentName() string
	RoleIdPrefix() string
	MaxConcurrency() int
	MaxConcurrencyPerPoller() int
}

type Poller func(ctx context.Context, wg *sync.WaitGroup, concurrency int, roleId string)
