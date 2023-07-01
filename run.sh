#!/bin/bash

docker run \
    -d --restart unless-stopped \
    -p 8080:80 \
    --name crserver-proxy \
    -v /var/run/docker.sock:/var/run/docker.sock:ro \
    magister/crserver-proxy
