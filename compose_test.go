package main

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

func TestResolveHostIP(t *testing.T) {
	tests := []struct {
		name    string
		sshURI  string
		wantErr bool
	}{
		{
			name:   "localhost resolves",
			sshURI: "ssh://user@localhost",
		},
		{
			name:   "IP address passthrough",
			sshURI: "ssh://user@127.0.0.1",
		},
		{
			name:   "with port",
			sshURI: "ssh://user@localhost:2222",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, err := resolveHostIP(tt.sshURI)
			if (err != nil) != tt.wantErr {
				t.Errorf("resolveHostIP(%q) error = %v, wantErr %v", tt.sshURI, err, tt.wantErr)
				return
			}
			if ip == "" {
				t.Errorf("resolveHostIP(%q) returned empty string", tt.sshURI)
			}
		})
	}
}

func TestInjectExtraHosts(t *testing.T) {
	t.Run("two services on different hosts", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name:   "web",
					Labels: types.Labels{"composure": "ssh://user@host-a"},
				},
				"api": types.ServiceConfig{
					Name:   "api",
					Labels: types.Labels{"composure": "ssh://user@host-b"},
				},
			},
		}

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		err := injectExtraHosts(project, hostDeployment)
		if err != nil {
			t.Fatalf("injectExtraHosts() error = %v", err)
		}

		webHosts := project.Services["web"].ExtraHosts
		if _, ok := webHosts["api"]; !ok {
			t.Error("web service should have extra_hosts entry for api")
		}

		apiHosts := project.Services["api"].ExtraHosts
		if _, ok := apiHosts["web"]; !ok {
			t.Error("api service should have extra_hosts entry for web")
		}
	})

	t.Run("same host services do not get extra_hosts", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name:   "web",
					Labels: types.Labels{"composure": "ssh://user@host-a"},
				},
				"worker": types.ServiceConfig{
					Name:   "worker",
					Labels: types.Labels{"composure": "ssh://user@host-a"},
				},
			},
		}

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web", "worker"},
		}

		err := injectExtraHosts(project, hostDeployment)
		if err != nil {
			t.Fatalf("injectExtraHosts() error = %v", err)
		}

		webHosts := project.Services["web"].ExtraHosts
		if len(webHosts) != 0 {
			t.Errorf("web should have no extra_hosts, got %v", webHosts)
		}

		workerHosts := project.Services["worker"].ExtraHosts
		if len(workerHosts) != 0 {
			t.Errorf("worker should have no extra_hosts, got %v", workerHosts)
		}
	})

	t.Run("preserves existing extra_hosts", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name:       "web",
					Labels:     types.Labels{"composure": "ssh://user@host-a"},
					ExtraHosts: types.HostsList{"custom": []string{"10.0.0.1"}},
				},
				"api": types.ServiceConfig{
					Name:   "api",
					Labels: types.Labels{"composure": "ssh://user@host-b"},
				},
			},
		}

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		err := injectExtraHosts(project, hostDeployment)
		if err != nil {
			t.Fatalf("injectExtraHosts() error = %v", err)
		}

		webHosts := project.Services["web"].ExtraHosts
		if _, ok := webHosts["custom"]; !ok {
			t.Error("web service should still have custom extra_hosts entry")
		}
		if _, ok := webHosts["api"]; !ok {
			t.Error("web service should have extra_hosts entry for api")
		}
	})

	t.Run("three hosts cross-host entries", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"a": types.ServiceConfig{
					Name:   "a",
					Labels: types.Labels{"composure": "ssh://user@host-1"},
				},
				"b": types.ServiceConfig{
					Name:   "b",
					Labels: types.Labels{"composure": "ssh://user@host-2"},
				},
				"c": types.ServiceConfig{
					Name:   "c",
					Labels: types.Labels{"composure": "ssh://user@host-3"},
				},
			},
		}

		hostDeployment := map[string][]string{
			"ssh://user@host-1": {"a"},
			"ssh://user@host-2": {"b"},
			"ssh://user@host-3": {"c"},
		}

		err := injectExtraHosts(project, hostDeployment)
		if err != nil {
			t.Fatalf("injectExtraHosts() error = %v", err)
		}

		aHosts := project.Services["a"].ExtraHosts
		if len(aHosts) != 2 {
			t.Errorf("service a should have 2 extra_hosts entries, got %d: %v", len(aHosts), aHosts)
		}

		bHosts := project.Services["b"].ExtraHosts
		if len(bHosts) != 2 {
			t.Errorf("service b should have 2 extra_hosts entries, got %d: %v", len(bHosts), bHosts)
		}

		cHosts := project.Services["c"].ExtraHosts
		if len(cHosts) != 2 {
			t.Errorf("service c should have 2 extra_hosts entries, got %d: %v", len(cHosts), cHosts)
		}
	})
}

func stubConnectivityCheck(failHosts map[string]bool) func(ctx context.Context, host string) error {
	return func(ctx context.Context, host string) error {
		if failHosts[host] {
			return fmt.Errorf("connection refused")
		}
		return nil
	}
}

func TestCheckHostsConnectivity(t *testing.T) {
	t.Run("all hosts reachable", func(t *testing.T) {
		original := connectivityCheckFn
		defer func() { connectivityCheckFn = original }()
		connectivityCheckFn = stubConnectivityCheck(nil)

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		err := checkHostsConnectivity(context.Background(), hostDeployment)
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("single host unreachable", func(t *testing.T) {
		original := connectivityCheckFn
		defer func() { connectivityCheckFn = original }()
		connectivityCheckFn = stubConnectivityCheck(map[string]bool{
			"ssh://user@host-b": true,
		})

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		err := checkHostsConnectivity(context.Background(), hostDeployment)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "host-b") {
			t.Errorf("error should mention host-b, got: %v", err)
		}
	})

	t.Run("multiple hosts unreachable reports all", func(t *testing.T) {
		original := connectivityCheckFn
		defer func() { connectivityCheckFn = original }()
		connectivityCheckFn = stubConnectivityCheck(map[string]bool{
			"ssh://user@host-a": true,
			"ssh://user@host-c": true,
		})

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
			"ssh://user@host-c": {"db"},
		}

		err := checkHostsConnectivity(context.Background(), hostDeployment)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		errMsg := err.Error()
		if !strings.Contains(errMsg, "host-a") {
			t.Errorf("error should mention host-a, got: %v", errMsg)
		}
		if !strings.Contains(errMsg, "host-c") {
			t.Errorf("error should mention host-c, got: %v", errMsg)
		}
		if strings.Contains(errMsg, "host-b") {
			t.Errorf("error should not mention host-b (it succeeded), got: %v", errMsg)
		}
	})

	t.Run("all hosts unreachable", func(t *testing.T) {
		original := connectivityCheckFn
		defer func() { connectivityCheckFn = original }()
		connectivityCheckFn = stubConnectivityCheck(map[string]bool{
			"ssh://user@host-a": true,
			"ssh://user@host-b": true,
		})

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		err := checkHostsConnectivity(context.Background(), hostDeployment)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		errMsg := err.Error()
		if !strings.Contains(errMsg, "host-a") {
			t.Errorf("error should mention host-a, got: %v", errMsg)
		}
		if !strings.Contains(errMsg, "host-b") {
			t.Errorf("error should mention host-b, got: %v", errMsg)
		}
	})

	t.Run("empty deployment succeeds", func(t *testing.T) {
		original := connectivityCheckFn
		defer func() { connectivityCheckFn = original }()
		connectivityCheckFn = stubConnectivityCheck(nil)

		err := checkHostsConnectivity(context.Background(), map[string][]string{})
		if err != nil {
			t.Errorf("expected no error for empty deployment, got: %v", err)
		}
	})
}

func TestCheckHostConnectivity(t *testing.T) {
	t.Run("reachable host returns nil", func(t *testing.T) {
		original := connectivityCheckFn
		defer func() { connectivityCheckFn = original }()
		connectivityCheckFn = stubConnectivityCheck(nil)

		err := checkHostConnectivity(context.Background(), "ssh://user@host-a")
		if err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("unreachable host wraps error with host name", func(t *testing.T) {
		original := connectivityCheckFn
		defer func() { connectivityCheckFn = original }()
		connectivityCheckFn = stubConnectivityCheck(map[string]bool{
			"ssh://user@host-a": true,
		})

		err := checkHostConnectivity(context.Background(), "ssh://user@host-a")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "ssh://user@host-a") {
			t.Errorf("error should contain the host URI, got: %v", err)
		}
		if !strings.Contains(err.Error(), "unreachable") {
			t.Errorf("error should say unreachable, got: %v", err)
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		original := connectivityCheckFn
		defer func() { connectivityCheckFn = original }()
		connectivityCheckFn = func(ctx context.Context, host string) error {
			<-ctx.Done()
			return ctx.Err()
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := checkHostConnectivity(ctx, "ssh://user@host-a")
		if err == nil {
			t.Fatal("expected error from cancelled context, got nil")
		}
	})
}

func TestFilterProjectForHost(t *testing.T) {
	t.Run("keeps only specified services", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web":    types.ServiceConfig{Name: "web"},
				"api":    types.ServiceConfig{Name: "api"},
				"worker": types.ServiceConfig{Name: "worker"},
			},
		}

		filtered := filterProjectForHost(project, []string{"web", "api"})

		if len(filtered.Services) != 2 {
			t.Fatalf("expected 2 services, got %d", len(filtered.Services))
		}
		if _, ok := filtered.Services["web"]; !ok {
			t.Error("expected web service")
		}
		if _, ok := filtered.Services["api"]; !ok {
			t.Error("expected api service")
		}
		if _, ok := filtered.Services["worker"]; ok {
			t.Error("worker should have been removed")
		}
	})

	t.Run("does not mutate original project", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
				"api": types.ServiceConfig{Name: "api"},
			},
		}

		_ = filterProjectForHost(project, []string{"web"})

		if len(project.Services) != 2 {
			t.Fatalf("original project should still have 2 services, got %d", len(project.Services))
		}
	})

	t.Run("strips cross-host depends_on", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name: "web",
					DependsOn: types.DependsOnConfig{
						"api":    types.ServiceDependency{},
						"worker": types.ServiceDependency{},
					},
				},
				"api":    types.ServiceConfig{Name: "api"},
				"worker": types.ServiceConfig{Name: "worker"},
			},
		}

		filtered := filterProjectForHost(project, []string{"web", "api"})

		webDeps := filtered.Services["web"].DependsOn
		if _, ok := webDeps["api"]; !ok {
			t.Error("web should still depend on api (same host)")
		}
		if _, ok := webDeps["worker"]; ok {
			t.Error("web should not depend on worker (different host)")
		}
	})

	t.Run("preserves local depends_on", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"transmission": types.ServiceConfig{
					Name: "transmission",
					DependsOn: types.DependsOnConfig{
						"wireguard": types.ServiceDependency{},
					},
				},
				"wireguard": types.ServiceConfig{Name: "wireguard"},
			},
		}

		filtered := filterProjectForHost(project, []string{"transmission", "wireguard"})

		deps := filtered.Services["transmission"].DependsOn
		if _, ok := deps["wireguard"]; !ok {
			t.Error("transmission should still depend on wireguard")
		}
	})

	t.Run("clears network_mode referencing removed service", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"client": types.ServiceConfig{
					Name:        "client",
					NetworkMode: "service:vpn",
				},
				"vpn": types.ServiceConfig{Name: "vpn"},
			},
		}

		filtered := filterProjectForHost(project, []string{"client"})

		if filtered.Services["client"].NetworkMode != "" {
			t.Errorf("network_mode should be cleared, got %q", filtered.Services["client"].NetworkMode)
		}
	})

	t.Run("preserves network_mode referencing kept service", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"client": types.ServiceConfig{
					Name:        "client",
					NetworkMode: "service:vpn",
				},
				"vpn": types.ServiceConfig{Name: "vpn"},
			},
		}

		filtered := filterProjectForHost(project, []string{"client", "vpn"})

		if filtered.Services["client"].NetworkMode != "service:vpn" {
			t.Errorf("network_mode should be preserved, got %q", filtered.Services["client"].NetworkMode)
		}
	})
}

func TestBuildHostDeployOrder(t *testing.T) {
	t.Run("single host", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := [][]string{{"ssh://user@host-a"}}
		if !reflect.DeepEqual(levels, expected) {
			t.Errorf("expected %v, got %v", expected, levels)
		}
	})

	t.Run("two independent hosts same level", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
				"api": types.ServiceConfig{Name: "api"},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(levels) != 1 {
			t.Fatalf("expected 1 level, got %d: %v", len(levels), levels)
		}
		if len(levels[0]) != 2 {
			t.Errorf("expected 2 hosts in level 0, got %d", len(levels[0]))
		}
	})

	t.Run("chain dependency A then B then C", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"svc-a": types.ServiceConfig{Name: "svc-a"},
				"svc-b": types.ServiceConfig{
					Name:      "svc-b",
					DependsOn: types.DependsOnConfig{"svc-a": types.ServiceDependency{}},
				},
				"svc-c": types.ServiceConfig{
					Name:      "svc-c",
					DependsOn: types.DependsOnConfig{"svc-b": types.ServiceDependency{}},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"svc-a"},
			"ssh://user@host-b": {"svc-b"},
			"ssh://user@host-c": {"svc-c"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := [][]string{
			{"ssh://user@host-a"},
			{"ssh://user@host-b"},
			{"ssh://user@host-c"},
		}
		if !reflect.DeepEqual(levels, expected) {
			t.Errorf("expected %v, got %v", expected, levels)
		}
	})

	t.Run("diamond dependency", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"svc-a": types.ServiceConfig{Name: "svc-a"},
				"svc-b": types.ServiceConfig{
					Name:      "svc-b",
					DependsOn: types.DependsOnConfig{"svc-a": types.ServiceDependency{}},
				},
				"svc-c": types.ServiceConfig{
					Name:      "svc-c",
					DependsOn: types.DependsOnConfig{"svc-a": types.ServiceDependency{}},
				},
				"svc-d": types.ServiceConfig{
					Name: "svc-d",
					DependsOn: types.DependsOnConfig{
						"svc-b": types.ServiceDependency{},
						"svc-c": types.ServiceDependency{},
					},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"svc-a"},
			"ssh://user@host-b": {"svc-b"},
			"ssh://user@host-c": {"svc-c"},
			"ssh://user@host-d": {"svc-d"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(levels) != 3 {
			t.Fatalf("expected 3 levels, got %d: %v", len(levels), levels)
		}
		if !reflect.DeepEqual(levels[0], []string{"ssh://user@host-a"}) {
			t.Errorf("level 0: expected [host-a], got %v", levels[0])
		}
		if len(levels[1]) != 2 {
			t.Errorf("level 1: expected 2 hosts (b and c), got %v", levels[1])
		}
		if !reflect.DeepEqual(levels[2], []string{"ssh://user@host-d"}) {
			t.Errorf("level 2: expected [host-d], got %v", levels[2])
		}
	})

	t.Run("same-host depends_on ignored", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"transmission": types.ServiceConfig{
					Name:      "transmission",
					DependsOn: types.DependsOnConfig{"wireguard": types.ServiceDependency{}},
				},
				"wireguard": types.ServiceConfig{Name: "wireguard"},
				"overseerr": types.ServiceConfig{
					Name:      "overseerr",
					DependsOn: types.DependsOnConfig{"transmission": types.ServiceDependency{}},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@coconut": {"transmission", "wireguard"},
			"ssh://user@kiwi":    {"overseerr"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := [][]string{
			{"ssh://user@coconut"},
			{"ssh://user@kiwi"},
		}
		if !reflect.DeepEqual(levels, expected) {
			t.Errorf("expected %v, got %v", expected, levels)
		}
	})

	t.Run("cycle falls back to parallel", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"svc-a": types.ServiceConfig{
					Name:      "svc-a",
					DependsOn: types.DependsOnConfig{"svc-b": types.ServiceDependency{}},
				},
				"svc-b": types.ServiceConfig{
					Name:      "svc-b",
					DependsOn: types.DependsOnConfig{"svc-a": types.ServiceDependency{}},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"svc-a"},
			"ssh://user@host-b": {"svc-b"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("expected no error (cycle should warn, not fail), got: %v", err)
		}
		if len(levels) != 1 {
			t.Fatalf("expected 1 level (both hosts parallel), got %d: %v", len(levels), levels)
		}
		if len(levels[0]) != 2 {
			t.Errorf("expected 2 hosts in level 0, got %d: %v", len(levels[0]), levels[0])
		}
	})
}

func TestInjectExtraHostsNetworkMode(t *testing.T) {
	t.Run("skips service with network_mode service", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name:   "web",
					Labels: types.Labels{"composure": "ssh://user@host-a"},
				},
				"vpn": types.ServiceConfig{
					Name:   "vpn",
					Labels: types.Labels{"composure": "ssh://user@host-a"},
				},
				"client": types.ServiceConfig{
					Name:        "client",
					Labels:      types.Labels{"composure": "ssh://user@host-a"},
					NetworkMode: "service:vpn",
				},
				"api": types.ServiceConfig{
					Name:   "api",
					Labels: types.Labels{"composure": "ssh://user@host-b"},
				},
			},
		}

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web", "vpn", "client"},
			"ssh://user@host-b": {"api"},
		}

		err := injectExtraHosts(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if _, ok := project.Services["web"].ExtraHosts["api"]; !ok {
			t.Error("web should have extra_hosts for api")
		}
		if _, ok := project.Services["vpn"].ExtraHosts["api"]; !ok {
			t.Error("vpn should have extra_hosts for api")
		}

		clientHosts := project.Services["client"].ExtraHosts
		if clientHosts != nil && len(clientHosts) > 0 {
			t.Errorf("client should have no extra_hosts (network_mode: service:vpn), got %v", clientHosts)
		}
	})

	t.Run("skips service with network_mode container", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"sidecar": types.ServiceConfig{
					Name:        "sidecar",
					Labels:      types.Labels{"composure": "ssh://user@host-a"},
					NetworkMode: "container:some-container",
				},
				"api": types.ServiceConfig{
					Name:   "api",
					Labels: types.Labels{"composure": "ssh://user@host-b"},
				},
			},
		}

		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"sidecar"},
			"ssh://user@host-b": {"api"},
		}

		err := injectExtraHosts(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		sidecarHosts := project.Services["sidecar"].ExtraHosts
		if sidecarHosts != nil && len(sidecarHosts) > 0 {
			t.Errorf("sidecar should have no extra_hosts, got %v", sidecarHosts)
		}
	})
}

func TestFilterProjectForHostEdgeCases(t *testing.T) {
	t.Run("preserves non-service network_mode", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name:        "web",
					NetworkMode: "host",
				},
			},
		}

		filtered := filterProjectForHost(project, []string{"web"})

		if filtered.Services["web"].NetworkMode != "host" {
			t.Errorf("network_mode 'host' should be preserved, got %q", filtered.Services["web"].NetworkMode)
		}
	})

	t.Run("does not mutate original depends_on", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name: "web",
					DependsOn: types.DependsOnConfig{
						"api":    types.ServiceDependency{},
						"worker": types.ServiceDependency{},
					},
				},
				"api":    types.ServiceConfig{Name: "api"},
				"worker": types.ServiceConfig{Name: "worker"},
			},
		}

		_ = filterProjectForHost(project, []string{"web", "api"})

		originalDeps := project.Services["web"].DependsOn
		if len(originalDeps) != 2 {
			t.Errorf("original depends_on should still have 2 entries, got %d", len(originalDeps))
		}
	})

	t.Run("handles service with nil depends_on", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
			},
		}

		filtered := filterProjectForHost(project, []string{"web"})

		if filtered.Services["web"].DependsOn != nil {
			t.Errorf("depends_on should remain nil, got %v", filtered.Services["web"].DependsOn)
		}
	})

	t.Run("handles empty services list", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
			},
		}

		filtered := filterProjectForHost(project, []string{})

		if len(filtered.Services) != 0 {
			t.Errorf("expected 0 services, got %d", len(filtered.Services))
		}
	})

	t.Run("does not mutate original volumes", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
			},
			Volumes: types.Volumes{
				"data": types.VolumeConfig{Name: "data"},
				"logs": types.VolumeConfig{Name: "logs"},
			},
		}

		filtered := filterProjectForHost(project, []string{"web"})

		delete(filtered.Volumes, "data")

		if _, ok := project.Volumes["data"]; !ok {
			t.Error("deleting from filtered.Volumes must not remove from original project.Volumes")
		}
		if len(project.Volumes) != 2 {
			t.Errorf("original project should still have 2 volumes, got %d", len(project.Volumes))
		}
		if len(filtered.Volumes) != 1 {
			t.Errorf("filtered project should have 1 volume after delete, got %d", len(filtered.Volumes))
		}
	})
}

func TestBuildHostDeployOrderEdgeCases(t *testing.T) {
	t.Run("three-node cycle falls back to parallel", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"a": types.ServiceConfig{
					Name:      "a",
					DependsOn: types.DependsOnConfig{"c": types.ServiceDependency{}},
				},
				"b": types.ServiceConfig{
					Name:      "b",
					DependsOn: types.DependsOnConfig{"a": types.ServiceDependency{}},
				},
				"c": types.ServiceConfig{
					Name:      "c",
					DependsOn: types.DependsOnConfig{"b": types.ServiceDependency{}},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"a"},
			"ssh://user@host-b": {"b"},
			"ssh://user@host-c": {"c"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("expected no error (cycle should warn, not fail), got: %v", err)
		}
		if len(levels) != 1 {
			t.Fatalf("expected 1 level (all hosts parallel), got %d: %v", len(levels), levels)
		}
		if len(levels[0]) != 3 {
			t.Errorf("expected 3 hosts in level 0, got %d: %v", len(levels[0]), levels[0])
		}
	})

	t.Run("multiple services per host with mixed cross-host deps", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"db":     types.ServiceConfig{Name: "db"},
				"cache":  types.ServiceConfig{Name: "cache"},
				"api":    types.ServiceConfig{Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
				"worker": types.ServiceConfig{Name: "worker", DependsOn: types.DependsOnConfig{"cache": types.ServiceDependency{}}},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@data":    {"db", "cache"},
			"ssh://user@compute": {"api", "worker"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := [][]string{
			{"ssh://user@data"},
			{"ssh://user@compute"},
		}
		if !reflect.DeepEqual(levels, expected) {
			t.Errorf("expected %v, got %v", expected, levels)
		}
	})

	t.Run("service depends on unlabeled service ignored", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web":      types.ServiceConfig{Name: "web", DependsOn: types.DependsOnConfig{"redis": types.ServiceDependency{}}},
				"redis":    types.ServiceConfig{Name: "redis"},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
		}

		levels, err := buildHostDeployOrder(project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expected := [][]string{{"ssh://user@host-a"}}
		if !reflect.DeepEqual(levels, expected) {
			t.Errorf("expected %v, got %v", expected, levels)
		}
	})
}

type deployRecord struct {
	host string
	seq  int
}

func TestDeployInOrder(t *testing.T) {
	stubDeploy := func(mu *sync.Mutex, records *[]deployRecord, seq *int, failHosts map[string]bool) func(context.Context, *types.Project, string, []string, string, []string) error {
		return func(ctx context.Context, project *types.Project, host string, services []string, cmd string, flags []string) error {
			mu.Lock()
			*seq++
			*records = append(*records, deployRecord{host: host, seq: *seq})
			mu.Unlock()
			if failHosts[host] {
				return fmt.Errorf("deploy failed on %s", host)
			}
			return nil
		}
	}

	t.Run("deploys in dependency order", func(t *testing.T) {
		original := deployToHostFn
		defer func() { deployToHostFn = original }()

		var mu sync.Mutex
		var records []deployRecord
		seq := 0
		deployToHostFn = stubDeploy(&mu, &records, &seq, nil)

		project := &types.Project{
			Services: types.Services{
				"db":  types.ServiceConfig{Name: "db"},
				"api": types.ServiceConfig{Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"db"},
			"ssh://user@host-b": {"api"},
		}

		err := deployInOrder(context.Background(), project, hostDeployment, "up", nil, false, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(records) != 2 {
			t.Fatalf("expected 2 deploys, got %d", len(records))
		}

		var hostASeq, hostBSeq int
		for _, r := range records {
			if r.host == "ssh://user@host-a" {
				hostASeq = r.seq
			}
			if r.host == "ssh://user@host-b" {
				hostBSeq = r.seq
			}
		}
		if hostASeq >= hostBSeq {
			t.Errorf("host-a (db) should deploy before host-b (api), got seq a=%d b=%d", hostASeq, hostBSeq)
		}
	})

	t.Run("reverse order for down", func(t *testing.T) {
		original := deployToHostFn
		defer func() { deployToHostFn = original }()

		var mu sync.Mutex
		var records []deployRecord
		seq := 0
		deployToHostFn = stubDeploy(&mu, &records, &seq, nil)

		project := &types.Project{
			Services: types.Services{
				"db":  types.ServiceConfig{Name: "db"},
				"api": types.ServiceConfig{Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"db"},
			"ssh://user@host-b": {"api"},
		}

		err := deployInOrder(context.Background(), project, hostDeployment, "down", nil, true, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(records) != 2 {
			t.Fatalf("expected 2 deploys, got %d", len(records))
		}

		var hostASeq, hostBSeq int
		for _, r := range records {
			if r.host == "ssh://user@host-a" {
				hostASeq = r.seq
			}
			if r.host == "ssh://user@host-b" {
				hostBSeq = r.seq
			}
		}
		if hostBSeq >= hostASeq {
			t.Errorf("host-b (api) should deploy before host-a (db) in reverse, got seq a=%d b=%d", hostASeq, hostBSeq)
		}
	})

	t.Run("stops on error in level", func(t *testing.T) {
		original := deployToHostFn
		defer func() { deployToHostFn = original }()

		var mu sync.Mutex
		var records []deployRecord
		seq := 0
		deployToHostFn = stubDeploy(&mu, &records, &seq, map[string]bool{
			"ssh://user@host-a": true,
		})

		project := &types.Project{
			Services: types.Services{
				"db":  types.ServiceConfig{Name: "db"},
				"api": types.ServiceConfig{Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"db"},
			"ssh://user@host-b": {"api"},
		}

		err := deployInOrder(context.Background(), project, hostDeployment, "up", nil, false, true)
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		for _, r := range records {
			if r.host == "ssh://user@host-b" {
				t.Error("host-b should not have been deployed after host-a failed")
			}
		}
	})

	t.Run("stops on context cancellation between levels", func(t *testing.T) {
		original := deployToHostFn
		defer func() { deployToHostFn = original }()

		ctx, cancel := context.WithCancel(context.Background())

		var mu sync.Mutex
		var records []deployRecord
		seq := 0
		deployToHostFn = func(ctx context.Context, project *types.Project, host string, services []string, cmd string, flags []string) error {
			mu.Lock()
			seq++
			records = append(records, deployRecord{host: host, seq: seq})
			mu.Unlock()
			cancel()
			return nil
		}

		project := &types.Project{
			Services: types.Services{
				"db":  types.ServiceConfig{Name: "db"},
				"api": types.ServiceConfig{Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"db"},
			"ssh://user@host-b": {"api"},
		}

		err := deployInOrder(ctx, project, hostDeployment, "up", nil, false, true)
		if err == nil {
			t.Fatal("expected context error, got nil")
		}

		for _, r := range records {
			if r.host == "ssh://user@host-b" {
				t.Error("host-b should not have been deployed after context cancellation")
			}
		}
	})

	t.Run("parallel within same level", func(t *testing.T) {
		original := deployToHostFn
		defer func() { deployToHostFn = original }()

		var mu sync.Mutex
		var records []deployRecord
		seq := 0
		deployToHostFn = stubDeploy(&mu, &records, &seq, nil)

		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
				"api": types.ServiceConfig{Name: "api"},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		err := deployInOrder(context.Background(), project, hostDeployment, "up", nil, false, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(records) != 2 {
			t.Fatalf("expected 2 deploys, got %d", len(records))
		}
	})
}

func TestShortHost(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"ssh://gabe@coconut.dvc.link", "coconut.dvc.link"},
		{"ssh://user@localhost:2222", "localhost"},
		{"not-a-url", "not-a-url"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shortHost(tt.input)
			if got != tt.want {
				t.Errorf("shortHost(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestWritePlan(t *testing.T) {
	t.Run("shows hosts services and volumes", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name:  "web",
					Image: "nginx:latest",
					Volumes: []types.ServiceVolumeConfig{
						{Type: "bind", Source: "/data/html", Target: "/usr/share/nginx/html"},
					},
				},
				"api": types.ServiceConfig{
					Name:  "api",
					Image: "myapp:v2",
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		var buf bytes.Buffer
		err := writePlan(&buf, project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		for _, want := range []string{
			"Host: ssh://user@host-a",
			"Host: ssh://user@host-b",
			"web",
			"nginx:latest",
			"/data/html:/usr/share/nginx/html",
			"api",
			"myapp:v2",
			"Deploy order:",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q\n\nGot:\n%s", want, out)
			}
		}
	})

	t.Run("shows network_mode", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"vpn": types.ServiceConfig{Name: "vpn"},
				"client": types.ServiceConfig{
					Name:        "client",
					NetworkMode: "service:vpn",
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"vpn", "client"},
		}

		var buf bytes.Buffer
		err := writePlan(&buf, project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(buf.String(), "network: service:vpn") {
			t.Errorf("output missing network_mode\n\nGot:\n%s", buf.String())
		}
	})

	t.Run("shows cross-host dependencies", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"db": types.ServiceConfig{Name: "db"},
				"api": types.ServiceConfig{
					Name:      "api",
					DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"db"},
			"ssh://user@host-b": {"api"},
		}

		var buf bytes.Buffer
		err := writePlan(&buf, project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "Cross-host dependencies:") {
			t.Errorf("output missing cross-host deps header\n\nGot:\n%s", out)
		}
		if !strings.Contains(out, "api (host-b) -> db (host-a)") {
			t.Errorf("output missing dependency line\n\nGot:\n%s", out)
		}
	})

	t.Run("omits cross-host section when none exist", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
				"api": types.ServiceConfig{Name: "api"},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		var buf bytes.Buffer
		err := writePlan(&buf, project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if strings.Contains(buf.String(), "Cross-host dependencies:") {
			t.Errorf("should not show cross-host section when there are no cross-host deps\n\nGot:\n%s", buf.String())
		}
	})

	t.Run("shows deploy order levels", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"db": types.ServiceConfig{Name: "db"},
				"api": types.ServiceConfig{
					Name:      "api",
					DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"db"},
			"ssh://user@host-b": {"api"},
		}

		var buf bytes.Buffer
		err := writePlan(&buf, project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := buf.String()
		if !strings.Contains(out, "Level 1 (single): ssh://user@host-a") {
			t.Errorf("output missing level 1\n\nGot:\n%s", out)
		}
		if !strings.Contains(out, "Level 2 (single): ssh://user@host-b") {
			t.Errorf("output missing level 2\n\nGot:\n%s", out)
		}
	})

	t.Run("parallel hosts in same level", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{Name: "web"},
				"api": types.ServiceConfig{Name: "api"},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"web"},
			"ssh://user@host-b": {"api"},
		}

		var buf bytes.Buffer
		err := writePlan(&buf, project, hostDeployment)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !strings.Contains(buf.String(), "(parallel)") {
			t.Errorf("output should show parallel for multiple hosts in one level\n\nGot:\n%s", buf.String())
		}
	})
}

func TestHasDetachFlag(t *testing.T) {
	tests := []struct {
		name  string
		flags []string
		want  bool
	}{
		{"short flag", []string{"-d"}, true},
		{"long flag", []string{"--detach"}, true},
		{"mixed flags", []string{"--build", "-d"}, true},
		{"no detach", []string{"--build", "--force-recreate"}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDetachFlag(tt.flags); got != tt.want {
				t.Errorf("hasDetachFlag(%v) = %v, want %v", tt.flags, got, tt.want)
			}
		})
	}
}

func TestDeployInOrderNonBlocking(t *testing.T) {
	t.Run("foreground up deploys all hosts across levels", func(t *testing.T) {
		original := deployToHostFn
		defer func() { deployToHostFn = original }()

		var mu sync.Mutex
		var deployed []string
		deployToHostFn = func(ctx context.Context, project *types.Project, host string, services []string, cmd string, flags []string) error {
			mu.Lock()
			deployed = append(deployed, host)
			mu.Unlock()
			return nil
		}

		project := &types.Project{
			Services: types.Services{
				"db":  types.ServiceConfig{Name: "db"},
				"api": types.ServiceConfig{Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"db"},
			"ssh://user@host-b": {"api"},
		}

		err := deployInOrder(context.Background(), project, hostDeployment, "up", nil, false, false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(deployed) != 2 {
			t.Fatalf("expected 2 hosts deployed, got %d", len(deployed))
		}
	})

	t.Run("up with -d still blocks between levels", func(t *testing.T) {
		original := deployToHostFn
		defer func() { deployToHostFn = original }()

		var mu sync.Mutex
		var records []deployRecord
		seq := 0
		deployToHostFn = func(ctx context.Context, project *types.Project, host string, services []string, cmd string, flags []string) error {
			mu.Lock()
			seq++
			records = append(records, deployRecord{host: host, seq: seq})
			mu.Unlock()
			return nil
		}

		project := &types.Project{
			Services: types.Services{
				"db":  types.ServiceConfig{Name: "db"},
				"api": types.ServiceConfig{Name: "api", DependsOn: types.DependsOnConfig{"db": types.ServiceDependency{}}},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"db"},
			"ssh://user@host-b": {"api"},
		}

		err := deployInOrder(context.Background(), project, hostDeployment, "up", []string{"-d"}, false, true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var hostASeq, hostBSeq int
		for _, r := range records {
			if r.host == "ssh://user@host-a" {
				hostASeq = r.seq
			}
			if r.host == "ssh://user@host-b" {
				hostBSeq = r.seq
			}
		}
		if hostASeq >= hostBSeq {
			t.Errorf("with -d, host-a should deploy before host-b, got seq a=%d b=%d", hostASeq, hostBSeq)
		}
	})
}
