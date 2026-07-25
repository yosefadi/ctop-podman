package connector

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/op/go-logging"
	"github.com/hako/durafmt"

	"github.com/bcicen/ctop/connector/collector"
	"github.com/bcicen/ctop/connector/manager"
	"github.com/bcicen/ctop/container"
	api "github.com/fsouza/go-dockerclient"
)

func init() { enabled["docker"] = NewDocker }

var actionToStatus = map[string]string{
	"start":   "running",
	"die":     "exited",
	"died":    "exited",
	"stop":    "exited",
	"pause":   "paused",
	"unpause": "running",
}

type StatusUpdate struct {
	Cid    string
	Field  string // "status" or "health"
	Status string
}

type Docker struct {
	client       *api.Client
	containers   map[string]*container.Container
	needsRefresh chan string // container IDs requiring refresh
	statuses     chan StatusUpdate
	closed       chan struct{}
	lock         sync.RWMutex
	hostNCPU     uint8
}

func NewDocker() (Connector, error) {
	// init docker client
	client, err := api.NewClientFromEnv()
	if err != nil {
		return nil, err
	}
	return newDockerConnector(client)
}

// newDockerConnector wires up a Docker-API connector around an already
// constructed client. Shared by the docker and podman connectors, since
// Podman's compat API is Docker-API-compatible.
func newDockerConnector(client *api.Client) (Connector, error) {
	// query info as pre-flight healthcheck
	info, err := client.Info()
	if err != nil {
		return nil, err
	}

	cm := &Docker{
		client:       client,
		containers:   make(map[string]*container.Container),
		needsRefresh: make(chan string, 60),
		statuses:     make(chan StatusUpdate, 60),
		closed:       make(chan struct{}),
		lock:         sync.RWMutex{},
		hostNCPU:     uint8(info.NCPU),
	}

	log.Debugf("docker-connector ID: %s", info.ID)
	log.Debugf("docker-connector Driver: %s", info.Driver)
	log.Debugf("docker-connector Images: %d", info.Images)
	log.Debugf("docker-connector Name: %s", info.Name)
	log.Debugf("docker-connector ServerVersion: %s", info.ServerVersion)

	go cm.Loop()
	go cm.LoopStatuses()
	cm.refreshAll()
	go cm.watchEvents()
	go cm.periodicResync()
	return cm, nil
}

// Docker implements Connector
func (cm *Docker) Wait() struct{} { return <-cm.closed }

// Docker events watcher
func (cm *Docker) watchEvents() {
	log.Info("docker event listener starting")
	events := make(chan *api.APIEvents)
	// Deliberately not filtering on "event" here (only "type"). Verified
	// against a live Podman socket: Podman's compat filter matching poisons
	// the *entire* "event" filter list if it contains a single value Podman
	// doesn't recognize (e.g. Docker's "destroy" - Podman's internal name is
	// "remove") - so a combined Docker+Podman value list silently drops
	// events that should have matched (confirmed: adding "destroy" to the
	// list caused Podman to never deliver "remove" at all, even though
	// "remove" was also in the list). Filtering client-side in the switch
	// below instead works identically against both engines.
	opts := api.EventsOptions{Filters: map[string][]string{
		"type": {"container"},
	}}
	cm.client.AddEventListenerWithOptions(opts, events)

	for e := range events {
		actionName := e.Action
		switch {
		case actionName == "create":
			if log.IsEnabledFor(logging.DEBUG) {
				log.Debugf("handling docker event: action=create id=%s", e.ID)
			}
			cm.needsRefresh <- e.ID
		case actionName == "destroy" || actionName == "remove":
			// Podman's compat layer only rewrites "remove"->"destroy" for
			// image events, not container events, so it arrives untranslated.
			if log.IsEnabledFor(logging.DEBUG) {
				log.Debugf("handling docker event: action=%s id=%s", actionName, e.ID)
			}
			cm.delByID(e.ID)
		case strings.HasPrefix(actionName, "health_status"):
			// Docker encodes the new status in the action string itself
			// ("health_status: healthy"); Podman emits a bare "health_status"
			// and carries the value in a field go-dockerclient doesn't parse.
			// Rather than depend on either engine's exact event shape, just
			// re-inspect - refresh() already reads health straight from the
			// container inspect response, which both engines populate.
			if log.IsEnabledFor(logging.DEBUG) {
				log.Debugf("handling docker event: action=%s id=%s", actionName, e.ID)
			}
			cm.needsRefresh <- e.ID
		default:
			// check if this action changes status e.g. start -> running
			status := actionToStatus[actionName]
			if status != "" {
				if log.IsEnabledFor(logging.DEBUG) {
					log.Debugf("handling docker event: action=%s id=%s %s", actionName, e.ID, status)
				}
				cm.statuses <- StatusUpdate{e.ID, "status", status}
			}
		}
	}
	log.Info("docker event listener exited")
	close(cm.closed)
}

// periodicResync re-queues all known containers for refresh on a fixed
// interval, bounding staleness even if a lifecycle event is dropped,
// misshapen, or never emitted (health-status events in particular are
// documented as unreliable on some Podman versions - see
// containers/podman#13493, #19237, #24003). A no-op in practice against
// Docker, where the event stream is already exhaustive.
func (cm *Docker) periodicResync() {
	const interval = 5 * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			cm.lock.Lock()
			ids := make([]string, 0, len(cm.containers))
			for id := range cm.containers {
				ids = append(ids, id)
			}
			cm.lock.Unlock()
			for _, id := range ids {
				select {
				case cm.needsRefresh <- id:
				default: // drop if the refresh queue is backed up; next tick retries
				}
			}
		case <-cm.closed:
			return
		}
	}
}

func portsFormat(ports map[api.Port][]api.PortBinding) string {
	var exposed []string
	var published []string

	for k, v := range ports {
		if len(v) == 0 {
			exposed = append(exposed, string(k))
			continue
		}
		for _, binding := range v {
			s := fmt.Sprintf("%s:%s -> %s", binding.HostIP, binding.HostPort, k)
			published = append(published, s)
		}
	}

	return strings.Join(append(exposed, published...), "\n")
}

func webPort(ports map[api.Port][]api.PortBinding) string {
	for _, v := range ports {
		if len(v) == 0 {
			continue
		}
		for _, binding := range v {
			publishedIp := binding.HostIP
			if publishedIp == "0.0.0.0" {
				publishedIp = "localhost"
			}
			publishedWebPort := fmt.Sprintf("%s:%s", publishedIp, binding.HostPort)
			return publishedWebPort
		}
	}
	return ""
}

func ipsFormat(networks map[string]api.ContainerNetwork) string {
	var ips []string

	for k, v := range networks {
		s := fmt.Sprintf("%s:%s", k, v.IPAddress)
		ips = append(ips, s)
	}

	return strings.Join(ips, "\n")
}

func (cm *Docker) refresh(c *container.Container) {
	insp, found, failed := cm.inspect(c.Id)
	if failed {
		return
	}
	// remove container if no longer exists
	if !found {
		cm.delByID(c.Id)
		return
	}
	c.SetMeta("name", shortName(insp.Name))
	c.SetMeta("image", insp.Config.Image)
	c.SetMeta("IPs", ipsFormat(insp.NetworkSettings.Networks))
	c.SetMeta("ports", portsFormat(insp.NetworkSettings.Ports))
	webPort := webPort(insp.NetworkSettings.Ports)
	if webPort != "" {
		c.SetMeta("Web Port", webPort)
	}
	c.SetMeta("created", insp.Created.Format("Mon Jan 02 15:04:05 2006"))
	c.SetMeta("uptime", calcUptime(insp))
	c.SetMeta("health", insp.State.Health.Status)
	c.SetMeta("[ENV-VAR]", strings.Join(insp.Config.Env, ";"))
	c.SetState(insp.State.Status)
}

func (cm *Docker) inspect(id string) (insp *api.Container, found bool, failed bool) {
	c, err := cm.client.InspectContainer(id)
	if err != nil {
		if _, notFound := err.(*api.NoSuchContainer); notFound {
			return c, false, false
		}
		// other error e.g. connection failed
		log.Errorf("%s (%T)", err.Error(), err)
		return c, false, true
	}
	return c, true, false
}

func calcUptime(insp *api.Container) string {
	endTime := insp.State.FinishedAt
	if endTime.IsZero() || insp.State.Running {
		endTime = time.Now()
	}
	uptime := endTime.Sub(insp.State.StartedAt)
	return durafmt.Parse(uptime).LimitFirstN(1).String()
}

// Mark all container IDs for refresh
func (cm *Docker) refreshAll() {
	opts := api.ListContainersOptions{All: true}
	allContainers, err := cm.client.ListContainers(opts)
	if err != nil {
		log.Errorf("%s (%T)", err.Error(), err)
		return
	}

	for _, i := range allContainers {
		c := cm.MustGet(i.ID)
		c.SetMeta("name", shortName(i.Names[0]))
		c.SetState(i.State)
		cm.needsRefresh <- c.Id
	}
}

func (cm *Docker) Loop() {
	for {
		select {
		case id := <-cm.needsRefresh:
			c := cm.MustGet(id)
			cm.refresh(c)
		case <-cm.closed:
			return
		}
	}
}

func (cm *Docker) LoopStatuses() {
	for {
		select {
		case statusUpdate := <-cm.statuses:
			c, _ := cm.Get(statusUpdate.Cid)
			if c != nil {
				if statusUpdate.Field == "health" {
					c.SetMeta("health", statusUpdate.Status)
				} else {
					c.SetState(statusUpdate.Status)
				}
			}
		case <-cm.closed:
			return
		}
	}
}

// MustGet gets a single container, creating one anew if not existing
func (cm *Docker) MustGet(id string) *container.Container {
	c, ok := cm.Get(id)
	// append container struct for new containers
	if !ok {
		// create collector
		collector := collector.NewDocker(cm.client, id, cm.hostNCPU)
		// create manager
		manager := manager.NewDocker(cm.client, id)
		// create container
		c = container.New(id, collector, manager)
		cm.lock.Lock()
		cm.containers[id] = c
		cm.lock.Unlock()
	}
	return c
}

// Docker implements Connector
func (cm *Docker) Get(id string) (*container.Container, bool) {
	cm.lock.Lock()
	c, ok := cm.containers[id]
	cm.lock.Unlock()
	return c, ok
}

// Remove containers by ID
func (cm *Docker) delByID(id string) {
	cm.lock.Lock()
	delete(cm.containers, id)
	cm.lock.Unlock()
	log.Infof("removed dead container: %s", id)
}

// Docker implements Connector
func (cm *Docker) All() (containers container.Containers) {
	cm.lock.Lock()
	for _, c := range cm.containers {
		containers = append(containers, c)
	}

	containers.Sort()
	containers.Filter()
	cm.lock.Unlock()
	return containers
}

// use primary container name
func shortName(name string) string {
	return strings.TrimPrefix(name, "/")
}
