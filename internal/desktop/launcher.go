package desktop

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// OpenDesktopWindow opens targetURL in a chromeless desktop app window on Windows (Edge/Chrome --app or rundll32 fallback),
// or delegates to the native desktop opener on macOS (open) and Linux (xdg-open).
func OpenDesktopWindow(targetURL string) error {
	return openDesktopWindowWith(
		runtime.GOOS,
		targetURL,
		exec.LookPath,
		fileExists,
		startDetachedCommand,
	)
}

func openDesktopWindowWith(
	goos string,
	targetURL string,
	lookup func(string) (string, error),
	exists func(string) bool,
	start func(name string, args ...string) error,
) error {
	validatedURL, err := validateWindowTargetURL(targetURL)
	if err != nil {
		return err
	}

	switch goos {
	case "windows":
		if browserBin := findWindowsChromiumBinary(lookup, exists); browserBin != "" {
			return start(browserBin, "--app="+validatedURL)
		}
		return start("rundll32", "url.dll,FileProtocolHandler", validatedURL)
	case "darwin":
		return start("open", validatedURL)
	default:
		return start("xdg-open", validatedURL)
	}
}

func validateWindowTargetURL(rawURL string) (string, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return "", fmt.Errorf("%w: targetURL is required", ErrValidation)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("%w: invalid targetURL: %v", ErrValidation, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", fmt.Errorf("%w: targetURL scheme must be http or https", ErrValidation)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("%w: targetURL host is required", ErrValidation)
	}
	return parsed.String(), nil
}

func findWindowsChromiumBinary(lookup func(string) (string, error), exists func(string) bool) string {
	for _, exeName := range []string{"msedge.exe", "chrome.exe"} {
		if found, err := lookup(exeName); err == nil && found != "" {
			return found
		}
	}

	candidateDirs := []string{
		os.Getenv("ProgramFiles"),
		os.Getenv("ProgramFiles(x86)"),
		os.Getenv("LocalAppData"),
	}
	relativePaths := []string{
		filepath.Join("Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join("Google", "Chrome", "Application", "chrome.exe"),
	}
	for _, baseDir := range candidateDirs {
		trimmedBase := strings.TrimSpace(baseDir)
		if trimmedBase == "" {
			continue
		}
		for _, relPath := range relativePaths {
			fullPath := filepath.Join(trimmedBase, relPath)
			if exists(fullPath) {
				return fullPath
			}
		}
	}
	return ""
}

func fileExists(targetPath string) bool {
	info, err := os.Stat(targetPath)
	return err == nil && !info.IsDir()
}

func startDetachedCommand(name string, args ...string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("empty launcher binary")
	}
	cmd := exec.Command(name, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("launch %s: %w", name, err)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}
