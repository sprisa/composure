package main

import (
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

	t.Run("three hosts", func(t *testing.T) {
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
