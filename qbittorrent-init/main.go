package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

const (
	defaultWebPort    = "8080"
	defaultBtPort     = "6881"
	defaultConfigPath = "/config/qBittorrent/qBittorrent.conf"
	defaultLogPath    = "/config/qBittorrent/logs/qbittorrent.log"
)

var (
	version   = "dev"
	buildTime = ""
	logger    *slog.Logger
)

func main() {
	configureLogger()
	logger.Info("Starting qBittorrent initializer",
		"version", version,
		"build_time", buildTime)

	// Handle termination signals
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Validate and set configuration
	if err := setupEnvironment(); err != nil {
		logger.Error("Failed to setup environment", "error", err)
		os.Exit(1)
	}

	// Initialize configuration files
	if err := initializeConfig(); err != nil {
		logger.Error("Configuration initialization failed", "error", err)
		os.Exit(1)
	}

	// Start qBittorrent process
	if err := runQBittorrent(ctx); err != nil {
		logger.Error("qBittorrent process failed", "error", err)
		os.Exit(1)
	}

	logger.Info("qBittorrent initializer completed successfully")
}

func configureLogger() {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level:     slog.LevelInfo,
		AddSource: false,
	}).WithAttrs([]slog.Attr{
		slog.String("service", "qbittorrent-init"),
	})

	logger = slog.New(handler)
}

func setupEnvironment() error {
	// Set ports with defaults
	setPort("QBT_WEBUI_PORT", "QBITTORRENT__PORT", defaultWebPort)
	setPort("QBT_TORRENTING_PORT", "QBITTORRENT__BT_PORT", defaultBtPort)

	// Validate environment variables
	if os.Getenv("QBT_WEBUI_PORT") == "" {
		return fmt.Errorf("WEBUI_PORT must be set")
	}
	if os.Getenv("QBT_TORRENTING_PORT") == "" {
		return fmt.Errorf("TORRENTING_PORT must be set")
	}

	return nil
}

func setPort(targetVar, sourceVar, defaultValue string) {
	if val := os.Getenv(sourceVar); val != "" {
		os.Setenv(targetVar, val)
	} else {
		os.Setenv(targetVar, defaultValue)
		logger.Warn("Using default port", "variable", targetVar, "value", defaultValue)
	}
}

func initializeConfig() error {
	// Create config directory if needed
	if err := ensureConfigFile(defaultConfigPath); err != nil {
		return fmt.Errorf("config file setup failed: %w", err)
	}

	// Setup logging symlink
	if err := ensureLogSymlink(defaultLogPath); err != nil {
		return fmt.Errorf("log setup failed: %w", err)
	}

	return nil
}

func ensureConfigFile(configPath string) error {
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		logger.Info("Initializing default configuration", "path", configPath)

		if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
			return fmt.Errorf("failed to create config directory: %w", err)
		}

		if err := copyFile("/app/qBittorrent.conf", configPath); err != nil {
			return fmt.Errorf("failed to copy config file: %w", err)
		}

		logger.Info("Default configuration copied successfully")
	}
	return nil
}

func ensureLogSymlink(logPath string) error {
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		logger.Info("Initializing log symlink", "path", logPath)

		if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
			return fmt.Errorf("failed to create log directory: %w", err)
		}

		if err := os.Symlink("/dev/stdout", logPath); err != nil {
			return fmt.Errorf("failed to create log symlink: %w", err)
		}

		logger.Info("Log symlink created successfully")
	}
	return nil
}

func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open source file: %w", err)
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return fmt.Errorf("failed to copy file contents: %w", err)
	}

	if err := os.Chmod(dst, 0644); err != nil {
		return fmt.Errorf("failed to set file permissions: %w", err)
	}

	return nil
}

func runQBittorrent(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "/usr/bin/qbittorrent-nox", os.Args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	logger.Info("Starting qBittorrent process", "command", cmd.String())

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		return fmt.Errorf("process exited unexpectedly: %w", err)
	case <-ctx.Done():
		logger.Info("Received termination signal, shutting down")
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)

		select {
		case <-time.After(30 * time.Second):
			syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			return fmt.Errorf("forced shutdown after timeout")
		case err := <-done:
			return err
		}
	}
}
