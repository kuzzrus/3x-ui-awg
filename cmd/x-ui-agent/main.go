// Command x-ui-agent runs Xray-core for a master x-ui panel. It holds no database and
// no web UI: the master pushes the config and the agent applies it and reports back.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/op/go-logging"

	"github.com/mhsanaei/3x-ui/v3/internal/agent"
	"github.com/mhsanaei/3x-ui/v3/internal/agentproto"
	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "x-ui-agent:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("x-ui-agent", flag.ContinueOnError)
	bundleFile := flags.String("bundle-file", "/etc/x-ui-agent/bundle", "pairing bundle the installer stored")
	stateDir := flags.String("state-dir", "/etc/x-ui-agent", "folder for the agent's identity and its last good config")
	binDir := flags.String("bin-dir", "/usr/local/x-ui-agent/bin", "folder with the xray binary and the geo files")
	logDir := flags.String("log-dir", "/var/log/x-ui-agent", "folder for the logs")
	showVersion := flags.Bool("version", false, "print the version and exit")
	checkBundle := flags.Bool("check-bundle", false, "validate the pairing bundle, print the address it names and exit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		_, err := fmt.Fprintln(stdout, config.GetPanelVersion())
		return err
	}

	raw, err := os.ReadFile(*bundleFile)
	if err != nil {
		return fmt.Errorf("read the pairing bundle: %w", err)
	}
	bundle, err := agentproto.ParseBundle(string(raw))
	if err != nil {
		return err
	}
	if *checkBundle {
		_, err := fmt.Fprintln(stdout, net.JoinHostPort(bundle.Address, strconv.Itoa(bundle.Port)))
		return err
	}
	bin, err := filepath.Abs(*binDir)
	if err != nil {
		return err
	}
	logs, err := filepath.Abs(*logDir)
	if err != nil {
		return err
	}
	// internal/xray finds the core and its logs through these.
	if err := os.Setenv("XUI_BIN_FOLDER", bin); err != nil {
		return err
	}
	if err := os.Setenv("XUI_LOG_FOLDER", logs); err != nil {
		return err
	}
	level, err := logLevel()
	if err != nil {
		return err
	}
	logger.InitLogger(level)
	defer logger.CloseLogger()

	return agent.Run(ctx, agent.Options{Bundle: bundle, StateDir: *stateDir, LogDir: logs})
}

func logLevel() (logging.Level, error) {
	switch config.GetLogLevel() {
	case config.Debug:
		return logging.DEBUG, nil
	case config.Info:
		return logging.INFO, nil
	case config.Notice:
		return logging.NOTICE, nil
	case config.Warning:
		return logging.WARNING, nil
	case config.Error:
		return logging.ERROR, nil
	default:
		return 0, fmt.Errorf("unknown log level %q", config.GetLogLevel())
	}
}
