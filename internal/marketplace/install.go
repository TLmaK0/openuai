package marketplace

import (
	"os/exec"
	"strings"

	"openuai/internal/config"
)

// Install converts a catalog entry into an MCPServerConfig ready to be added.
// The secret parameter is substituted into env vars and args that contain {{SECRET}}.
func Install(entry CatalogEntry, secret string) config.MCPServerConfig {
	// Build env map, substituting {{SECRET}}
	env := make(map[string]string, len(entry.Env))
	for k, v := range entry.Env {
		env[k] = strings.ReplaceAll(v, "{{SECRET}}", secret)
	}

	// Build args, substituting {{SECRET}} (used by postgres for conn string)
	args := make([]string, len(entry.Args))
	for i, a := range entry.Args {
		args[i] = strings.ReplaceAll(a, "{{SECRET}}", secret)
	}

	return config.MCPServerConfig{
		Name:      entry.Name,
		Command:   entry.Command,
		Args:      args,
		Env:       env,
		AutoStart: true,
		Subscribe: entry.Subscribe,
	}
}

// CheckNpx returns true if npx is available on the system PATH.
func CheckNpx() bool {
	_, err := exec.LookPath("npx")
	return err == nil
}
