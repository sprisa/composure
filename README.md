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


