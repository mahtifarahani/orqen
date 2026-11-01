package sysprobe

import (
	"context"
	"strings"
)

func dockerChecks(ctx context.Context, d Deps, in Inputs) []Check {
	return []Check{timed(func() Check {
		c := Check{ID: "docker.engine", Group: "docker", Blocks: []string{"commands_needing_docker", "compose_mongo"}}
		orb := d.FS.Exists("/Applications/OrbStack.app") || d.LookPath(in.ChildPath, "orb") != ""
		path := d.LookPath(in.ChildPath, "docker")
		if path == "" {
			c.Status, c.FixCode, c.Detail = Warn, "docker_missing", "docker was not found"
			c.FixCommand = []string{"brew install --cask orbstack"}
			return c
		}
		out, exit, err := d.Runner.Run(ctx, []string{path, "version", "--format", "{{.Server.Version}}"}, runEnv(in))
		if err != nil || exit != 0 {
			c.Status, c.FixCode, c.Detail = Warn, "docker_not_running", "the Docker engine is not reachable"
			c.FixCommand = []string{"open -a OrbStack"}
			return c
		}
		c.Status, c.Detail = Pass, "Docker engine "+strings.TrimSpace(out)
		if ctxName, exit, err := d.Runner.Run(ctx, []string{path, "context", "show"}, runEnv(in)); err == nil && exit == 0 {
			c.Detail += ", context " + strings.TrimSpace(ctxName)
		}
		if orb {
			c.Detail += " (OrbStack detected)"
		}
		return c
	})}
}
