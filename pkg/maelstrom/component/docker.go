package component

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/coopernurse/maelstrom/pkg/common"
	v1 "github.com/coopernurse/maelstrom/pkg/v1"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"strings"
)

func pullImage(dockerClient *docker.Client, c v1.Component) error {
	if len(c.Docker.PullCommand) == 0 {
		// normal pull
		authStr, err := pullImageAuth(c)
		if err != nil {
			return err
		}
		out, err := dockerClient.ImagePull(context.Background(), c.Docker.Image,
			types.ImagePullOptions{RegistryAuth: authStr})
		if err == nil {
			defer common.CheckClose(out, &err)
			_, err = ioutil.ReadAll(out)
		}
		return err
	} else {
		// custom pull

		// rewrite placeholder
		for i, s := range c.Docker.PullCommand {
			if s == "<image>" {
				c.Docker.PullCommand[i] = c.Docker.Image
			}
		}

		// run command
		cmd := exec.Command(c.Docker.PullCommand[0], c.Docker.PullCommand[1:]...)
		return cmd.Run()
	}
}

func pullImageAuth(c v1.Component) (string, error) {
	if c.Docker.PullUsername == "" && c.Docker.PullPassword == "" {
		return "", nil
	}
	authConfig := types.AuthConfig{
		Username: c.Docker.PullUsername,
		Password: c.Docker.PullPassword,
	}
	encodedJSON, err := json.Marshal(authConfig)
	if err != nil {
		return "", err
	}
	authStr := base64.URLEncoding.EncodeToString(encodedJSON)
	return authStr, nil
}

func getImageByNameStripRepo(dockerClient *docker.Client, imageName string) (*types.ImageSummary, error) {
	img, err := getImageByName(dockerClient, imageName)
	if err != nil {
		return nil, err
	}
	if img == nil {
		slashes := strings.Count(imageName, "/")
		if slashes > 1 {
			imageName = imageName[strings.Index(imageName, "/")+1:]
		}
		img, err = getImageByName(dockerClient, imageName)
		if err != nil {
			return nil, err
		}
	}
	return img, nil
}

func getImageByName(dockerClient *docker.Client, imageName string) (*types.ImageSummary, error) {
	filter := filters.NewArgs()
	filter.Add("reference", common.NormalizeImageName(imageName))
	images, err := dockerClient.ImageList(context.Background(), types.ImageListOptions{
		Filters: filter,
	})
	if err == nil {
		if len(images) > 0 {
			return &images[0], nil
		}
		return nil, nil
	}
	return nil, err
}

func printContainerLogs(dockerClient *docker.Client, containerId string) error {
	out, err := dockerClient.ContainerLogs(context.Background(), containerId, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})
	if err == nil {
		defer common.CheckClose(out, &err)
		fmt.Printf("Container logs for: %s\n", containerId)
		_, err = io.Copy(os.Stdout, out)
	}
	return err
}
