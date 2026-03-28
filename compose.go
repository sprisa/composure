package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"

	composeCli "github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	l "github.com/sprisa/x/log"
	"golang.org/x/sync/errgroup"
)

func runComposeCmd(ctx context.Context, cmd string, flags []string) error {
	// TODO: Use compose file from -f if available well
	composeFile, err := findComposeFile()
	if err != nil {
		return err
	}

	options, err := composeCli.NewProjectOptions(
		[]string{composeFile},
	)
	if err != nil {
		return err
	}

	project, err := options.LoadProject(ctx)
	if err != nil {
		return err
	}

	hostDeployment := map[string][]string{}
	for _, service := range project.Services {
		host, hasHost := service.Labels["composure"]
		if !hasHost {
			continue
		}

		deploys, hasDeployment := hostDeployment[host]
		if !hasDeployment {
			deploys = []string{}
		}

		deploys = append(deploys, service.Name)
		hostDeployment[host] = deploys
	}

	if err := injectExtraHosts(project, hostDeployment); err != nil {
		return err
	}

	composeYAML, err := project.MarshalYAML()
	if err != nil {
		return err
	}

	group, ctx := errgroup.WithContext(ctx)

	for host, services := range hostDeployment {
		group.Go(func() error {
			err := runDockerCompose(ctx, host, composeYAML, cmd, flags, services)
			if err != nil {
				l.Log.Err(err).
					Str("host", host).
					Msg("Deployment failed")
				return err
			}
			return nil
		})
	}

	return group.Wait()
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
