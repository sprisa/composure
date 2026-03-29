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
  plan     - Show deployment plan
  setup    - Setup shared volumes (NFS)
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


## Shared Volumes (NFS)

When services on different hosts need access to the same data, Composure can share volumes over NFS. Declare shared volumes using the `composure-nfs` driver in the top-level `volumes` block:

```yaml
volumes:
  media:
    driver: composure-nfs
    driver_opts:
      host: ssh://gabe@coconut.dvc.link   # Host that owns the data
      path: /home/gabe/lib/plex            # Absolute path on that host

services:
  plex:
    volumes:
      - media:/data
    labels:
      composure: ssh://gabe@coconut.dvc.link

  overseerr:
    volumes:
      - media:/data
    labels:
      composure: ssh://gabe@kiwi.dvc.link
```

During deployment, Composure rewrites these volumes per-host:
- On the **source host** (`coconut`), the named volume becomes a local bind mount to `/home/gabe/lib/plex`
- On **other hosts** (`kiwi`), it becomes an NFS mount pointing to `coconut`

### One-time setup

Before deploying, run `composure setup` to install NFS packages and configure exports. It will SSH into each host, create the source directories, and attempt to install packages via `sudo`. If `sudo` requires a password, you'll be prompted interactively. If it fails, the commands are printed for you to run manually.

```sh
composure setup
```

### Rendered Compose Files

Each time you deploy, Composure writes the rendered docker-compose YAML to `~/.config/composure/<project>.yml` on each remote host. This is the final YAML after NFS volume rewriting and host filtering — exactly what `docker compose` sees. You can inspect it at any time:

```sh
ssh gabe@kiwi cat ~/.config/composure/plex.yml
```

### Debugging NFS

If NFS mounts aren't working after setup, verify the configuration on the server host:

```sh
# Check the NFS server service is running
sudo systemctl status nfs-server        # Fedora/Arch
sudo systemctl status nfs-kernel-server # Debian/Ubuntu

# View configured exports
cat /etc/exports

# View active exports
sudo exportfs -v

# Restart and re-export if needed
sudo exportfs -ra
sudo systemctl restart nfs-server
```

From a client host, verify the server is reachable:

```sh
# List exports available from the server
showmount -e <server-hostname>

# Test mount manually
sudo mkdir -p /tmp/nfs-test
sudo mount -t nfs4 <server-hostname>:/path/to/share /tmp/nfs-test
ls /tmp/nfs-test
sudo umount /tmp/nfs-test
```

## Not Yet Supported
- Cross host `service.depends_on`
