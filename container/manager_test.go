package container

import (
	"testing"

	"github.com/farseer-go/docker"
)

func TestMatchContainerNames(t *testing.T) {
	tests := []struct {
		name       string
		matchNames []string
		allowNames []string
		want       bool
	}{
		{
			name:       "empty filter matches all",
			matchNames: []string{"nginx"},
			want:       true,
		},
		{
			name:       "exact service name matches",
			matchNames: []string{"traefik"},
			allowNames: []string{"traefik"},
			want:       true,
		},
		{
			name:       "swarm task name matches by delimiter",
			matchNames: []string{"traefik.1.abc123"},
			allowNames: []string{"traefik"},
			want:       true,
		},
		{
			name:       "different service does not match",
			matchNames: []string{"nginx.1.abc123", "nginx"},
			allowNames: []string{"traefik"},
			want:       false,
		},
		{
			name:       "similar prefix without delimiter does not match",
			matchNames: []string{"traefik-test.1.abc123", "traefik-test"},
			allowNames: []string{"traefik"},
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchContainerNames(tt.matchNames, tt.allowNames)
			if got != tt.want {
				t.Fatalf("MatchContainerNames(%v, %v) = %v, want %v", tt.matchNames, tt.allowNames, got, tt.want)
			}
		})
	}
}

func TestGetContainerMatchNames(t *testing.T) {
	inspect := &docker.ContainerIdInspectJson{}
	inspect.Name = "/traefik.1.abc123"
	inspect.Config.Labels.ComDockerSwarmServiceName = "traefik"
	inspect.Config.Labels.ComDockerSwarmTaskName = "traefik.1.abc123"

	matchNames := GetContainerMatchNames(inspect)
	if !MatchContainerNames(matchNames, []string{"traefik"}) {
		t.Fatalf("expected %v to match traefik", matchNames)
	}
}
