package maelstrom

import (
	"github.com/coopernurse/maelstrom/pkg/db"
	"github.com/coopernurse/maelstrom/pkg/maelstrom/component"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	docker "github.com/docker/docker/client"
	log "github.com/mgutz/logxi/v1"
)

func NewImagePuller(dockerClient *docker.Client, db db.Db, pullState *component.PullState) *ImagePuller {
	return &ImagePuller{
		dockerClient: dockerClient,
		db:           db,
		pullState:    pullState,
	}
}

type ImagePuller struct {
	dockerClient *docker.Client
	db           db.Db
	pullState    *component.PullState
}

func (i *ImagePuller) OnComponentNotification(change v1.DataChangedUnion) {
	if change.PutComponent != nil {
		comp, err := i.db.GetComponent(change.PutComponent.Name)
		if err == nil {
			if comp.Docker.PullImageOnPut {
				i.pullState.Pull(comp, false)
			}
		} else {
			log.Error("puller: unable to GetComponent", "err", err, "component", change.PutComponent.Name)
		}
	}
}
