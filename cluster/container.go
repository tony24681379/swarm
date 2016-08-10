package cluster

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	log "github.com/Sirupsen/logrus"
	"github.com/docker/docker/pkg/stringid"
	"github.com/docker/engine-api/types"
	"github.com/docker/go-units"
)

// CheckpointTicker is exported
type CheckpointTicker struct {
	KeepVersion    int
	PreDumpVersion int
	Version        int
	Checkpointed   map[int]bool
	Ticker         bool
}

// Container is exported
type Container struct {
	types.Container

	Config           *ContainerConfig
	Info             types.ContainerJSON
	Engine           *Engine
	CheckpointTicker CheckpointTicker
}

// StateString returns a single string to describe state
func StateString(state *types.ContainerState) string {
	startedAt, _ := time.Parse(time.RFC3339Nano, state.StartedAt)
	if state.Running {
		if state.Paused {
			return "paused"
		}
		if state.Restarting {
			return "restarting"
		}
		return "running"
	}

	if state.Dead {
		return "dead"
	}

	if state.Checkpointed {
		return "checkpointed"
	}

	if startedAt.IsZero() {
		return "created"
	}

	return "exited"
}

// FullStateString returns human-readable description of the state
func FullStateString(state *types.ContainerState) string {
	startedAt, _ := time.Parse(time.RFC3339Nano, state.StartedAt)
	finishedAt, _ := time.Parse(time.RFC3339Nano, state.FinishedAt)
	checkpointedAt, _ := time.Parse(time.RFC3339Nano, state.CheckpointedAt)
	if state.Running {
		if state.Paused {
			return fmt.Sprintf("Up %s (Paused)", units.HumanDuration(time.Now().UTC().Sub(startedAt)))
		}
		if state.Restarting {
			return fmt.Sprintf("Restarting (%d) %s ago", state.ExitCode, units.HumanDuration(time.Now().UTC().Sub(finishedAt)))
		}
		return fmt.Sprintf("Up %s", units.HumanDuration(time.Now().UTC().Sub(startedAt)))
	}

	if state.Dead {
		return "Dead"
	}

	if state.Checkpointed {
		return fmt.Sprintf("Checkpointed %s ago", units.HumanDuration(time.Now().UTC().Sub(checkpointedAt)))
	}

	if startedAt.IsZero() {
		return "Created"
	}

	if finishedAt.IsZero() {
		return ""
	}

	return fmt.Sprintf("Exited (%d) %s ago", state.ExitCode, units.HumanDuration(time.Now().UTC().Sub(finishedAt)))
}

// Refresh container
func (c *Container) Refresh() (*Container, error) {
	return c.Engine.refreshContainer(c.ID, true)
}

// SetupCheckpointContainer setup container checkpoint
func (c *Container) SetupCheckpointContainer() {
	if c.Info.State.Running {
		if checkpointTime, keepVersion, err := c.Config.HasCheckpointTimePolicy(); err != nil {
			log.Errorf("Fails to set container %s checkpoint time, %s", c.ID, err)
		} else if checkpointTime > 0 {
			if c.CheckpointTicker.Ticker == false {
				c.CheckpointContainerTicker(checkpointTime, keepVersion)
			}
		}
	}
}

// CheckpointContainerTicker set a checkpoint ticker
func (c *Container) CheckpointContainerTicker(checkpointTime time.Duration, keepVersion int) {
	var ticker = time.NewTicker(checkpointTime)
	var stopCh = make(chan bool)
	c.CheckpointTicker = CheckpointTicker{
		Checkpointed:   make(map[int]bool),
		KeepVersion:    keepVersion,
		PreDumpVersion: 0,
		Version:        0,
		Ticker:         true,
	}

	go func() {
		c.Engine.WaitContainer(c.ID)
		log.Infof("wait %s stop", c.ID)
		stopCh <- true
	}()
	go func() {
		for {
			select {
			case <-ticker.C:
				version := c.CheckpointTicker.Version
				preDumpVersion := c.CheckpointTicker.PreDumpVersion
				c.CheckpointTicker.Checkpointed[version] = false

				if version%keepVersion == 0 {
					imgDir := filepath.Join(c.Engine.DockerRootDir, "checkpoint", c.ID, strconv.Itoa(preDumpVersion))
					criuOpts := types.CriuConfig{
						ImagesDirectory: imgDir,
						WorkDirectory:   filepath.Join(imgDir, "criu.work"),
						LeaveRunning:    true,
						TrackMem:        true,
						PreDump:         true,
					}
					item := &Item{
						C:         c,
						CriuOpts:  criuOpts,
						Version:   preDumpVersion,
						IsPreDump: true,
					}
					checkpointQueue.Push(item)
				}
				imgDir := filepath.Join(c.Engine.DockerRootDir, "checkpoint", c.ID, strconv.Itoa(preDumpVersion), strconv.Itoa(version))

				criuOpts := types.CriuConfig{
					ImagesDirectory:     imgDir,
					LeaveRunning:        true,
					TrackMem:            true,
					PrevImagesDirectory: "../" + strconv.Itoa(version-1),
				}
				if version%keepVersion == 0 {
					criuOpts.PrevImagesDirectory = ".."
				}

				item := &Item{
					C:        c,
					CriuOpts: criuOpts,
					Version:  version,
				}
				checkpointQueue.Push(item)

				c.CheckpointTicker.Checkpointed[version] = true
				if version%keepVersion == keepVersion-1 {
					criuOpts := types.CriuConfig{
						ImagesDirectory: filepath.Join(c.Engine.DockerRootDir, "checkpoint", c.ID, strconv.Itoa(preDumpVersion-1)),
					}
					item := &Item{
						C:        c,
						CriuOpts: criuOpts,
						IsDelete: true,
						Version:  preDumpVersion - 1,
					}
					checkpointQueue.Push(item)
				}
				c.CheckpointTicker.Version++
				if c.CheckpointTicker.Version%keepVersion == 0 {
					c.CheckpointTicker.PreDumpVersion++
				}
			case <-stopCh:
				ticker.Stop()
				c.CheckpointTicker.Ticker = false
				log.Infof("%s stop checkpoint", c.ID)
				return
			}
		}
	}()
}

// An Item is something we manage in a priority queue.
type Item struct {
	C         *Container
	CriuOpts  types.CriuConfig
	IsPreDump bool
	Version   int
	IsDelete  bool
	index     int
}

// NewQueue returns a new queue with the given initial size.
func NewQueue(size int) *Queue {
	return &Queue{
		nodes: make([]*Item, size),
		size:  size,
	}
}

// Queue is a basic FIFO queue based on a circular list that resizes as needed.
type Queue struct {
	nodes []*Item
	size  int
	head  int
	tail  int
	count int
}

// Push adds a node to the queue.
func (q *Queue) Push(n *Item) {
	if q.head == q.tail && q.count > 0 {
		nodes := make([]*Item, len(q.nodes)+q.size)
		copy(nodes, q.nodes[q.head:])
		copy(nodes[len(q.nodes)-q.head:], q.nodes[:q.head])
		q.head = 0
		q.tail = len(q.nodes)
		q.nodes = nodes
	}
	q.nodes[q.tail] = n
	q.tail = (q.tail + 1) % len(q.nodes)
	q.count++
}

// Pop removes and returns a node from the queue in first to last order.
func (q *Queue) Pop() *Item {
	if q.count == 0 {
		return nil
	}
	node := q.nodes[q.head]
	q.head = (q.head + 1) % len(q.nodes)
	q.count--
	return node
}

func (q *Queue) Len() int { return q.count }

// Containers represents a list of containers
type Containers []*Container

// Get returns a container using its ID or Name
func (containers Containers) Get(IDOrName string) *Container {
	// Abort immediately if the name is empty.
	if len(IDOrName) == 0 {
		return nil
	}

	// Match exact or short Container ID.
	for _, container := range containers {
		if container.ID == IDOrName || stringid.TruncateID(container.ID) == IDOrName {
			return container
		}
	}

	// Match exact Swarm ID.
	for _, container := range containers {
		if swarmID := container.Config.SwarmID(); swarmID == IDOrName || stringid.TruncateID(swarmID) == IDOrName {
			return container
		}
	}

	candidates := []*Container{}

	// Match name, /name or engine/name.
	for _, container := range containers {
		found := false
		for _, name := range container.Names {
			if name == IDOrName || name == "/"+IDOrName || container.Engine.ID+name == IDOrName || container.Engine.Name+name == IDOrName {
				found = true
			}
		}
		if found {
			candidates = append(candidates, container)
		}
	}

	if size := len(candidates); size == 1 {
		return candidates[0]
	} else if size > 1 {
		return nil
	}

	// Match Container ID prefix.
	for _, container := range containers {
		if strings.HasPrefix(container.ID, IDOrName) {
			candidates = append(candidates, container)
		}
	}

	// Match Swarm ID prefix.
	for _, container := range containers {
		if strings.HasPrefix(container.Config.SwarmID(), IDOrName) {
			candidates = append(candidates, container)
		}
	}

	if len(candidates) == 1 {
		return candidates[0]
	}

	return nil
}
