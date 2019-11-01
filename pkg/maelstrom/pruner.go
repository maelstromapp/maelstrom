package maelstrom

import (
	"context"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"github.com/docker/go-units"
	log "github.com/mgutz/logxi/v1"
	"strings"
	"sync"
	"time"
)

func NewDockerPruner(dockerClient *docker.Client, db Db, ctx context.Context,
	pruneUnregistered bool, pruneKeep []string) *DockerPruner {
	return &DockerPruner{
		dockerClient:      dockerClient,
		db:                db,
		ctx:               ctx,
		pruneUnregistered: pruneUnregistered,
		pruneKeep:         pruneKeep,
	}
}

type DockerPruner struct {
	dockerClient      *docker.Client
	db                Db
	ctx               context.Context
	pruneUnregistered bool
	pruneKeep         []string
}

func (d *DockerPruner) Run(interval time.Duration, wg *sync.WaitGroup) {
	log.Info("maelstrom: docker pruner loop staring", "interval", interval.String())
	defer wg.Done()
	ticker := time.Tick(interval)
	for {
		select {
		case <-ticker:
			d.runOnce()
		case <-d.ctx.Done():
			log.Info("maelstrom: docker pruner loop shutdown gracefully")
			return
		}
	}
}

func (d *DockerPruner) runOnce() {
	if d.pruneUnregistered {
		toDelete, err := d.imageIdsToDelete()
		if err == nil {
			// Delete images not in use and not registered against a component
			for _, id := range toDelete {
				_, err = d.dockerClient.ImageRemove(d.ctx, id, types.ImageRemoveOptions{})
				if err != nil {
					log.Error("maelstrom: unable to remove image", "image", id, "err", err)
				} else {
					log.Info("maelstrom: pruner removed image", "image", id)
				}
			}
		} else {
			log.Error("maelstrom: unable to load image ids to delete", "err", err)
		}
	}

	// Docker prune containers and images
	_, err := d.dockerClient.ContainersPrune(d.ctx, filters.Args{})
	if err != nil {
		log.Error("maelstrom: unable to prune containers", "err", err)
	}
	imagePruneReport, err := d.dockerClient.ImagesPrune(d.ctx, filters.Args{})
	if err != nil {
		log.Error("maelstrom: unable to prune images", "err", err)
	} else {
		log.Info("maelstrom: pruner reclaimed space", "size",
			units.HumanSize(float64(imagePruneReport.SpaceReclaimed)))
	}
}

func (d *DockerPruner) imageIdsToDelete() ([]string, error) {
	imageNames, err := d.componentImages()
	if err != nil {
		return nil, err
	}
	imageNameMap := make(map[string]bool)
	for _, name := range imageNames {
		imageNameMap[name] = true
	}

	containers, err := d.dockerClient.ContainerList(d.ctx, types.ContainerListOptions{
		All: false,
	})
	if err != nil {
		return nil, err
	}

	runningImageIds := make(map[string]bool)
	for _, cont := range containers {
		runningImageIds[cont.ImageID] = true
	}

	toDelete := make([]string, 0)
	images, err := d.dockerClient.ImageList(d.ctx, types.ImageListOptions{})
	if err == nil {
		for _, img := range images {
			if !runningImageIds[img.ID] {
				deleteImg := true
				for _, tag := range img.RepoTags {
					if d.keepImage(tag, imageNameMap) {
						deleteImg = false
					}
				}
				if deleteImg {
					toDelete = append(toDelete, img.ID)
				}
			}
		}
		return toDelete, nil
	} else {
		return nil, err
	}
}

func (d *DockerPruner) componentImages() ([]string, error) {
	nextToken := ""
	imageNames := make([]string, 0)
	for {
		out, err := d.db.ListComponents(v1.ListComponentsInput{
			Limit:     1000,
			NextToken: nextToken,
		})

		if err == nil {
			for _, c := range out.Components {
				imageNames = append(imageNames, c.Docker.Image)
			}
		} else {
			return nil, err
		}

		nextToken = out.NextToken
		if nextToken == "" {
			return imageNames, nil
		}
	}
}

func (d *DockerPruner) keepImage(tag string, imageNameMap map[string]bool) bool {
	if imageNameMap[tag] {
		return true
	}
	for _, keepRule := range d.pruneKeep {
		if common.GlobMatches(strings.TrimSpace(keepRule), tag) {
			return true
		}
	}
	return false
}
