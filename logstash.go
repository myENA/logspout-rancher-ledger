package logstash

import (
	"encoding/json"
	"errors"
	"github.com/fsouza/go-dockerclient"
	"github.com/gliderlabs/logspout/router"
	"github.com/rancherio/go-rancher/v3"
	"log"
	"net"
	"os"
	"strings"
)

// Setting package global Rancher API setting
var cattleUrl = os.Getenv("CATTLE_URL")
var cattleAccessKkey = os.Getenv("CATTLE_ACCESS_KEY")
var cattleSecretKey = os.Getenv("CATTLE_SECRET_KEY")

var err error
var rancher *client.RancherClient
var cCache map[string]*RancherInfo

func init() {
	router.AdapterFactories.Register(NewLogstashAdapter, "logstash")
	rancher = initRancherClient()
	cCache = make(map[string]*RancherInfo)

}

// LogstashAdapter is an adapter that streams UDP JSON to Logstash.
type LogstashAdapter struct {
	conn           net.Conn
	route          *router.Route
	containerTags  map[string][]string
	logstashFields map[string]map[string]string
}

func initRancherClient() *client.RancherClient {

	config := &client.ClientOpts{
		Url:       cattleUrl,
		AccessKey: cattleAccessKkey,
		SecretKey: cattleSecretKey,
	}

	r, err := client.NewRancherClient(config)

	if err != nil {
		log.Fatalf("Unable to establish rancher api connection: %s", err)
	}

	return r
}

// NewLogstashAdapter creates a LogstashAdapter with UDP as the default transport.
func NewLogstashAdapter(route *router.Route) (router.LogAdapter, error) {
	transport, found := router.AdapterTransports.Lookup(route.AdapterTransport("udp"))
	if !found {
		return nil, errors.New("unable to find adapter: " + route.Adapter)
	}

	conn, err := transport.Dial(route.Address, route.Options)
	if err != nil {
		return nil, err
	}

	return &LogstashAdapter{
		route:          route,
		conn:           conn,
		containerTags:  make(map[string][]string),
		logstashFields: make(map[string]map[string]string),
	}, nil
}

// Parse the logstash fields env variables
func GetLogstashFields(c *docker.Container, a *LogstashAdapter) map[string]string {
	if fields, ok := a.logstashFields[c.ID]; ok {
		return fields
	}

	fieldsStr := os.Getenv("LOGSTASH_FIELDS")
	fields := map[string]string{}

	for _, e := range c.Config.Env {
		if strings.HasPrefix(e, "LOGSTASH_FIELDS=") {
			fieldsStr = strings.TrimPrefix(e, "LOGSTASH_FIELDS=")
		}
	}

	if len(fieldsStr) > 0 {
		for _, f := range strings.Split(fieldsStr, ",") {
			sp := strings.Split(f, "=")
			k, v := sp[0], sp[1]
			fields[k] = v
		}
	}

	a.logstashFields[c.ID] = fields

	return fields
}

// Uses the passed docker id to find the rancher Id
func GetRancherId(cID string) *client.Container {

	// This adds a filter to search for the specific container we just received an event from
	filters := map[string]interface{}{"externalId": cID}

	listOpts := &client.ListOpts{Filters: filters}

	container, err := rancher.Container.List(listOpts)

	if err != nil {
		log.Print(err)
	}

	// There should only ever be 1 container in the list thanks to our filter
	return &container.Data[0]
}

// Add the RancherInfo to the cache
func Cache(con *RancherInfo) {
	cCache[con.Container.DockerID] = con
}

// Check if the container data already exists in the cached map
func ExistsInCache(containerID string) bool {
	for k := range cCache {
		if k == containerID {
			return true
		}
	}

	return false
}

// Get the container data from the map
func GetFromCache(cID string) *RancherInfo {
	return cCache[cID]
}

// Get the rancher meteadata from the api/cahce
func GetRancherInfo(c *docker.Container) *RancherInfo {

	// Check if we have added this container to cache before
	if !ExistsInCache(c.ID) {

		// Pull rancher data from the API instead of the docker.sock
		// First we use the docker id to pull the rancher container data
		rcontainer := GetRancherId(c.ID)

		// Since its not in cache go get it
		// Get container data, service data, and stack data if available
		if rcontainer == nil {
			log.Print("Could not find rancher metadata in the API")
			return nil
		}

		service, err := rancher.Service.ById(rcontainer.ServiceId)
		if err != nil {
			log.Print(err)
		}

		stackData, err := rancher.Stack.ById(rcontainer.StackId)

		if err != nil {
			log.Print(err)
		}

		// Fill out container data
		container := &RancherContainer{
			Name:     rcontainer.Labels["io.rancher.container.name"],
			IP:       rcontainer.Labels["io.rancher.container.ip"],
			ID:       rcontainer.Id,
			HostID:   rcontainer.HostId,
			DockerID: c.ID,
		}

		stack := &RancherStack{
			Service:    service.Name,
			StackName:  stackData.Name,
			StackState: stackData.State,
		}

		// Since Rancher 2.0 an environemt is a k8s namespace so we pull the namespace label to set
		// the environment
		environment := rcontainer.Labels["io.kubernetes.pod.namespace"]
		rancherInfo := &RancherInfo{
			Environment: environment,
			Container:   container,
			Stack:       stack,
		}

		Cache(rancherInfo)

		return rancherInfo
	}

	return GetFromCache(c.ID)
}

// Stream implements the router.LogAdapter interface.
func (a *LogstashAdapter) Stream(logstream chan *router.Message) {

	for m := range logstream {

		dockerInfo := DockerInfo{
			Name:     m.Container.Name,
			ID:       m.Container.ID,
			Image:    m.Container.Config.Image,
			Hostname: m.Container.Config.Hostname,
		}

		fields := GetLogstashFields(m.Container, a)

		rancherInfo := GetRancherInfo(m.Container)

		var js []byte
		var data map[string]interface{}
		var err error

		// Try to parse JSON-encoded m.Data. If it wasn't JSON, create an empty object
		// and use the original data as the message.
		if err = json.Unmarshal([]byte(m.Data), &data); err != nil {
			data = make(map[string]interface{})
			data["message"] = m.Data
		}

		for k, v := range fields {
			data[k] = v
		}

		data["docker"] = dockerInfo
		data["rancher"] = rancherInfo

		// Set the index field for use with the logstash.conf
		// elasticsearch hosts field has "%{index}-%{+YYYY.MM.dd}"
		if rancherInfo.Stack.StackName != "" {
			data["index"] = rancherInfo.Stack.StackName
		} else {
			data["index"] = rancherInfo.Container.Name
		}

		// Return the JSON encoding
		if js, err = json.Marshal(data); err != nil {
			// Log error message and continue parsing next line, if marshalling fails
			log.Println("logstash: could not marshal JSON:", err)
			continue
		}

		// To work with tls and tcp transports via json_lines codec
		js = append(js, byte('\n'))

		if _, err := a.conn.Write(js); err != nil {
			// There is no retry option implemented yet
			log.Fatal("logstash: could not write:", err)
		}
	}
}

// Container Docker info for event data
type DockerInfo struct {
	Name     string `json:"name"`
	ID       string `json:"id"`
	Image    string `json:"image"`
	Hostname string `json:"hostname"`
}

// Rancher data for evetn data
type RancherInfo struct {
	Environment string            `json:"environment,omitempty"`
	Container   *RancherContainer `json:"container,omitempty"`
	Stack       *RancherStack     `json:"stack,omitempty"`
}

// Rancher container data for event
type RancherContainer struct {
	Name     string `json:"name"`           // io.rancher.container.name
	IP       string `json:"ip,omitempty"`   // io.rancher.container.ip
	ID       string `json:"once,omitempty"` // io.rancher.container.start_once
	HostID   string `json:"hostId,omitempty"`
	DockerID string `json:"dockerId,omitempty"`
}

// Rancher stack inf for event
type RancherStack struct {
	Service    string `json:"service,omitempty"`   // Service Name from API
	StackName  string `json:"stackName,omitempty"` // io.rancher.stack.name
	StackState string `json:"stackState,omitempty"`
}
