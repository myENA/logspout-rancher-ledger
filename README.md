
# logspout-rancher-ledger

A big thanks to @blippar for work on https://github.com/blippar/logspout-logstash-rancher. This plugin is based on their work, but with MAJOR changes to populating the Rancher info for the event data shipped to logstash

A minimalistic adapter for github.com/gliderlabs/logspout for containers running in Rancher 2.0 k8s cluster to write to Logstash. 

In the local directory there is an example ELK stack and logspout compose files that can be used to deploy to rancher. 
You will need to tweak the logspout-sample.yaml file

# Changes
I have created a new field index that is populated by the label io.rancher.stack.name. If it is empty or does not exist 
the index field is populated with container name label
```go
if rancherInfo.Stack.StackName != "" {
        data["index"] = rancherInfo.Stack.StackName
    } else {
        data["index"] = rancherInfo.Container.Name
}
```

All the metadata that gets passed in the event data is now pulled from the rancher API.
```go
// Uses the passed docker id to find the rancher Id
func GetRancherId(cID string) *client.Container {

    // This adds a filter to search for the specific container we just received an event from
    filters :=map[string]interface{}{"externalId": cID}

    listOpts := &client.ListOpts{Filters: filters}

    container, err := rancher.Container.List(listOpts)

    if err != nil {
        log.Print(err)
    }

    // There should only ever be 1 container in the list thanks to our filter
    return &container.Data[0]
}

```

The metadata also get cached in an in-memory map[string]*RancherInfo *NOTE* As of yet I do not know of a good way to remove the data from "cache" when a container 
gets deleted.
```go
func Cache(con *RancherInfo) {
	cCache[con.Container.DockerID] = con
}

func ExistsInCache(containerID string) bool {
	for k, _ := range cCache {
		if k == containerID {
			return true
		}
	}

	return false
}

func GetFromCache(cID string) *RancherInfo {
	return cCache[cID]
}
```

- Removed Logstash Tags -- Due to pulling the k8s namespace which is in essence what was generally getting passed as tags.
- API admin account keys *MUST* to be passed as env variables to query the API for container.
()This could be avoided by passing the service account labels in rancher, but logspout will send logs outside the scope of the
environment that the container is deployed in and cause errors)
```go
// Setting package global Rancher API setting
var cattleUrl = os.Getenv("CATTLE_URL")
var cattleAccessKey = os.Getenv("CATTLE_ACCESS_KEY")
var cattleSecretKey = os.Getenv("CATTLE_SECRET_KEY")
```
## Building 
Follow the instructions in https://github.com/gliderlabs/logspout/tree/master/custom on how to build your own Logspout container with custom modules. These files are in the custom folder of the repo.

```go
package main

import (
  _ "github.com/myENA/logspout-rancher-ledger"
  _ "github.com/gliderlabs/logspout/transports/udp"
  _ "github.com/gliderlabs/logspout/transports/tcp"
)
```

in modules.go.
## Available configuration options
You can also add arbitrary logstash fields to the event using the ```LOGSTASH_FIELDS``` container environment variable:

```bash
  # Add any number of arbitrary fields to your event
  -e LOGSTASH_FIELDS="myfield=something,anotherfield=something_else"
```

The output into logstash should be like:

```json
    "myfield": "something",
    "another_field": "something_else",
```

Both configuration options can be set for every individual container, or for the logspout-logstash
container itself where they then become a default for all containers if not overridden there.

This table shows all available configurations:

| Environment Variable | Input Type | Default Value |
|----------------------|------------|---------------|
| LOGSTASH_TAGS        | array      | None          |
| LOGSTASH_FIELDS      | map        | None          |

