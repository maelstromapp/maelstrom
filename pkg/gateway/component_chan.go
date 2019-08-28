package gateway

import (
	log "github.com/mgutz/logxi/v1"
	v1 "gitlab.com/coopernurse/maelstrom/pkg/v1"
	"sync"
	"time"
)

func newComponentChannels(nodeServiceGetter func() v1.NodeService) *componentChannels {
	return &componentChannels{
		localCh:           make(chan *MaelRequest),
		allCh:             make(chan *MaelRequest),
		nodeServiceGetter: nodeServiceGetter,
		lock:              &sync.Mutex{},
	}
}

type componentChannels struct {
	localCh               chan *MaelRequest
	allCh                 chan *MaelRequest
	nodeServiceGetter     func() v1.NodeService
	lastConsumerHeartbeat time.Time
	nextProvisionAt       time.Time
	lock                  *sync.Mutex
}

func (c *componentChannels) destroy() {
	close(c.allCh)
	close(c.localCh)
}

func (c *componentChannels) getLastConsumerHeartbeat() time.Time {
	c.lock.Lock()
	t := c.lastConsumerHeartbeat
	c.lock.Unlock()
	return t
}

func (c *componentChannels) consumerHeartbeat() {
	c.lock.Lock()
	c.lastConsumerHeartbeat = time.Now()
	c.nextProvisionAt = c.lastConsumerHeartbeat.Add(6 * time.Second)
	c.lock.Unlock()
}

func (c *componentChannels) shouldProvision() bool {
	c.lock.Lock()
	provision := time.Now().After(c.nextProvisionAt)
	if provision {
		// bump time to avoid calling provision more than once every minute
		c.nextProvisionAt = time.Now().Add(time.Minute)
	}
	c.lock.Unlock()
	return provision
}

func (c *componentChannels) maybeProvision(mr *MaelRequest) {
	if c.shouldProvision() {
		_, err := c.nodeServiceGetter().PlaceComponent(v1.PlaceComponentInput{ComponentName: mr.comp.Name})
		if err != nil {
			log.Error("component_chan: unable to place component", "err", err, "component", mr.comp.Name)
		}
	}
}

func (c *componentChannels) send(mr *MaelRequest) {

	// if no active consumers, tell NodeService to provision a container
	c.maybeProvision(mr)

	sent := false

	// this header is set by cluster peers when relaying, suggesting they intend for the request to terminate here
	preferLocal := mr.req.Header.Get("MAELSTROM-RELAY-PATH") != ""

	if preferLocal {
		select {
		// try to send to local channel first
		case c.localCh <- mr:
			c.consumerHeartbeat()
			sent = true
		// unable to send to local immediately - put on 'all' queue so any remote/local consumer can take it
		default:
		}
	}

	if !sent {
		provisionTicker := time.Tick(10 * time.Second)
		for {
			select {
			// try to send to any consumer
			case c.allCh <- mr:
				c.consumerHeartbeat()
				return
			case <-provisionTicker:
				c.maybeProvision(mr)
			// or bail if deadline reached
			case <-mr.ctx.Done():
				return
			}
		}
	}
}
