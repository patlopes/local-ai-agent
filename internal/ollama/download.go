package ollama

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// downloadOllamaBinary downloads the Ollama binary for the current OS/arch
// into the given output path. Returns nil on success.
func downloadOllamaBinary(outputPath string, logger interface{ Info(string, ...any) }) error {
	version := "0.20.5"
	goos := runtime.GOOS
	goarch := runtime.GOARCH

	// Determine the output directory (parent of the binary)
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", outputDir, err)
	}

	switch goos {
	case "linux":
		return downloadLinux(version, goarch, outputDir, outputPath, logger)
	case "darwin":
		return downloadDarwin(version, outputDir, outputPath, logger)
	case "windows":
		return downloadWindows(version, goarch, outputDir, outputPath, logger)
	default:
		return fmt.Errorf("unsupported OS: %s", goos)
	}
}

// downloadLinux downloads the .tar.zst archive for Linux, extracts directly into outputDir's parent
// preserving the archive structure (bin/ollama, lib/ollama/).
func downloadLinux(version, goarch, outputDir, outputPath string, logger interface{ Info(string, ...any) }) error {
	url := fmt.Sprintf("https://github.com/ollama/ollama/releases/download/v%s/ollama-linux-%s.tar.zst", version, goarch)
	logger.Info(fmt.Sprintf("Downloading Ollama v%s for linux/%s...", version, goarch))
	logger.Info(fmt.Sprintf("URL: %s", url))
	logger.Info("This is a one-time download, please wait...")

	// Download to a temp file
	tmpFile, err := os.CreateTemp("", "ollama-*.tar.zst")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	resp, err := http.Get(url)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}

	written, err := io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("download interrupted: %w", err)
	}
	logger.Info(fmt.Sprintf("Downloaded %d MB, extracting...", written/1024/1024))

	// Extract directly into the DataDir (parent of outputDir "bin/").
	// The archive contains bin/ollama and lib/ollama/ at root level,
	// so extracting to DataDir gives us the correct structure where
	// Ollama can find its runner libs at ../lib/ollama/ relative to binary.
	dataDir := filepath.Dir(outputDir)
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data dir: %w", err)
	}

	cmd := exec.Command("bash", "-c", fmt.Sprintf("zstd -d '%s' --stdout | tar xf - -C '%s'", tmpPath, dataDir))
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Verify the binary landed correctly
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		return fmt.Errorf("ollama binary not found at %s after extraction", outputPath)
	}
	os.Chmod(outputPath, 0755)

	// Check if CUDA/runner libs were extracted
	libDir := filepath.Join(dataDir, "lib", "ollama")
	if info, statErr := os.Stat(libDir); statErr == nil && info.IsDir() {
		logger.Info("GPU runner libraries extracted", "path", libDir)
	} else {
		logger.Info("No GPU runner libraries found in archive (CPU-only)")
	}

	logger.Info(fmt.Sprintf("Ollama v%s installed successfully", version))
	return nil
}

// downloadDarwin downloads the macOS tgz.
func downloadDarwin(version, outputDir, outputPath string, logger interface{ Info(string, ...any) }) error {
	url := fmt.Sprintf("https://github.com/ollama/ollama/releases/download/v%s/ollama-darwin.tgz", version)
	logger.Info(fmt.Sprintf("Downloading Ollama v%s for macOS...", version))
	logger.Info(fmt.Sprintf("URL: %s", url))
	logger.Info("This is a one-time download, please wait...")

	tmpFile, err := os.CreateTemp("", "ollama-*.tgz")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	resp, err := http.Get(url)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}

	written, err := io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("download interrupted: %w", err)
	}
	logger.Info(fmt.Sprintf("Downloaded %d MB, extracting...", written/1024/1024))

	extractDir, err := os.MkdirTemp("", "ollama-extract-*")
	if err != nil {
		return fmt.Errorf("failed to create extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	cmd := exec.Command("tar", "xzf", tmpPath, "-C", extractDir)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	// Find the ollama binary — might be at bin/ollama or just ollama
	for _, candidate := range []string{
		filepath.Join(extractDir, "bin", "ollama"),
		filepath.Join(extractDir, "ollama"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			if err := copyFile(candidate, outputPath); err != nil {
				return fmt.Errorf("failed to copy binary: %w", err)
			}
			os.Chmod(outputPath, 0755)
			logger.Info(fmt.Sprintf("Ollama v%s installed successfully", version))
			return nil
		}
	}

	return fmt.Errorf("ollama binary not found in macOS archive")
}

// downloadWindows downloads the Windows zip.
func downloadWindows(version, goarch, outputDir, outputPath string, logger interface{ Info(string, ...any) }) error {
	url := fmt.Sprintf("https://github.com/ollama/ollama/releases/download/v%s/ollama-windows-%s.zip", version, goarch)
	logger.Info(fmt.Sprintf("Downloading Ollama v%s for windows/%s...", version, goarch))
	logger.Info("This is a one-time download, please wait...")

	tmpFile, err := os.CreateTemp("", "ollama-*.zip")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	resp, err := http.Get(url)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		tmpFile.Close()
		return fmt.Errorf("download failed with HTTP %d", resp.StatusCode)
	}

	_, err = io.Copy(tmpFile, resp.Body)
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("download interrupted: %w", err)
	}

	// Extract with PowerShell or unzip
	extractDir, err := os.MkdirTemp("", "ollama-extract-*")
	if err != nil {
		return fmt.Errorf("failed to create extract dir: %w", err)
	}
	defer os.RemoveAll(extractDir)

	cmd := exec.Command("powershell", "-Command",
		fmt.Sprintf("Expand-Archive -Path '%s' -DestinationPath '%s' -Force", tmpPath, extractDir))
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("extraction failed: %w", err)
	}

	for _, candidate := range []string{
		filepath.Join(extractDir, "ollama.exe"),
		filepath.Join(extractDir, "bin", "ollama.exe"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			if err := copyFile(candidate, outputPath); err != nil {
				return fmt.Errorf("failed to copy binary: %w", err)
			}
			logger.Info(fmt.Sprintf("Ollama v%s installed successfully", version))
			return nil
		}
	}

	return fmt.Errorf("ollama binary not found in Windows archive")
}

// copyFile copies src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
