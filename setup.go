package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	l "github.com/sprisa/x/log"
)

const composureNFSDriver = "composure-nfs"

type NFSVolume struct {
	Host string
	Path string
}

var runSSHFn = defaultRunSSH

func defaultRunSSH(ctx context.Context, sshURI string, command string) (string, error) {
	u, err := url.Parse(sshURI)
	if err != nil {
		return "", fmt.Errorf("invalid SSH URI %q: %w", sshURI, err)
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
	args = append(args, host, command)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func runSSHInteractive(ctx context.Context, sshURI string, command string) error {
	u, err := url.Parse(sshURI)
	if err != nil {
		return fmt.Errorf("invalid SSH URI %q: %w", sshURI, err)
	}

	host := u.Hostname()
	if port := u.Port(); port != "" {
		host = host + ":" + port
	}
	user := u.User.Username()

	args := []string{"-t", "-o", "ConnectTimeout=10"}
	if user != "" {
		args = append(args, "-l", user)
	}
	args = append(args, host, command)

	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func readNFSVolumes(project *types.Project) (map[string]NFSVolume, error) {
	result := map[string]NFSVolume{}

	for name, vol := range project.Volumes {
		if vol.Driver != composureNFSDriver {
			continue
		}

		host := vol.DriverOpts["host"]
		path := vol.DriverOpts["path"]

		if host == "" {
			return nil, fmt.Errorf("volume %q: missing required driver_opt \"host\"", name)
		}
		if path == "" {
			return nil, fmt.Errorf("volume %q: missing required driver_opt \"path\"", name)
		}

		u, err := url.Parse(host)
		if err != nil || u.Scheme != "ssh" || u.Hostname() == "" {
			return nil, fmt.Errorf("volume %q: host must be an ssh:// URI, got %q", name, host)
		}

		if !filepath.IsAbs(path) {
			return nil, fmt.Errorf("volume %q: path must be absolute, got %q", name, path)
		}

		result[name] = NFSVolume{Host: host, Path: path}
	}

	return result, nil
}

// findConsumerHosts returns a map from each NFS server host to the set of
// other hosts that use its volumes (i.e., need NFS client access).
func findConsumerHosts(project *types.Project, hostDeployment map[string][]string, nfsVolumes map[string]NFSVolume) map[string]map[string]bool {
	serviceHost := map[string]string{}
	for host, services := range hostDeployment {
		for _, svc := range services {
			serviceHost[svc] = host
		}
	}

	// Map volume name -> set of hosts that use it
	volumeUsers := map[string]map[string]bool{}
	for _, svc := range project.Services {
		svcHost, ok := serviceHost[svc.Name]
		if !ok {
			continue
		}
		for _, v := range svc.Volumes {
			if v.Type == types.VolumeTypeVolume {
				if _, isNFS := nfsVolumes[v.Source]; isNFS {
					if volumeUsers[v.Source] == nil {
						volumeUsers[v.Source] = map[string]bool{}
					}
					volumeUsers[v.Source][svcHost] = true
				}
			}
		}
	}

	consumers := map[string]map[string]bool{}
	for volName, hosts := range volumeUsers {
		serverHost := nfsVolumes[volName].Host
		for h := range hosts {
			if h == serverHost {
				continue
			}
			if consumers[serverHost] == nil {
				consumers[serverHost] = map[string]bool{}
			}
			consumers[serverHost][h] = true
		}
	}

	return consumers
}

func runSetup(ctx context.Context) error {
	project, hostDeployment, err := loadProject(ctx)
	if err != nil {
		return err
	}

	nfsVolumes, err := readNFSVolumes(project)
	if err != nil {
		return err
	}

	if len(nfsVolumes) == 0 {
		fmt.Println("No composure-nfs volumes found in compose file.")
		return nil
	}

	consumers := findConsumerHosts(project, hostDeployment, nfsVolumes)

	// Group volumes by server host
	serverVolumes := map[string][]NFSVolume{}
	for _, vol := range nfsVolumes {
		serverVolumes[vol.Host] = append(serverVolumes[vol.Host], vol)
	}

	// Create source directories on each server host
	for host, vols := range serverVolumes {
		for _, vol := range vols {
			l.Log.Info().Str("host", shortHost(host)).Str("path", vol.Path).Msg("Creating directory")
			if _, err := runSSHFn(ctx, host, fmt.Sprintf("mkdir -p %q", vol.Path)); err != nil {
				return fmt.Errorf("failed to create %s on %s: %w", vol.Path, shortHost(host), err)
			}
		}
	}

	// Resolve consumer host IPs for /etc/exports
	consumerIPs := map[string]map[string]string{} // serverHost -> clientHost -> IP
	for serverHost, clients := range consumers {
		consumerIPs[serverHost] = map[string]string{}
		for clientHost := range clients {
			ip, err := resolveHostIP(clientHost)
			if err != nil {
				return fmt.Errorf("failed to resolve IP for %s: %w", clientHost, err)
			}
			consumerIPs[serverHost][clientHost] = ip
		}
	}

	// Collect all unique hosts that need package management
	allHosts := map[string]bool{}
	for host := range serverVolumes {
		allHosts[host] = true
	}
	for _, clients := range consumers {
		for h := range clients {
			allHosts[h] = true
		}
	}

	// Detect package manager on each host upfront
	hostPkgMgr := map[string]string{}
	for host := range allHosts {
		l.Log.Info().Str("host", shortHost(host)).Msg("Detecting package manager")
		pkgMgr, err := detectPackageManagerFn(ctx, host)
		if err != nil {
			return err
		}
		hostPkgMgr[host] = pkgMgr
		l.Log.Info().Str("host", shortHost(host)).Str("package_manager", pkgMgr).Msg("Detected package manager")
	}

	// Attempt NFS server setup on each server host
	for host, vols := range serverVolumes {
		clientIPs := consumerIPs[host]
		if len(clientIPs) == 0 {
			l.Log.Info().Str("host", shortHost(host)).Msg("No cross-host consumers, skipping NFS server setup")
			continue
		}

		if err := setupNFSServer(ctx, host, hostPkgMgr[host], vols, clientIPs); err != nil {
			l.Log.Warn().Str("host", shortHost(host)).Msg("Automated setup failed, printing manual commands")
			printSetupCommands(os.Stdout, host, hostPkgMgr[host], vols, clientIPs, true)
		} else {
			l.Log.Info().Str("host", shortHost(host)).Msg("NFS server configured")
		}
	}

	// Attempt NFS client setup on each consumer host
	clientHosts := map[string]bool{}
	for _, clients := range consumers {
		for h := range clients {
			clientHosts[h] = true
		}
	}
	for host := range clientHosts {
		if err := setupNFSClient(ctx, host, hostPkgMgr[host]); err != nil {
			l.Log.Warn().Str("host", shortHost(host)).Msg("Automated setup failed, printing manual commands")
			printSetupCommands(os.Stdout, host, hostPkgMgr[host], nil, nil, false)
		} else {
			l.Log.Info().Str("host", shortHost(host)).Msg("NFS client configured")
		}
	}

	return nil
}

var detectPackageManagerFn = defaultDetectPackageManager

func defaultDetectPackageManager(ctx context.Context, sshURI string) (string, error) {
	for _, pm := range []string{"apt", "dnf", "pacman"} {
		if out, err := runSSHFn(ctx, sshURI, "which "+pm); err == nil && out != "" {
			return pm, nil
		}
	}
	return "", fmt.Errorf("no supported package manager found on %s (tried apt, dnf, pacman)", shortHost(sshURI))
}

func nfsServerInstallCmd(pkgMgr string) string {
	switch pkgMgr {
	case "dnf":
		return "dnf install -y nfs-utils"
	case "pacman":
		return "pacman -S --noconfirm nfs-utils"
	default:
		return "apt install -y nfs-kernel-server"
	}
}

func nfsServerServiceName(pkgMgr string) string {
	switch pkgMgr {
	case "dnf", "pacman":
		return "nfs-server"
	default:
		return "nfs-kernel-server"
	}
}

func nfsClientInstallCmd(pkgMgr string) string {
	switch pkgMgr {
	case "dnf":
		return "dnf install -y nfs-utils"
	case "pacman":
		return "pacman -S --noconfirm nfs-utils"
	default:
		return "apt install -y nfs-common"
	}
}

func setupNFSServer(ctx context.Context, sshURI string, pkgMgr string, vols []NFSVolume, clientIPs map[string]string) error {
	var cmds []string
	cmds = append(cmds, nfsServerInstallCmd(pkgMgr))

	for _, vol := range vols {
		for _, ip := range clientIPs {
			exportLine := fmt.Sprintf("%s %s(rw,sync,no_subtree_check,no_root_squash)", vol.Path, ip)
			cmds = append(cmds, fmt.Sprintf("grep -qF %q /etc/exports || echo %q >> /etc/exports", exportLine, exportLine))
		}
	}
	cmds = append(cmds, "exportfs -ra")
	cmds = append(cmds, fmt.Sprintf("systemctl enable --now %s", nfsServerServiceName(pkgMgr)))

	script := strings.Join(cmds, " && ")
	return runSSHInteractive(ctx, sshURI, fmt.Sprintf("sudo bash -c '%s'", script))
}

func setupNFSClient(ctx context.Context, sshURI string, pkgMgr string) error {
	return runSSHInteractive(ctx, sshURI, fmt.Sprintf("sudo bash -c '%s'", nfsClientInstallCmd(pkgMgr)))
}

func printSetupCommands(w io.Writer, sshURI string, pkgMgr string, vols []NFSVolume, clientIPs map[string]string, isServer bool) {
	u, _ := url.Parse(sshURI)
	host := u.Hostname()
	user := u.User.Username()

	sshTarget := host
	if user != "" {
		sshTarget = user + "@" + host
	}

	fmt.Fprintf(w, "\n  ssh %s\n", sshTarget)

	if isServer {
		fmt.Fprintf(w, "  sudo %s\n", nfsServerInstallCmd(pkgMgr))
		for _, vol := range vols {
			for _, ip := range clientIPs {
				fmt.Fprintf(w, "  echo '%s %s(rw,sync,no_subtree_check,no_root_squash)' | sudo tee -a /etc/exports\n", vol.Path, ip)
			}
		}
		fmt.Fprintf(w, "  sudo exportfs -ra\n")
		fmt.Fprintf(w, "  sudo systemctl enable --now %s\n", nfsServerServiceName(pkgMgr))
	} else {
		fmt.Fprintf(w, "  sudo %s\n", nfsClientInstallCmd(pkgMgr))
	}
	fmt.Fprintln(w)
}
