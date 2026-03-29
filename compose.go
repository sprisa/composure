package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	composeCli "github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	l "github.com/sprisa/x/log"
)

func loadProject(ctx context.Context) (*types.Project, map[string][]string, error) {
	// TODO: Use compose file from -f if available well
	composeFile, err := findComposeFile()
	if err != nil {
		return nil, nil, err
	}

	options, err := composeCli.NewProjectOptions(
		[]string{composeFile},
	)
	if err != nil {
		return nil, nil, err
	}

	project, err := options.LoadProject(ctx)
	if err != nil {
		return nil, nil, err
	}

	hostDeployment := map[string][]string{}
	for _, service := range project.Services {
		host, hasHost := service.Labels["composure"]
		if !hasHost {
			continue
		}
		hostDeployment[host] = append(hostDeployment[host], service.Name)
	}

	return project, hostDeployment, nil
}

func runComposeCmd(ctx context.Context, cmd string, flags []string) error {
	project, hostDeployment, err := loadProject(ctx)
	if err != nil {
		return err
	}

	if err := checkHostsConnectivity(ctx, hostDeployment); err != nil {
		return err
	}

	if err := injectExtraHosts(project, hostDeployment); err != nil {
		return err
	}

	reverse := cmd == "down"
	// Foreground "up" (no -d) streams logs and blocks forever, so we launch
	// all levels' goroutines without waiting between them. Otherwise the first
	// level would block and subsequent hosts would never deploy.
	blockBetweenLevels := !(cmd == "up" && !hasDetachFlag(flags))
	return deployInOrder(ctx, project, hostDeployment, cmd, flags, reverse, blockBetweenLevels)
}

func hasDetachFlag(flags []string) bool {
	for _, f := range flags {
		if f == "-d" || f == "--detach" {
			return true
		}
	}
	return false
}

func shortHost(sshURI string) string {
	if u, err := url.Parse(sshURI); err == nil && u.Hostname() != "" {
		return u.Hostname()
	}
	return sshURI
}

func runPlanCmd(ctx context.Context) error {
	project, hostDeployment, err := loadProject(ctx)
	if err != nil {
		return err
	}
	return writePlan(os.Stdout, project, hostDeployment)
}

func writePlan(w io.Writer, project *types.Project, hostDeployment map[string][]string) error {
	serviceHost := map[string]string{}
	for host, services := range hostDeployment {
		for _, svc := range services {
			serviceHost[svc] = host
		}
	}

	hosts := make([]string, 0, len(hostDeployment))
	for h := range hostDeployment {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)

	for i, host := range hosts {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "Host: %s\n", host)

		services := make([]string, len(hostDeployment[host]))
		copy(services, hostDeployment[host])
		sort.Strings(services)

		for _, name := range services {
			svc := project.Services[name]
			fmt.Fprintf(w, "  %s\n", name)
			if svc.Image != "" {
				fmt.Fprintf(w, "    image:   %s\n", svc.Image)
			}
			if svc.NetworkMode != "" {
				fmt.Fprintf(w, "    network: %s\n", svc.NetworkMode)
			}
			if len(svc.Volumes) > 0 {
				vols := make([]string, len(svc.Volumes))
				for j, v := range svc.Volumes {
					vols[j] = v.String()
				}
				fmt.Fprintf(w, "    volumes: %s\n", strings.Join(vols, ", "))
			}
		}
	}

	// Show composure-nfs volumes
	var nfsLines []string
	for name, vol := range project.Volumes {
		if vol.Driver != composureNFSDriver {
			continue
		}
		host := vol.DriverOpts["host"]
		path := vol.DriverOpts["path"]

		var users []string
		for _, svc := range project.Services {
			for _, v := range svc.Volumes {
				if v.Type == types.VolumeTypeVolume && v.Source == name {
					svcHost := serviceHost[svc.Name]
					loc := "local"
					if svcHost != host {
						loc = "nfs"
					}
					users = append(users, fmt.Sprintf("%s (%s)", svc.Name, loc))
				}
			}
		}
		sort.Strings(users)
		nfsLines = append(nfsLines, fmt.Sprintf("  %s: %s on %s -> %s",
			name, path, shortHost(host), strings.Join(users, ", ")))
	}
	if len(nfsLines) > 0 {
		sort.Strings(nfsLines)
		fmt.Fprintf(w, "\nShared volumes (NFS):\n")
		for _, line := range nfsLines {
			fmt.Fprintln(w, line)
		}
	}

	var crossDeps []string
	for _, svc := range project.Services {
		myHost, ok := serviceHost[svc.Name]
		if !ok {
			continue
		}
		for dep := range svc.DependsOn {
			depHost, ok := serviceHost[dep]
			if !ok || depHost == myHost {
				continue
			}
			crossDeps = append(crossDeps,
				fmt.Sprintf("  %s (%s) -> %s (%s)", svc.Name, shortHost(myHost), dep, shortHost(depHost)))
		}
	}
	if len(crossDeps) > 0 {
		sort.Strings(crossDeps)
		fmt.Fprintf(w, "\nCross-host dependencies:\n")
		for _, line := range crossDeps {
			fmt.Fprintln(w, line)
		}
	}

	levels, err := buildHostDeployOrder(project, hostDeployment)
	if err != nil {
		return err
	}

	fmt.Fprintf(w, "\nDeploy order:\n")
	for i, level := range levels {
		label := "parallel"
		if len(level) == 1 {
			label = "single"
		}
		fmt.Fprintf(w, "  Level %d (%s): %s\n", i+1, label, strings.Join(level, ", "))
	}

	return nil
}

var deployToHostFn = defaultDeployToHost

func defaultDeployToHost(ctx context.Context, project *types.Project, host string, services []string, cmd string, flags []string) error {
	filtered := filterProjectForHost(project, services)
	hasNFS := rewriteNFSVolumes(filtered, host)
	composeYAML, err := filtered.MarshalYAML()
	if err != nil {
		return fmt.Errorf("host %s: failed to marshal compose YAML: %w", host, err)
	}
	if err := writeRemoteComposeFile(ctx, host, filtered.Name, composeYAML); err != nil {
		l.Log.Warn().Err(err).Str("host", host).Msg("Failed to write remote compose file")
	}
	err = runDockerCompose(ctx, host, composeYAML, cmd, flags, services)
	if err != nil && hasNFS {
		l.Log.Warn().Str("host", host).Msg("Deployment failed with NFS volumes — did you run 'composure setup'?")
	}
	return err
}

func deployInOrder(ctx context.Context, project *types.Project, hostDeployment map[string][]string, cmd string, flags []string, reverse bool, blockBetweenLevels bool) error {
	levels, err := buildHostDeployOrder(project, hostDeployment)
	if err != nil {
		return err
	}

	if reverse {
		for i, j := 0, len(levels)-1; i < j; i, j = i+1, j-1 {
			levels[i], levels[j] = levels[j], levels[i]
		}
	}

	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	for _, level := range levels {
		if ctx.Err() != nil {
			break
		}

		for _, host := range level {
			services := hostDeployment[host]
			wg.Go(func() {
				if err := deployToHostFn(ctx, project, host, services, cmd, flags); err != nil {
					l.Log.Err(err).Str("host", host).Msg("Deployment failed")
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			})
		}

		// For foreground "up" (no -d), don't block between levels so all hosts
		// can stream logs simultaneously. For "down", "restart", and "up -d",
		// wait for each level to finish before starting the next.
		if blockBetweenLevels {
			wg.Wait()
			if err := errors.Join(errs...); err != nil {
				return err
			}
		}
	}

	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	return ctx.Err()
}

var connectivityCheckFn = defaultConnectivityCheck

func defaultConnectivityCheck(ctx context.Context, host string) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "docker", "ps")
	cmd.Env = append(os.Environ(), "DOCKER_HOST="+host)
	cmd.Stdout = nil
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%s: %w", strings.TrimSpace(stderr.String()), err)
		}
		return err
	}
	return nil
}

func checkHostsConnectivity(ctx context.Context, hostDeployment map[string][]string) error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	for host := range hostDeployment {
		wg.Go(func() {
			if err := checkHostConnectivity(ctx, host); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}

	wg.Wait()
	return errors.Join(errs...)
}

func checkHostConnectivity(ctx context.Context, host string) error {
	l.Log.Info().Str("host", host).Msg("Checking connectivity")

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	if err := connectivityCheckFn(ctx, host); err != nil {
		return fmt.Errorf("host %s is unreachable: %w", host, err)
	}

	l.Log.Info().Str("host", host).Msg("Host is reachable")
	return nil
}

func writeRemoteComposeFile(ctx context.Context, sshURI string, projectName string, composeYAML []byte) error {
	u, err := url.Parse(sshURI)
	if err != nil {
		return fmt.Errorf("invalid SSH URI %q: %w", sshURI, err)
	}

	host := u.Hostname()
	if port := u.Port(); port != "" {
		host = host + ":" + port
	}
	user := u.User.Username()

	args := []string{"-o", "ConnectTimeout=10"}
	if user != "" {
		args = append(args, "-l", user)
	}
	args = append(args, host, fmt.Sprintf("mkdir -p ~/.config/composure && cat > ~/.config/composure/%s.yml", projectName))

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = bytes.NewReader(composeYAML)

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to write compose file: %s: %w", strings.TrimSpace(string(out)), err)
	}

	return nil
}

func runDockerCompose(
	ctx context.Context,
	host string,
	composeYAML []byte,
	composeCmd string,
	flags []string,
	services []string,
) error {
	args := []string{
		"compose",
		"-f", "-",
		composeCmd,
	}
	args = append(args, flags...)
	args = append(args, services...)

	cmd := exec.CommandContext(
		ctx,
		"docker",
		args...,
	)

	cmd.Stdin = bytes.NewReader(composeYAML)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(
		os.Environ(),
		"DOCKER_HOST="+host,
	)

	l.Log.Info().
		Str("host", host).
		Strs("services", services).
		Msg(cmd.String())

	return cmd.Run()
}

func resolveHostIP(sshURI string) (string, error) {
	u, err := url.Parse(sshURI)
	if err != nil {
		return "", err
	}

	hostname := u.Hostname()

	addrs, err := net.LookupHost(hostname)
	if err != nil {
		return hostname, nil
	}

	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip != nil && ip.To4() != nil {
			return addr, nil
		}
	}

	if len(addrs) > 0 {
		return addrs[0], nil
	}

	return hostname, nil
}

func injectExtraHosts(project *types.Project, hostDeployment map[string][]string) error {
	hostIPs := map[string]string{}
	for host := range hostDeployment {
		ip, err := resolveHostIP(host)
		if err != nil {
			return err
		}
		hostIPs[host] = ip
	}

	serviceHost := map[string]string{}
	for host, services := range hostDeployment {
		for _, svc := range services {
			serviceHost[svc] = host
		}
	}

	for name, service := range project.Services {
		myHost := serviceHost[name]

		if strings.HasPrefix(service.NetworkMode, "service:") ||
			strings.HasPrefix(service.NetworkMode, "container:") {
			l.Log.Warn().
				Str("service", name).
				Str("network_mode", service.NetworkMode).
				Msg("Skipping extra_hosts injection for service with shared network namespace")
			continue
		}

		if service.ExtraHosts == nil {
			service.ExtraHosts = types.HostsList{}
		}

		for otherName, otherHost := range serviceHost {
			if otherHost == myHost {
				continue
			}
			if _, exists := service.ExtraHosts[otherName]; !exists {
				service.ExtraHosts[otherName] = []string{hostIPs[otherHost]}
			}
		}

		project.Services[name] = service
	}

	return nil
}

// rewriteNFSVolumes modifies the project in-place, converting composure-nfs
// volumes for the given host. On the source host the named volume becomes a
// bind mount; on other hosts it becomes a real NFS volume. Returns true if
// any composure-nfs volumes were present.
func rewriteNFSVolumes(project *types.Project, host string) bool {
	nfsVols := map[string]NFSVolume{}
	for name, vol := range project.Volumes {
		if vol.Driver != composureNFSDriver {
			continue
		}
		nfsVols[name] = NFSVolume{Host: vol.DriverOpts["host"], Path: vol.DriverOpts["path"]}
	}
	if len(nfsVols) == 0 {
		return false
	}

	for name, nfs := range nfsVols {
		if nfs.Host == host {
			// Source host: convert service volume references to bind mounts
			for svcName, svc := range project.Services {
				for i, v := range svc.Volumes {
					if v.Type == types.VolumeTypeVolume && v.Source == name {
						svc.Volumes[i].Type = types.VolumeTypeBind
						svc.Volumes[i].Source = nfs.Path
					}
				}
				project.Services[svcName] = svc
			}
			delete(project.Volumes, name)
		} else {
			// Remote host: rewrite top-level volume to NFS driver
			ip, err := resolveHostIP(nfs.Host)
			if err != nil {
				ip = shortHost(nfs.Host)
			}
			project.Volumes[name] = types.VolumeConfig{
				Name:   name,
				Driver: "local",
				DriverOpts: types.Options{
					"type":   "nfs",
					"o":      fmt.Sprintf("addr=%s,rw,nfsvers=4", ip),
					"device": ":" + nfs.Path,
				},
			}
		}
	}

	return true
}

func filterProjectForHost(project *types.Project, services []string) *types.Project {
	keep := map[string]bool{}
	for _, s := range services {
		keep[s] = true
	}

	filtered := *project
	filtered.Services = types.Services{}
	// Deep-copy Volumes so concurrent rewriteNFSVolumes calls per host
	// don't race on the same map (the struct copy above is shallow).
	filtered.Volumes = types.Volumes{}
	maps.Copy(filtered.Volumes, project.Volumes)

	for _, name := range services {
		svc, exists := project.Services[name]
		if !exists {
			continue
		}

		if svc.DependsOn != nil {
			cleanDeps := types.DependsOnConfig{}
			for dep, config := range svc.DependsOn {
				if keep[dep] {
					cleanDeps[dep] = config
				}
			}
			svc.DependsOn = cleanDeps
		}

		if after, ok := strings.CutPrefix(svc.NetworkMode, "service:"); ok {
			ref := after
			if !keep[ref] {
				svc.NetworkMode = ""
			}
		}

		filtered.Services[name] = svc
	}

	return &filtered
}

func buildHostDeployOrder(project *types.Project, hostDeployment map[string][]string) ([][]string, error) {
	serviceHost := map[string]string{}
	for host, services := range hostDeployment {
		for _, svc := range services {
			serviceHost[svc] = host
		}
	}

	// deps[hostA] = {hostB: true} means A must wait for B
	deps := map[string]map[string]bool{}
	// crossHostEdges tracks service-level reasons: "service -> dep"
	crossHostEdges := map[string]map[string][]string{}
	for host := range hostDeployment {
		deps[host] = map[string]bool{}
		crossHostEdges[host] = map[string][]string{}
	}

	for _, service := range project.Services {
		myHost, ok := serviceHost[service.Name]
		if !ok {
			continue
		}
		for dep := range service.DependsOn {
			depHost, ok := serviceHost[dep]
			if !ok || depHost == myHost {
				continue
			}
			deps[myHost][depHost] = true
			crossHostEdges[myHost][depHost] = append(
				crossHostEdges[myHost][depHost],
				fmt.Sprintf("%s -> %s", service.Name, dep),
			)
		}
	}

	inDegree := map[string]int{}
	dependents := map[string][]string{}
	for host := range hostDeployment {
		inDegree[host] = len(deps[host])
	}
	for host, hostDeps := range deps {
		for dep := range hostDeps {
			dependents[dep] = append(dependents[dep], host)
		}
	}

	var levels [][]string
	var queue []string
	for host, degree := range inDegree {
		if degree == 0 {
			queue = append(queue, host)
		}
	}

	processed := 0
	for len(queue) > 0 {
		level := make([]string, len(queue))
		copy(level, queue)
		sort.Strings(level)
		levels = append(levels, level)
		processed += len(level)

		var next []string
		for _, host := range level {
			for _, dependent := range dependents[host] {
				inDegree[dependent]--
				if inDegree[dependent] == 0 {
					next = append(next, dependent)
				}
			}
		}
		queue = next
	}

	if processed != len(hostDeployment) {
		cycleHostSet := map[string]bool{}
		for host, degree := range inDegree {
			if degree > 0 {
				cycleHostSet[host] = true
			}
		}

		var edgeLines []string
		for from, targets := range crossHostEdges {
			if !cycleHostSet[from] {
				continue
			}
			for to, reasons := range targets {
				if !cycleHostSet[to] {
					continue
				}
				for _, r := range reasons {
					edgeLines = append(edgeLines, fmt.Sprintf("  %s (%s depends on %s)", r, from, to))
				}
			}
		}
		sort.Strings(edgeLines)

		l.Log.Warn().Msg(fmt.Sprintf("Circular cross-host dependency detected, deploying these hosts in parallel:\n%s", strings.Join(edgeLines, "\n")))

		var remaining []string
		for host := range cycleHostSet {
			remaining = append(remaining, host)
		}
		sort.Strings(remaining)
		levels = append(levels, remaining)
	}

	return levels, nil
}

func findComposeFile() (string, error) {
	// Try common compose file names in order
	files := []string{
		"docker-compose.yml",
		"docker-compose.yaml",
		"compose.yml",
		"compose.yaml",
	}

	for _, filename := range files {
		if _, err := os.Stat(filename); err == nil {
			return filename, nil
		}
	}

	return "", errors.New("no compose file found")
}
