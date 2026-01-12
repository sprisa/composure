package main

import (
	"context"
	"errors"
	"os"
	"os/exec"

	composeCli "github.com/compose-spec/compose-go/v2/cli"
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

	group, ctx := errgroup.WithContext(ctx)

	for host, services := range hostDeployment {
		group.Go(func() error {
			err := runDockerCompose(ctx, host, composeFile, cmd, flags, services)
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
	file string,
	composeCmd string,
	flags []string,
	services []string,
) error {
	args := []string{
		"compose",
		"-f", file,
		composeCmd,
	}
	args = append(args, flags...)
	args = append(args, services...)

	cmd := exec.CommandContext(
		ctx,
		"docker",
		args...,
	)

	// Pass through stdout and stderr
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
