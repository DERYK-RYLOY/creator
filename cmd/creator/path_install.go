// path_install.go — teach the installer to put the bin directory on the
// user's PATH, idempotently. `creator install` should leave the system in a
// state where typing `creator` just works — mirroring install.sh/install.ps1.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const pathMarker = "# >>> creator >>>"

// ensureUserPathIncludes adds dir to the user PATH if it is missing.
// Returns (true, nil) when the PATH was changed, (false, nil) when dir was
// already present, and an error when the change could not be made.
// Set CREATOR_NO_PATH=1 to skip the PATH change entirely.
func ensureUserPathIncludes(dir string) (bool, error) {
	if os.Getenv("CREATOR_NO_PATH") != "" {
		// Deliberate escape hatch — do not touch the user's PATH.
		return false, nil
	}
	current := os.Getenv("PATH")
	if pathContains(current, dir) {
		return false, nil
	}
	if isWindows() {
		return ensureUserPathWindows(dir)
	}
	return ensureUserPathUnix(dir)
}

func isWindows() bool { return os.PathListSeparator == ';' }

func pathContains(path, dir string) bool {
	target := normalizeDir(dir)
	for _, p := range strings.Split(path, string(os.PathListSeparator)) {
		p = normalizeDir(p)
		if p == target {
			return true
		}
	}
	return false
}

func normalizeDir(d string) string {
	d = strings.TrimSpace(d)
	for len(d) > 1 && (strings.HasSuffix(d, `\`) || strings.HasSuffix(d, "/")) {
		d = d[:len(d)-1]
	}
	return strings.ToLower(filepath.Clean(d))
}

// ensureUserPathWindows modifies the *user* PATH in the registry through
// PowerShell (reading it first, so nothing is truncated) and also exports it
// into the current process so `creator` works immediately afterwards.
func ensureUserPathWindows(dir string) (bool, error) {
	script := `
$p = [Environment]::GetEnvironmentVariable('Path','User')
$parts = @()
if ($p) { $parts = $p -split ';' | ForEach-Object { $_.Trim().TrimEnd('\') } | Where-Object { $_ -ne '' } }
$target = $env:CREATOR_DIR_NEEDLE
$norm = $target.TrimEnd('\')
foreach ($e in $parts) {
    if ([string]::Equals($e, $norm, [StringComparison]::OrdinalIgnoreCase)) { Write-Output 'already'; exit 0 }
}
$new = if ($p) { "$p;$target" } else { $target }
[Environment]::SetEnvironmentVariable('Path', $new, 'User')
Write-Output 'added'
`
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	cmd.Env = append(os.Environ(), "CREATOR_DIR_NEEDLE="+dir)
	out, err := cmd.Output()
	if err != nil {
		// PowerShell missing or blocked: report, don't guess.
		return false, fmt.Errorf("powershell.exe unavailable: %w", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "already":
		return false, nil
	case "added":
		// visible in this process too
		os.Setenv("PATH", os.Getenv("PATH")+string(os.PathListSeparator)+dir)
		return true, nil
	default:
		return false, fmt.Errorf("unexpected powershell output %q", strings.TrimSpace(string(out)))
	}
}

// ensureUserPathUnix appends a marker-guarded export to the rc files that
// match the user's shell (plus ~/.profile as the POSIX catch-all).
func ensureUserPathUnix(dir string) (bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, err
	}
	shell := filepath.Base(os.Getenv("SHELL"))
	rcFiles := []string{}
	switch shell {
	case "zsh":
		rcFiles = []string{filepath.Join(home, ".zshrc")}
	case "bash":
		rcFiles = []string{filepath.Join(home, ".bashrc"), filepath.Join(home, ".bash_profile")}
	case "fish":
		added, err := ensureFishPath(home, dir)
		if err != nil {
			return false, err
		}
		return added, nil
	default:
		rcFiles = []string{filepath.Join(home, ".profile")}
	}
	block := "\n" + pathMarker + "\nexport PATH=\"" + dir + ":$PATH\"\n# <<< creator <<<\n"
	fmt.Println("updating PATH in:")
	changed := false
	for _, rc := range rcFiles {
		data, err := os.ReadFile(rc)
		if err != nil {
			continue // rc does not exist or is unreadable — not fatal
		}
		if strings.Contains(string(data), pathMarker) || pathContains(":"+string(data)+":", dir) {
			fmt.Printf("  keep  %s (already configured)\n", rc)
			continue
		}
		if err := os.WriteFile(rc, append([]byte(block), data...), 0o644); err != nil {
			return changed, fmt.Errorf("write %s: %w", rc, err)
		}
		// the export must come before existing PATH usage lines, so prepend is correct
		fmt.Printf("  edit  %s\n", rc)
		changed = true
	}
	// POSIX catch-all for login shells
	profile := filepath.Join(home, ".profile")
	if data, err := os.ReadFile(profile); err == nil && !strings.Contains(string(data), pathMarker) {
		if err := os.WriteFile(profile, append([]byte(block), data...), 0o644); err == nil {
			fmt.Printf("  edit  %s\n", profile)
			changed = true
		}
	}
	return changed, nil
}

// ensureFishPath uses fish_add_path when Fish is the shell.
func ensureFishPath(home, dir string) (bool, error) {
	cfgDir := filepath.Join(home, ".config", "fish")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		return false, err
	}
	cfg := filepath.Join(cfgDir, "config.fish")
	data, err := os.ReadFile(cfg)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if strings.Contains(string(data), pathMarker) || strings.Contains(string(data), dir) {
		fmt.Printf("  keep  %s (already configured)\n", cfg)
		return false, nil
	}
	block := "\n" + pathMarker + "\nfish_add_path " + dir + "\n# <<< creator <<<\n"
	if err := os.WriteFile(cfg, append([]byte(block), data...), 0o644); err != nil {
		return false, err
	}
	fmt.Printf("  edit  %s\n", cfg)
	return true, nil
}
