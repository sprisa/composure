# Composure

> Deploy docker-compose files to multiple hosts via SSH with Composure 

Composure is a simple CLI tool for deploying Docker Compose applications across multiple remote servers via SSH. No Kubernetes, no Swarm — just your familiar docker-compose.yml files deployed exactly where you want them.

## Why Composure?  

You have a docker-compose.yml file. You have multiple servers. You want your application running on all of them without the complexity of Kubernetes or the overhead of a full orchestration platform.  

Composure keeps it simple:  
- Uses your existing docker-compose.yml files
- Deploys over SSH—no agents, no daemons
- Supports server-specific configurations
- Parallel deployments for speed


## Install

**Homebrew (auto updates)**
```sh
brew install sprisa/tap/composure
```

**NPM**
```sh
npm i -g @sprisa/composure
# Or run directly
npx @sprisa/composure
```

**Golang Source**
```sh
go install github.com/sprisa/composure@latest
# Or run directly
go run github.com/sprisa/composure@latest
```


## Usage

Add a composure label to your docker-compose services. This described which SSH host to deploy the docker service to. This can be any [`DOCKER_HOST`](https://docs.docker.com/reference/cli/docker/#environment-variables) variable.


```yaml
services:
  # Simple Hello World application
  hello-world:
    image: crccheck/hello-world
    ports:
      - "8081:8000"
    restart: unless-stopped
    labels:
      composure: ssh://gabe@jackfruit.local # <- Deploy to jackfruit server. Can be any IP or DNS name

  # Nginx reverse proxy
  nginx:
    image: nginx:alpine
    ports:
      - "8080:80"
    restart: unless-stopped
    labels:
      composure: ssh://gabe@mango.local # <- Deploy to mango server. Can be any IP or DNS name
```


Now we can deploy with `composure up`. This accepts the same flags as `docker compose up`.

```sh
# Deploy in background
composure up -d 

# Destroy services
compose down
```


## Options

Show Help
```sh
~ ❯ composure
Composure - Calm docker compose deployments

Usage: composure <command>

Commands:
  up       - Start services
  down     - Stop services
  restart  - Restart services
  help     - Show this help message
```



## Recommendations  

### Use absolute paths for Volumes

Docker resolves relative paths like `./` or `~` based the machine you ran `composure up` from, **not** the host target machine. It's recommended to use explicit absolute paths for portability.

*Assuming username is `ubuntu`*  
```yaml
services:
  plex:
    image: lscr.io/linuxserver/plex:latest
    ports:
      - 32400:32400
    # Use absolute paths instead of related paths
    volumes:
      - /home/ubuntu/plex/library:/config
      # The below is not as portable
      # - ~/plex/library:/config
    labels:
      composure: ssh://ubuntu@jackfruit.dvc.link
```


## Multi-Host Networking

Composure automatically enables cross-host service discovery. When you deploy services to different hosts, each service gets `extra_hosts` entries injected so it can resolve other services by name.

For example, given services `hello-world` on `jackfruit.local` and `nginx` on `mango.local`:
- `hello-world` can reach `nginx` at `nginx:<published-port>`
- `nginx` can reach `hello-world` at `hello-world:<published-port>`

Composure resolves each SSH host's IP at deploy time and adds the mappings automatically. Services on the same host are skipped since they already share Docker's internal network. Any existing `extra_hosts` you define are preserved.

> **Note:** Services communicate over the host network using published ports, not container ports. Ensure the relevant ports are exposed and accessible between hosts.


## Not Yet Supported
- Cross host volumes
- Cross host `service.depends_on`
