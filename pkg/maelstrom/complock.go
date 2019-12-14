package maelstrom

import (
	"context"
	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/maelstrom/component"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/pkg/errors"
	"time"
)

type CompLocker struct {
	db     db.Db
	nodeId string
}

func NewCompLocker(db db.Db, nodeId string) *CompLocker {
	return &CompLocker{
		db:     db,
		nodeId: nodeId,
	}
}

func (c *CompLocker) startLockAcquire(ctx context.Context, comp *v1.Component) (bool, error) {
	if comp.StartParallelism == v1.StartParallelismParallel {
		// no lock required
		return false, nil
	}

	roleId := compLockerRoleId(comp)
	lockDur := time.Second * time.Duration(healthCheckSeconds(comp.Docker)+1)
	for {

		if comp.StartParallelism == v1.StartParallelismSeriesfirst {
			deployCount, err := c.db.GetComponentDeployCount(comp.Name, comp.Version)
			if err != nil {
				return false, errors.Wrap(err, "complock: unable to get component deploy count")
			}
			if deployCount > 0 {
				// no lock required
				return false, nil
			}
		}

		// try to lock
		ok, _, err := c.db.AcquireOrRenewRole(roleId, c.nodeId, lockDur)
		if err != nil {
			return false, err
		}
		if ok {
			// lock acquired
			return true, nil
		}

		// wait to retry, aborting if context canceled
		ticker := time.NewTicker(5 * time.Second)
		select {
		case <-ctx.Done():
			return false, component.ErrConvergeContextCanceled
		case <-ticker.C:
			// try again
		}
	}
}

func (c *CompLocker) postStartContainer(comp *v1.Component, releaseLock bool, success bool) error {
	if success {
		err := c.db.IncrementComponentDeployCount(comp.Name, comp.Version)
		if err != nil {
			return err
		}
	}
	if releaseLock {
		err := c.db.ReleaseRole(compLockerRoleId(comp), c.nodeId)
		if err != nil {
			return err
		}
	}
	return nil
}

func compLockerRoleId(comp *v1.Component) string {
	return "start_container_" + comp.Name
}
