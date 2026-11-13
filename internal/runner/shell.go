package runner

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

type shell struct {
	path string
	args []string
	name string
}

// Name reports the interpreter that will be used.
func (s shell) Name() string { return s.name }

// ResolveShell picks an interpreter for a command string. Preference "auto"
// selects the best shell available on this machine.
func ResolveShell(preference string) (shell, error) {
	pref := strings.ToLower(strings.TrimSpace(preference))
	if pref == "" {
		pref = "auto"
	}
	if pref == "auto" {
		pref = defaultShellName()
	}
	switch pref {
	case "bash":
		if p, err := exec.LookPath("bash"); err == nil {
			return shell{path: p, args: []string{"-c"}, name: "bash"}, nil
		}
		return shell{}, fmt.Errorf("bash not found on PATH")
	case "sh":
		if p, err := exec.LookPath("sh"); err == nil {
			return shell{path: p, args: []string{"-c"}, name: "sh"}, nil
		}
		return shell{}, fmt.Errorf("sh not found on PATH")
	case "zsh":
		if p, err := exec.LookPath("zsh"); err == nil {
			return shell{path: p, args: []string{"-c"}, name: "zsh"}, nil
		}
		return shell{}, fmt.Errorf("zsh not found on PATH")
	case "pwsh":
		if p, err := exec.LookPath("pwsh"); err == nil {
			return shell{path: p, args: []string{"-NoProfile", "-NonInteractive", "-Command"}, name: "pwsh"}, nil
		}
		return shell{}, fmt.Errorf("pwsh not found on PATH")
	case "powershell":
		if p, err := exec.LookPath("powershell"); err == nil {
			return shell{path: p, args: []string{"-NoProfile", "-NonInteractive", "-Command"}, name: "powershell"}, nil
		}
		return shell{}, fmt.Errorf("powershell not found on PATH")
	case "cmd":
		if p, err := exec.LookPath("cmd"); err == nil {
			return shell{path: p, args: []string{"/c"}, name: "cmd"}, nil
		}
		return shell{}, fmt.Errorf("cmd not found on PATH")
	default:
		return shell{}, fmt.Errorf("unknown shell %q (use auto, bash, zsh, sh, pwsh, powershell or cmd)", preference)
	}
}

func defaultShellName() string {
	if runtime.GOOS == "windows" {
		for _, candidate := range []string{"bash", "pwsh", "powershell", "cmd"} {
			if _, err := exec.LookPath(candidate); err == nil {
				return candidate
			}
		}
		return "cmd"
	}
	if _, err := exec.LookPath("bash"); err == nil {
		return "bash"
	}
	return "sh"
}
