package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

var cli *client.Client

func recoverer(id int, f func()) {
	defer func() {
		if err := recover(); err != nil {
			fmt.Println(err)
			time.Sleep(2 * time.Second)
			go recoverer(id, f)
		}
	}()
	f()
}

func listenEvents(portsMapChan chan<- map[string]string) {
	ctx := context.Background()
	filter := filters.NewArgs()
	filter.Add("type", "container")
	filter.Add("event", "start")
	filter.Add("event", "die")
	msgs, errs := cli.Events(ctx, types.EventsOptions{Filters: filter})
	for {
		select {
		case err := <-errs:
			{
				panic(err)
			}
		case _ = <-msgs:
			{
				go updateContainers(portsMapChan)
			}
		}
	}
}

func updateContainers(portsMapChan chan<- map[string]string) {

	portsMap := make(map[string]string)

	ctx := context.Background()

	filter := filters.NewArgs()
	filter.Add("label", "crserver")
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{Filters: filter})
	if err != nil {
		log.Println(err.Error())
		return
	}
	for _, container := range containers {
		haveHTTPPort := false
		for _, port := range container.Ports {
			if port.PrivatePort == 80 {
				haveHTTPPort = true
				break
			}
		}
		if !haveHTTPPort {
			log.Printf("Skipping container %s because it does not expose port 80", container.Names[0])
			continue
		}
		// inspect container
		containerDetails, ierr := cli.ContainerInspect(ctx, container.ID)
		if ierr != nil {
			log.Println(ierr.Error())
			return
		}
		// get container IP
		containerIP := containerDetails.NetworkSettings.IPAddress
		// parse container image to get crserver version
		parts := strings.Split(container.Image, ":")
		if len(parts) == 0 {
			log.Printf("Skipping container %s (image %s) because cannot detect crserver version from image name", container.Names[0], container.Image)
			continue
		}
		// add to our proxy map
		version := parts[len(parts)-1]
		portsMap[version] = containerIP
	}

	portsMapChan <- portsMap

}

// DockerConnect - connect to docker daemon and listen to changes
// returns a channel which sends maps of version-port
func DockerConnect() <-chan map[string]string {

	portsMapChan := make(chan map[string]string)

	var err error
	cli, err = client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		log.Panicln(err)
	}
	cli.NegotiateAPIVersion(context.Background())

	// initial update of containers list
	go updateContainers(portsMapChan)

	// subscribe to events
	go recoverer(1, func() { listenEvents(portsMapChan) })

	return portsMapChan

}
