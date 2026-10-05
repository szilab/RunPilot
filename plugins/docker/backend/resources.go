package main

import (
	"encoding/json"
	"strings"
)

type volume struct {
	Name              string            `json:"name"`
	Driver            string            `json:"driver"`
	Scope             string            `json:"scope"`
	Labels            map[string]string `json:"labels"`
	ComposeProject    string            `json:"composeProject,omitempty"`
	ComposeVolume     string            `json:"composeVolume,omitempty"`
	ManagedByRunPilot bool              `json:"managedByRunPilot"`
	InUse             bool              `json:"inUse"`
	RunningUse        bool              `json:"runningUse"`
	UsedBy            []string          `json:"usedBy,omitempty"`
}
type network struct {
	Name              string            `json:"name"`
	Driver            string            `json:"driver"`
	Scope             string            `json:"scope"`
	Labels            map[string]string `json:"labels"`
	ComposeProject    string            `json:"composeProject,omitempty"`
	ComposeNetwork    string            `json:"composeNetwork,omitempty"`
	ManagedByRunPilot bool              `json:"managedByRunPilot"`
	InUse             bool              `json:"inUse"`
	RunningUse        bool              `json:"runningUse"`
	UsedBy            []string          `json:"usedBy,omitempty"`
}
type resourceRow struct {
	Name   string `json:"Name"`
	Driver string `json:"Driver"`
	Scope  string `json:"Scope"`
	Labels string `json:"Labels"`
}
type use struct {
	Running bool
	Names   []string
}

func parseResourceRows(s string) ([]resourceRow, *rpcError) {
	if strings.TrimSpace(s) == "" {
		return []resourceRow{}, nil
	}
	var rows []resourceRow
	if json.Unmarshal([]byte(s), &rows) == nil {
		for _, row := range rows {
			if row.Name == "" {
				return nil, fail("invalid_data", "Docker resource row is missing a name")
			}
		}
		return rows, nil
	}
	rows = []resourceRow{}
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row resourceRow
		if json.Unmarshal([]byte(line), &row) != nil {
			return nil, fail("invalid_data", "invalid Docker resource JSON")
		}
		if row.Name == "" {
			return nil, fail("invalid_data", "Docker resource row is missing a name")
		}
		rows = append(rows, row)
	}
	return rows, nil
}
func (p *plugin) resourceRows(kind string) ([]resourceRow, *rpcError) {
	out, e := run([]string{kind, "ls", "--format", "json"}, "", 2<<20)
	if e != nil {
		return nil, e
	}
	return parseResourceRows(out.Stdout)
}
func (p *plugin) allInspected() (map[string]use, map[string]use, *rpcError) {
	vols := map[string]use{}
	nets := map[string]use{}
	out, e := run([]string{"ps", "-aq"}, "", 2<<20)
	if e != nil {
		return nil, nil, e
	}
	ids := strings.Fields(out.Stdout)
	if len(ids) == 0 {
		return vols, nets, nil
	}
	if len(ids) > 1024 {
		return nil, nil, fail("resource_limit", "too many Docker containers to inspect safely")
	}
	args := append([]string{"inspect"}, ids...)
	out, e = run(args, "", 2<<20)
	if e != nil {
		return nil, nil, e
	}
	var cs []inspectedContainer
	if json.Unmarshal([]byte(out.Stdout), &cs) != nil {
		return nil, nil, fail("invalid_data", "invalid Docker container metadata")
	}
	if len(cs) != len(ids) {
		return nil, nil, fail("invalid_data", "incomplete Docker container metadata")
	}
	seen := map[string]bool{}
	for _, c := range cs {
		if !validID(c.ID) || seen[c.ID] {
			return nil, nil, fail("invalid_data", "invalid Docker container ID in metadata")
		}
		seen[c.ID] = true
	}
	for _, id := range ids {
		found := false
		for _, c := range cs {
			if strings.HasPrefix(strings.ToLower(c.ID), strings.ToLower(id)) {
				found = true
				break
			}
		}
		if !found {
			return nil, nil, fail("invalid_data", "Docker inspect did not return a requested container")
		}
	}
	for _, c := range cs {
		name := strings.TrimPrefix(c.Name, "/")
		if service := c.Config.Labels["com.docker.compose.service"]; service != "" {
			name = service
		}
		for _, m := range c.Mounts {
			if m.Type != "volume" || m.Name == "" {
				continue
			}
			u := vols[m.Name]
			u.Running = u.Running || c.State.Running
			u.Names = append(u.Names, name)
			vols[m.Name] = u
		}
		for n := range c.NetworkSettings.Networks {
			u := nets[n]
			u.Running = u.Running || c.State.Running
			u.Names = append(u.Names, name)
			nets[n] = u
		}
	}
	return vols, nets, nil
}
func (p *plugin) listVolumes() ([]volume, *rpcError) {
	rows, e := p.resourceRows("volume")
	if e != nil {
		return nil, e
	}
	usage, _, e := p.allInspected()
	if e != nil {
		return nil, e
	}
	out := make([]volume, 0, len(rows))
	for _, row := range rows {
		l := labels(row.Labels)
		u := usage[row.Name]
		out = append(out, volume{Name: row.Name, Driver: row.Driver, Scope: row.Scope, Labels: l, ComposeProject: l["com.docker.compose.project"], ComposeVolume: l["com.docker.compose.volume"], ManagedByRunPilot: l["com.runpilot.managed"] == "true", InUse: len(u.Names) > 0, RunningUse: u.Running, UsedBy: u.Names})
	}
	return out, nil
}
func (p *plugin) listNetworks() ([]network, *rpcError) {
	rows, e := p.resourceRows("network")
	if e != nil {
		return nil, e
	}
	_, usage, e := p.allInspected()
	if e != nil {
		return nil, e
	}
	out := make([]network, 0, len(rows))
	for _, row := range rows {
		l := labels(row.Labels)
		u := usage[row.Name]
		out = append(out, network{Name: row.Name, Driver: row.Driver, Scope: row.Scope, Labels: l, ComposeProject: l["com.docker.compose.project"], ComposeNetwork: l["com.docker.compose.network"], ManagedByRunPilot: l["com.runpilot.managed"] == "true", InUse: len(u.Names) > 0, RunningUse: u.Running, UsedBy: u.Names})
	}
	return out, nil
}
func (p *plugin) resourceAction(method, name string) (any, *rpcError) {
	if !validResourceName(name) {
		return nil, fail("invalid_argument", "invalid Docker resource name")
	}
	if !p.runtime().Available {
		return nil, fail("unavailable", "Docker runtime is unavailable")
	}
	switch method {
	case "docker.volumes.create":
		_, e := run([]string{"volume", "create", "--driver", "local", "--label", "com.runpilot.managed=true", name}, "", 8192)
		if e != nil {
			return nil, e
		}
	case "docker.volumes.delete":
		usage, _, e := p.allInspected()
		if e != nil {
			return nil, e
		}
		if len(usage[name].Names) > 0 {
			return nil, fail("failed_precondition", "Docker volume is referenced by a container")
		}
		_, e = run([]string{"volume", "rm", name}, "", 8192)
		if e != nil {
			return nil, e
		}
	case "docker.networks.create":
		rows, e := p.resourceRows("network")
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			if row.Name == name {
				return nil, fail("already_exists", "Docker network already exists")
			}
		}
		_, e = run([]string{"network", "create", "--driver", "bridge", "--label", "com.runpilot.managed=true", name}, "", 8192)
		if e != nil {
			return nil, e
		}
	case "docker.networks.delete":
		if name == "bridge" || name == "host" || name == "none" {
			return nil, fail("forbidden", "default Docker network cannot be deleted")
		}
		rows, e := p.resourceRows("network")
		if e != nil {
			return nil, e
		}
		for _, row := range rows {
			if row.Name == name && labels(row.Labels)["com.docker.compose.project"] != "" {
				return nil, fail("forbidden", "Compose-managed network cannot be deleted directly")
			}
		}
		_, usage, e := p.allInspected()
		if e != nil {
			return nil, e
		}
		if len(usage[name].Names) > 0 {
			return nil, fail("failed_precondition", "Docker network is referenced by a container")
		}
		_, e = run([]string{"network", "rm", name}, "", 8192)
		if e != nil {
			return nil, e
		}
	default:
		return nil, fail("unknown_method", "unknown Docker resource operation")
	}
	return map[string]any{"ok": true}, nil
}
