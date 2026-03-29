package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/compose-spec/compose-go/v2/types"
)

func TestReadNFSVolumes(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		project := &types.Project{
			Volumes: types.Volumes{
				"media": types.VolumeConfig{
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"host": "ssh://gabe@coconut.dvc.link",
						"path": "/home/gabe/lib/plex",
					},
				},
				"downloads": types.VolumeConfig{
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"host": "ssh://gabe@coconut.dvc.link",
						"path": "/home/gabe/lib/plex/downloads",
					},
				},
			},
		}

		vols, err := readNFSVolumes(project)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(vols) != 2 {
			t.Fatalf("expected 2 volumes, got %d", len(vols))
		}
		if vols["media"].Host != "ssh://gabe@coconut.dvc.link" {
			t.Errorf("media host = %q", vols["media"].Host)
		}
		if vols["downloads"].Path != "/home/gabe/lib/plex/downloads" {
			t.Errorf("downloads path = %q", vols["downloads"].Path)
		}
	})

	t.Run("missing host", func(t *testing.T) {
		project := &types.Project{
			Volumes: types.Volumes{
				"data": types.VolumeConfig{
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"path": "/data",
					},
				},
			},
		}

		_, err := readNFSVolumes(project)
		if err == nil {
			t.Fatal("expected error for missing host")
		}
		if !strings.Contains(err.Error(), "missing required driver_opt \"host\"") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("missing path", func(t *testing.T) {
		project := &types.Project{
			Volumes: types.Volumes{
				"data": types.VolumeConfig{
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"host": "ssh://user@host",
					},
				},
			},
		}

		_, err := readNFSVolumes(project)
		if err == nil {
			t.Fatal("expected error for missing path")
		}
		if !strings.Contains(err.Error(), "missing required driver_opt \"path\"") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("non-ssh host", func(t *testing.T) {
		project := &types.Project{
			Volumes: types.Volumes{
				"data": types.VolumeConfig{
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"host": "coconut.dvc.link",
						"path": "/data",
					},
				},
			},
		}

		_, err := readNFSVolumes(project)
		if err == nil {
			t.Fatal("expected error for non-ssh host")
		}
		if !strings.Contains(err.Error(), "host must be an ssh:// URI") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("relative path", func(t *testing.T) {
		project := &types.Project{
			Volumes: types.Volumes{
				"data": types.VolumeConfig{
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"host": "ssh://user@host",
						"path": "lib/plex",
					},
				},
			},
		}

		_, err := readNFSVolumes(project)
		if err == nil {
			t.Fatal("expected error for relative path")
		}
		if !strings.Contains(err.Error(), "path must be absolute") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("no composure-nfs volumes", func(t *testing.T) {
		project := &types.Project{
			Volumes: types.Volumes{
				"data": types.VolumeConfig{
					Driver: "local",
				},
			},
		}

		vols, err := readNFSVolumes(project)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(vols) != 0 {
			t.Errorf("expected 0 volumes, got %d", len(vols))
		}
	})

	t.Run("nil volumes", func(t *testing.T) {
		project := &types.Project{}

		vols, err := readNFSVolumes(project)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(vols) != 0 {
			t.Errorf("expected 0 volumes, got %d", len(vols))
		}
	})
}

func TestFindConsumerHosts(t *testing.T) {
	t.Run("single consumer", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"plex": types.ServiceConfig{
					Name: "plex",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "media", Target: "/data"},
					},
				},
				"overseerr": types.ServiceConfig{
					Name: "overseerr",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "media", Target: "/data"},
					},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@coconut": {"plex"},
			"ssh://user@kiwi":    {"overseerr"},
		}
		nfsVolumes := map[string]NFSVolume{
			"media": {Host: "ssh://user@coconut", Path: "/data/media"},
		}

		consumers := findConsumerHosts(project, hostDeployment, nfsVolumes)

		if len(consumers) != 1 {
			t.Fatalf("expected 1 server, got %d", len(consumers))
		}
		clients := consumers["ssh://user@coconut"]
		if !clients["ssh://user@kiwi"] {
			t.Errorf("expected kiwi as consumer of coconut, got %v", clients)
		}
	})

	t.Run("multiple NFS hosts", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"svc-a": types.ServiceConfig{
					Name: "svc-a",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "vol-b", Target: "/b"},
					},
				},
				"svc-b": types.ServiceConfig{
					Name: "svc-b",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "vol-a", Target: "/a"},
					},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"svc-a"},
			"ssh://user@host-b": {"svc-b"},
		}
		nfsVolumes := map[string]NFSVolume{
			"vol-a": {Host: "ssh://user@host-a", Path: "/data/a"},
			"vol-b": {Host: "ssh://user@host-b", Path: "/data/b"},
		}

		consumers := findConsumerHosts(project, hostDeployment, nfsVolumes)

		if len(consumers) != 2 {
			t.Fatalf("expected 2 servers, got %d: %v", len(consumers), consumers)
		}
		if !consumers["ssh://user@host-a"]["ssh://user@host-b"] {
			t.Error("host-b should be consumer of host-a")
		}
		if !consumers["ssh://user@host-b"]["ssh://user@host-a"] {
			t.Error("host-a should be consumer of host-b")
		}
	})

	t.Run("no consumers when all on same host", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"svc-a": types.ServiceConfig{
					Name: "svc-a",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "data", Target: "/data"},
					},
				},
				"svc-b": types.ServiceConfig{
					Name: "svc-b",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "data", Target: "/data"},
					},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"svc-a", "svc-b"},
		}
		nfsVolumes := map[string]NFSVolume{
			"data": {Host: "ssh://user@host-a", Path: "/data"},
		}

		consumers := findConsumerHosts(project, hostDeployment, nfsVolumes)
		if len(consumers) != 0 {
			t.Errorf("expected no consumers, got %v", consumers)
		}
	})

	t.Run("bind mount volumes ignored", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"svc": types.ServiceConfig{
					Name: "svc",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeBind, Source: "/host/path", Target: "/data"},
					},
				},
			},
		}
		hostDeployment := map[string][]string{
			"ssh://user@host-a": {"svc"},
		}
		nfsVolumes := map[string]NFSVolume{
			"data": {Host: "ssh://user@host-b", Path: "/data"},
		}

		consumers := findConsumerHosts(project, hostDeployment, nfsVolumes)
		if len(consumers) != 0 {
			t.Errorf("expected no consumers for bind mounts, got %v", consumers)
		}
	})
}

func TestRewriteNFSVolumes(t *testing.T) {
	t.Run("source host gets bind mount", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"plex": types.ServiceConfig{
					Name: "plex",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "media", Target: "/data"},
					},
				},
			},
			Volumes: types.Volumes{
				"media": types.VolumeConfig{
					Name:   "media",
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"host": "ssh://user@coconut",
						"path": "/home/user/media",
					},
				},
			},
		}

		hasNFS := rewriteNFSVolumes(project, "ssh://user@coconut")
		if !hasNFS {
			t.Fatal("expected hasNFS=true")
		}

		svc := project.Services["plex"]
		if svc.Volumes[0].Type != types.VolumeTypeBind {
			t.Errorf("expected bind mount, got %q", svc.Volumes[0].Type)
		}
		if svc.Volumes[0].Source != "/home/user/media" {
			t.Errorf("expected source /home/user/media, got %q", svc.Volumes[0].Source)
		}
		if _, exists := project.Volumes["media"]; exists {
			t.Error("top-level volume should be removed on source host")
		}
	})

	t.Run("remote host gets NFS volume", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"overseerr": types.ServiceConfig{
					Name: "overseerr",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "media", Target: "/data"},
					},
				},
			},
			Volumes: types.Volumes{
				"media": types.VolumeConfig{
					Name:   "media",
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"host": "ssh://user@coconut",
						"path": "/home/user/media",
					},
				},
			},
		}

		hasNFS := rewriteNFSVolumes(project, "ssh://user@kiwi")
		if !hasNFS {
			t.Fatal("expected hasNFS=true")
		}

		// Service volume should stay as named volume
		svc := project.Services["overseerr"]
		if svc.Volumes[0].Type != types.VolumeTypeVolume {
			t.Errorf("expected volume type, got %q", svc.Volumes[0].Type)
		}
		if svc.Volumes[0].Source != "media" {
			t.Errorf("expected source media, got %q", svc.Volumes[0].Source)
		}

		vol, exists := project.Volumes["media"]
		if !exists {
			t.Fatal("top-level volume should exist on remote host")
		}
		if vol.Driver != "local" {
			t.Errorf("expected driver local, got %q", vol.Driver)
		}
		if vol.DriverOpts["type"] != "nfs" {
			t.Errorf("expected type nfs, got %q", vol.DriverOpts["type"])
		}
		if !strings.Contains(vol.DriverOpts["o"], "nfsvers=4") {
			t.Errorf("expected nfsvers=4 in options, got %q", vol.DriverOpts["o"])
		}
		if vol.DriverOpts["device"] != ":/home/user/media" {
			t.Errorf("expected device :/home/user/media, got %q", vol.DriverOpts["device"])
		}
	})

	t.Run("non-composure-nfs volumes left untouched", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"web": types.ServiceConfig{
					Name: "web",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "regular", Target: "/data"},
					},
				},
			},
			Volumes: types.Volumes{
				"regular": types.VolumeConfig{
					Name:   "regular",
					Driver: "local",
				},
			},
		}

		hasNFS := rewriteNFSVolumes(project, "ssh://user@host")
		if hasNFS {
			t.Fatal("expected hasNFS=false for non-composure-nfs volumes")
		}

		vol := project.Volumes["regular"]
		if vol.Driver != "local" {
			t.Errorf("regular volume should be untouched, got driver=%q", vol.Driver)
		}
	})

	t.Run("mixed volumes", func(t *testing.T) {
		project := &types.Project{
			Services: types.Services{
				"app": types.ServiceConfig{
					Name: "app",
					Volumes: []types.ServiceVolumeConfig{
						{Type: types.VolumeTypeVolume, Source: "shared", Target: "/shared"},
						{Type: types.VolumeTypeBind, Source: "/host/config", Target: "/config"},
						{Type: types.VolumeTypeVolume, Source: "regular", Target: "/data"},
					},
				},
			},
			Volumes: types.Volumes{
				"shared": types.VolumeConfig{
					Name:   "shared",
					Driver: composureNFSDriver,
					DriverOpts: types.Options{
						"host": "ssh://user@server",
						"path": "/srv/shared",
					},
				},
				"regular": types.VolumeConfig{
					Name:   "regular",
					Driver: "local",
				},
			},
		}

		rewriteNFSVolumes(project, "ssh://user@server")

		svc := project.Services["app"]
		// shared -> bind mount
		if svc.Volumes[0].Type != types.VolumeTypeBind {
			t.Errorf("shared should be bind, got %q", svc.Volumes[0].Type)
		}
		// host config -> still bind
		if svc.Volumes[1].Type != types.VolumeTypeBind {
			t.Errorf("config should still be bind, got %q", svc.Volumes[1].Type)
		}
		// regular -> still volume
		if svc.Volumes[2].Type != types.VolumeTypeVolume {
			t.Errorf("regular should still be volume, got %q", svc.Volumes[2].Type)
		}
	})
}

func TestDetectPackageManager(t *testing.T) {
	t.Run("detects apt", func(t *testing.T) {
		original := runSSHFn
		defer func() { runSSHFn = original }()
		runSSHFn = func(ctx context.Context, sshURI string, command string) (string, error) {
			if command == "which apt" {
				return "/usr/bin/apt", nil
			}
			return "", fmt.Errorf("not found")
		}

		pm, err := defaultDetectPackageManager(context.Background(), "ssh://user@host")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pm != "apt" {
			t.Errorf("expected apt, got %q", pm)
		}
	})

	t.Run("detects dnf when apt missing", func(t *testing.T) {
		original := runSSHFn
		defer func() { runSSHFn = original }()
		runSSHFn = func(ctx context.Context, sshURI string, command string) (string, error) {
			if command == "which dnf" {
				return "/usr/bin/dnf", nil
			}
			return "", fmt.Errorf("not found")
		}

		pm, err := defaultDetectPackageManager(context.Background(), "ssh://user@host")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pm != "dnf" {
			t.Errorf("expected dnf, got %q", pm)
		}
	})

	t.Run("detects pacman when apt and dnf missing", func(t *testing.T) {
		original := runSSHFn
		defer func() { runSSHFn = original }()
		runSSHFn = func(ctx context.Context, sshURI string, command string) (string, error) {
			if command == "which pacman" {
				return "/usr/bin/pacman", nil
			}
			return "", fmt.Errorf("not found")
		}

		pm, err := defaultDetectPackageManager(context.Background(), "ssh://user@host")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if pm != "pacman" {
			t.Errorf("expected pacman, got %q", pm)
		}
	})

	t.Run("error when none found", func(t *testing.T) {
		original := runSSHFn
		defer func() { runSSHFn = original }()
		runSSHFn = func(ctx context.Context, sshURI string, command string) (string, error) {
			return "", fmt.Errorf("not found")
		}

		_, err := defaultDetectPackageManager(context.Background(), "ssh://user@host")
		if err == nil {
			t.Fatal("expected error when no package manager found")
		}
		if !strings.Contains(err.Error(), "no supported package manager") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

func TestNFSInstallCmds(t *testing.T) {
	tests := []struct {
		pkgMgr      string
		wantServer  string
		wantClient  string
		wantService string
	}{
		{"apt", "nfs-kernel-server", "nfs-common", "nfs-kernel-server"},
		{"dnf", "nfs-utils", "nfs-utils", "nfs-server"},
		{"pacman", "nfs-utils", "nfs-utils", "nfs-server"},
	}
	for _, tt := range tests {
		t.Run(tt.pkgMgr, func(t *testing.T) {
			server := nfsServerInstallCmd(tt.pkgMgr)
			if !strings.Contains(server, tt.wantServer) {
				t.Errorf("nfsServerInstallCmd(%q) = %q, want to contain %q", tt.pkgMgr, server, tt.wantServer)
			}
			client := nfsClientInstallCmd(tt.pkgMgr)
			if !strings.Contains(client, tt.wantClient) {
				t.Errorf("nfsClientInstallCmd(%q) = %q, want to contain %q", tt.pkgMgr, client, tt.wantClient)
			}
			svcName := nfsServerServiceName(tt.pkgMgr)
			if svcName != tt.wantService {
				t.Errorf("nfsServerServiceName(%q) = %q, want %q", tt.pkgMgr, svcName, tt.wantService)
			}
		})
	}
}

func TestPrintSetupCommands(t *testing.T) {
	t.Run("server commands with apt", func(t *testing.T) {
		var buf bytes.Buffer
		vols := []NFSVolume{
			{Host: "ssh://gabe@coconut.dvc.link", Path: "/home/gabe/lib/plex"},
		}
		clientIPs := map[string]string{"ssh://user@kiwi": "10.0.0.5"}

		printSetupCommands(&buf, "ssh://gabe@coconut.dvc.link", "apt", vols, clientIPs, true)

		out := buf.String()
		if !strings.Contains(out, "ssh gabe@coconut.dvc.link") {
			t.Errorf("missing ssh command, got:\n%s", out)
		}
		if !strings.Contains(out, "nfs-kernel-server") {
			t.Errorf("missing nfs-kernel-server install, got:\n%s", out)
		}
		if !strings.Contains(out, "/home/gabe/lib/plex 10.0.0.5(rw,sync,no_subtree_check,no_root_squash)") {
			t.Errorf("missing exports line, got:\n%s", out)
		}
		if !strings.Contains(out, "exportfs -ra") {
			t.Errorf("missing exportfs, got:\n%s", out)
		}
		if !strings.Contains(out, "systemctl enable --now nfs-kernel-server") {
			t.Errorf("missing systemctl enable, got:\n%s", out)
		}
	})

	t.Run("server commands with dnf", func(t *testing.T) {
		var buf bytes.Buffer
		vols := []NFSVolume{
			{Host: "ssh://user@host", Path: "/data"},
		}
		clientIPs := map[string]string{"ssh://user@other": "10.0.0.1"}

		printSetupCommands(&buf, "ssh://user@host", "dnf", vols, clientIPs, true)

		out := buf.String()
		if !strings.Contains(out, "dnf install -y nfs-utils") {
			t.Errorf("expected dnf command, got:\n%s", out)
		}
		if !strings.Contains(out, "systemctl enable --now nfs-server") {
			t.Errorf("missing systemctl enable for dnf, got:\n%s", out)
		}
	})

	t.Run("client commands", func(t *testing.T) {
		var buf bytes.Buffer
		printSetupCommands(&buf, "ssh://gabe@kiwi.dvc.link", "apt", nil, nil, false)

		out := buf.String()
		if !strings.Contains(out, "ssh gabe@kiwi.dvc.link") {
			t.Errorf("missing ssh command, got:\n%s", out)
		}
		if !strings.Contains(out, "nfs-common") {
			t.Errorf("missing nfs-common install, got:\n%s", out)
		}
	})
}

func TestWritePlanNFSVolumes(t *testing.T) {
	project := &types.Project{
		Services: types.Services{
			"plex": types.ServiceConfig{
				Name: "plex",
				Volumes: []types.ServiceVolumeConfig{
					{Type: types.VolumeTypeVolume, Source: "media", Target: "/data"},
				},
			},
			"overseerr": types.ServiceConfig{
				Name: "overseerr",
				Volumes: []types.ServiceVolumeConfig{
					{Type: types.VolumeTypeVolume, Source: "media", Target: "/data"},
				},
			},
		},
		Volumes: types.Volumes{
			"media": types.VolumeConfig{
				Name:   "media",
				Driver: composureNFSDriver,
				DriverOpts: types.Options{
					"host": "ssh://user@coconut",
					"path": "/home/user/media",
				},
			},
		},
	}
	hostDeployment := map[string][]string{
		"ssh://user@coconut": {"plex"},
		"ssh://user@kiwi":    {"overseerr"},
	}

	var buf bytes.Buffer
	err := writePlan(&buf, project, hostDeployment)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "Shared volumes (NFS):") {
		t.Errorf("missing NFS section header\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "media:") {
		t.Errorf("missing volume name\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "/home/user/media") {
		t.Errorf("missing volume path\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "coconut") {
		t.Errorf("missing host\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "plex (local)") {
		t.Errorf("missing local consumer\n\nGot:\n%s", out)
	}
	if !strings.Contains(out, "overseerr (nfs)") {
		t.Errorf("missing nfs consumer\n\nGot:\n%s", out)
	}
}
