package gateway

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	docker "github.com/docker/docker/client"
	"github.com/mgutz/logxi/v1"
	"gitlab.com/coopernurse/maelstrom/pkg/common"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

func NewLogsHandler(dockerClient *docker.Client) *LogsHandler {
	return &LogsHandler{
		dockerClient: dockerClient,
	}
}

type LogsHandler struct {
	dockerClient *docker.Client
}

func (h *LogsHandler) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sinceArg := req.FormValue("since")
	if sinceArg == "" {
		sinceArg = "1m"
	}

	componentsArg := req.FormValue("components")
	componentNames := make(map[string]bool)
	for _, s := range strings.Split(componentsArg, ",") {
		s = strings.TrimSpace(s)
		if s != "" && s != "*" {
			componentNames[s] = true
		}
	}

	filterArgs := filters.NewArgs()
	filterArgs.Add("event", "start")
	msgCh, errCh := h.dockerClient.Events(ctx, types.EventsOptions{
		Filters: filterArgs,
	})

	logReadCtx, logCancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}

	logCh := make(chan common.LogMsg, 10)
	pingTicker := time.NewTicker(time.Second)

	containers, err := listContainers(h.dockerClient)
	if err != nil {
		log.Error("logs: listContainers failed", "err", err)
		rw.WriteHeader(http.StatusInternalServerError)
		_, _ = fmt.Fprintf(rw, "Can't list docker containers")
		return
	}

	for _, c := range containers {
		component := c.Labels["maelstrom_component"]
		if followComponent(componentNames, component) {
			h.startStreamLogs(logReadCtx, wg, logCh, c.ID, component, sinceArg)
		}
	}

	rw.WriteHeader(http.StatusOK)
	for {
		var line string
		select {
		case <-pingTicker.C:
			line = common.PingLogMsg
		case m := <-logCh:
			msgBytes, err := json.Marshal(m)
			if err == nil {
				line = string(msgBytes)
			} else {
				log.Error("logs: json.Marshal failed", "err", err)
			}
		case m := <-msgCh:
			if log.IsDebug() {
				log.Debug("logs: docker event", "from", m.From, "actor", m.Actor, "type", m.Type, "status", m.Status)
			}
			if m.Type == "container" && m.Status == "start" {
				cont, err := h.dockerClient.ContainerInspect(context.Background(), m.Actor.ID)
				if err == nil {
					if cont.Config != nil && cont.Config.Labels != nil {
						component := cont.Config.Labels["maelstrom_component"]
						if followComponent(componentNames, component) {
							h.startStreamLogs(logReadCtx, wg, logCh, m.Actor.ID, component, sinceArg)
						}
					}
				} else {
					log.Error("logs: ContainerInspect failed", "containerId", m.Actor.ID, "err", err)
				}
			}
		case m := <-errCh:
			log.Warn("logs: docker error", "err", m.Error())
		}

		if line != "" {
			_, err := rw.Write([]byte(line + "\r\n"))
			if err == nil {
				if f, ok := rw.(http.Flusher); ok {
					f.Flush()
				}
			} else {
				logErr(err, "Unable to write line to log client")
				break
			}
		}
	}
	logCancel()
	wg.Wait()
}

func (h *LogsHandler) startStreamLogs(ctx context.Context, wg *sync.WaitGroup, logCh chan common.LogMsg,
	containerId string, component string, since string) {
	if component == "" || containerId == "" {
		return
	}
	reader, err := h.dockerClient.ContainerLogs(ctx, containerId,
		types.ContainerLogsOptions{
			ShowStdout: true,
			ShowStderr: true,
			Follow:     true,
			Since:      since,
		})
	if err == nil {
		go streamLogs(component, wg, reader, logCh)
	} else {
		log.Error("logs: dockerClient.ContainerLogs failed", "containerId", containerId, "err", err)
	}
}

func logErr(err error, msg string) {
	if err != nil && !strings.Contains(err.Error(), "broken pipe") {
		log.Error("logs: "+msg, "err", err)
	}
}

func streamLogs(component string, wg *sync.WaitGroup, reader io.ReadCloser, out chan<- common.LogMsg) {
	var err error
	wg.Add(1)
	defer wg.Done()
	defer common.CheckClose(reader, &err)

	if log.IsDebug() {
		log.Debug("streamLogs start", "component", component)
	}

	var header = make([]byte, 8)
	for {
		_, err := io.ReadFull(reader, header)
		if err == nil {
			size := int(binary.BigEndian.Uint32(header[4:8]))
			var body = make([]byte, size)
			_, err = io.ReadFull(reader, body)
			stream := "stdout"
			if header[0] == 2 {
				stream = "stderr"
			}
			if err == nil {
				out <- common.LogMsg{
					Component: component,
					Stream:    stream,
					Data:      string(body),
				}
			} else {
				break
			}
		} else {
			break
		}
	}
	if log.IsDebug() {
		log.Debug("streamLogs done", "component", component)
	}
}

func followComponent(componentNames map[string]bool, component string) bool {
	if component == "" {
		return false
	}
	if len(componentNames) == 0 {
		return true
	}

	for k := range componentNames {
		if strings.HasPrefix(k, "*") && strings.HasSuffix(k, "*") && len(k) > 2 {
			if strings.Contains(component, k[1:len(k)-1]) {
				return true
			}
		} else if strings.HasPrefix(k, "*") {
			if strings.HasSuffix(component, k[1:]) {
				return true
			}
		} else if strings.HasSuffix(k, "*") {
			if strings.HasPrefix(component, k[0:len(k)-1]) {
				return true
			}
		} else if k == component {
			return true
		}
	}

	return false
}
