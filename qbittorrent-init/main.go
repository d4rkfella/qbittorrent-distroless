package main

import (
	"bytes"
	"context"
	"fmt"
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

type allowedOption struct {
    expectsValue  bool
    allowedValues map[string]bool
    validator     func(string) bool
}

var allowedOptions = map[string]allowedOption{
    "-h":                              {expectsValue: false},
    "--help":                          {expectsValue: false},
    "-v":                              {expectsValue: false},
    "--version":                       {expectsValue: false},
    "--confirm-legal-notice":          {expectsValue: false},
    
    "--webui-port": {
        expectsValue: true,
        validator:    isValidPort,
    },
    "--torrenting-port": {
        expectsValue: true,
        validator:    isValidPort,
    },
    
    "-d":               {expectsValue: false},
    "--daemon":         {expectsValue: false},
    
    "--profile":        {expectsValue: true, validator: isValidPath},
    "--configuration":  {expectsValue: true},
    "--relative-fastresume": {expectsValue: false},
    
    "--save-path":      {expectsValue: true, validator: isValidPath},
    "--add-stopped": {
        expectsValue:  true,
        allowedValues: map[string]bool{"true": true, "false": true},
    },
    "--skip-hash-check":       {expectsValue: false},
    "--category":              {expectsValue: true},
    "--sequential":            {expectsValue: false},
    "--first-and-last":        {expectsValue: false},
    "--skip-dialog": {
        expectsValue:  true,
        allowedValues: map[string]bool{"true": true, "false": true},
    },
}

var (
	version = "dev"
	commit  = ""
	date    = ""
	logger  *slog.Logger
)

const defaultConfigTemplate = `[AutoRun]
enabled=false
program=

[LegalNotice]
Accepted=true

[BitTorrent]
Session\AsyncIOThreadsCount=10
Session\DiskCacheSize=-1
Session\DiskIOReadMode=DisableOSCache
Session\DiskIOType=SimplePreadPwrite
Session\DiskIOWriteMode=EnableOSCache
Session\DiskQueueSize=4194304
Session\FilePoolSize=40
Session\HashingThreadsCount=2
Session\ResumeDataStorageType=SQLite
Session\UseOSCache=true

[Preferences]
Connection\PortRangeMin=6881
Connection\UPnP=false
General\Locale=en
General\UseRandomPort=false
WebUI\Address=*
WebUI\CSRFProtection=false
WebUI\HostHeaderValidation=false
WebUI\LocalHostAuth=false
WebUI\ServerDomains=*
WebUI\UseUPnP=false
`

func main() {
	configureLogger()
	logger.Info("Starting qBittorrent initializer",
		"version", version,
		"commit", commit,
		"date", date)

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := setupEnvironment(); err != nil {
		logger.Error("Failed to setup environment", "error", err)
		os.Exit(1)
	}

	if err := initializeConfig(); err != nil {
		logger.Error("Configuration initialization failed", "error", err)
		os.Exit(1)
	}

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

func setDefaultEnvVar(name, defaultValue string) {
    if os.Getenv(name) == "" {
        os.Setenv(name, defaultValue)
        logger.Warn(fmt.Sprintf("%s not set, using default", name),
            "var", name, "value", defaultValue)
    } else {
        logger.Info(fmt.Sprintf("using user defined %s", name),
            "var", name, "value", os.Getenv(name))
    }
}

func setupEnvironment() error {
    setDefaultEnvVar("QBT_WEBUI_PORT", defaultWebPort)
    setDefaultEnvVar("QBT_TORRENTING_PORT", defaultBtPort)

    if !isValidPort(os.Getenv("QBT_WEBUI_PORT")) {
        return fmt.Errorf("invalid QBT_WEBUI_PORT: %s", os.Getenv("QBT_WEBUI_PORT"))
    }
    if !isValidPort(os.Getenv("QBT_TORRENTING_PORT")) {
        return fmt.Errorf("invalid QBT_TORRENTING_PORT: %s", os.Getenv("QBT_TORRENTING_PORT"))
    }

    return nil
}

func initializeConfig() error {
	if err := ensureConfigFile(defaultConfigPath); err != nil {
		return fmt.Errorf("config file setup failed: %w", err)
	}
	if err := ensureLogSymlink(defaultLogPath); err != nil {
		return fmt.Errorf("log setup failed: %w", err)
	}
	return nil
}

func ensureConfigFile(configPath string) error {
    if _, err := os.Stat(configPath); os.IsNotExist(err) {
        logger.Info("Configuration file does not exist, writing default configuration", "path", configPath)

        if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
            return fmt.Errorf("failed to create config directory: %w", err)
        }

        if err := os.WriteFile(configPath, []byte(defaultConfigTemplate), 0644); err != nil {
            return fmt.Errorf("failed to write config file: %w", err)
        }

        logger.Info("Default configuration written successfully")
    } else if err != nil {
        return fmt.Errorf("failed to check config file: %w", err)
    } else {
        logger.Info("Configuration file already exists, skipping write", "path", configPath)
    }
    return nil
}

func ensureLogSymlink(logPath string) error {
    err := os.Symlink("/dev/stdout", logPath)
    switch {
    case err == nil:
        logger.Info("Log symlink created successfully", "path", logPath)
        return nil
    case os.IsExist(err):
        logger.Debug("Log symlink already exists", "path", logPath)
        return nil
    default:
        if os.IsNotExist(err) {
            logger.Info("Creating log directory", "path", filepath.Dir(logPath))
            if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
                return fmt.Errorf("failed to create log directory: %w", err)
            }
            if err := os.Symlink("/dev/stdout", logPath); err != nil {
                return fmt.Errorf("failed to create log symlink after retry: %w", err)
            }
            logger.Info("Log symlink created after directory creation", "path", logPath)
            return nil
        }
        return fmt.Errorf("unexpected error creating symlink: %w", err)
    }
}

func sanitizeArgs(args []string) []string {
    var sanitized []string
    i := 0

    for i < len(args) {
        arg := args[i]
        originalArg := arg
        
        switch arg {
        case "-h":
            arg = "--help"
        case "-v":
            arg = "--version"
        case "-d":
            arg = "--daemon"
        }

        if arg == "--" {
            sanitized = append(sanitized, args[i:]...)
            break
        }

        opt, exists := allowedOptions[arg]
        if !exists || !strings.HasPrefix(arg, "-") {
            sanitized = append(sanitized, originalArg)
            i++
            continue
        }

        if !opt.expectsValue {
            sanitized = append(sanitized, arg)
            i++
            continue
        }

        var value string
        if strings.Contains(arg, "=") {
            parts := strings.SplitN(arg, "=", 2)
            arg = parts[0]
            value = parts[1]
        } else if i+1 < len(args) {
            value = args[i+1]
            i++
        }

        valid := true
        switch {
        case opt.validator != nil:
            valid = opt.validator(value)
        case len(opt.allowedValues) > 0:
            valid = opt.allowedValues[strings.ToLower(value)]
        }

        if valid {
            sanitized = append(sanitized, arg)
            if value != "" {
                sanitized = append(sanitized, value)
            }
        } else {
            logger.Warn("Skipping invalid value for option", 
                "option", arg, "value", value)
        }
        
        i++
    }

    return sanitized
}

func isValidPort(port string) bool {
    p, err := strconv.Atoi(port)
    return err == nil && p > 0 && p <= 65535
}

func isValidPath(path string) bool {
    return !strings.Contains(path, "..") && 
           !strings.HasPrefix(path, "/") && 
           !strings.Contains(path, "$")
}

func runQBittorrent(ctx context.Context) error {
	safeArgs := sanitizeArgs(os.Args[1:])
	cmd := exec.CommandContext(ctx, "/usr/bin/qbittorrent-nox", safeArgs...)
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
