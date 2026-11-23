package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/drain"
	"github.com/veertuinc/anklet/internal/host"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
	"github.com/veertuinc/anklet/internal/plugin"
	"github.com/veertuinc/anklet/internal/run"
)

var (
	version         = "dev"
	runOnce         = "false"
	versionFlag     = flag.Bool("version", false, "Print the version")
	configFlag      = flag.String("c", "", "Path to the config file (defaults to ~/.config/anklet/config.yml)")
	shutDownMessage = "anklet plugin shut down"
)

// steadyStatus returns the status a plugin should report between runs:
// "draining" for handler plugins while the drain file is present, otherwise
// "idle". Receiver plugins are never marked draining (they only queue jobs).
func steadyStatus(p config.Plugin) string {
	if !run.IsReceiverType(p.Plugin) && !p.IsReceiver() && drain.IsDraining() {
		return "draining"
	}
	return "idle"
}

// computeSoloReceiver reports whether every configured plugin is a receiver.
func computeSoloReceiver(plugins []config.Plugin) bool {
	if len(plugins) == 0 {
		return false
	}
	for _, p := range plugins {
		if !run.IsReceiverType(p.Plugin) && !p.IsReceiver() {
			return false
		}
	}
	return true
}

// openMetricsListener binds and returns a live TCP listener for the metrics server.
// When configuredPort is empty, ports starting at 8080 up to 65535 are probed.
func openMetricsListener(bindAddress, configuredPort string) (net.Listener, string, error) {
	if configuredPort != "" {
		ln, err := net.Listen("tcp", net.JoinHostPort(bindAddress, configuredPort))
		if err != nil {
			return nil, configuredPort, err
		}
		return ln, configuredPort, nil
	}
	for port := 8080; port <= 65535; port++ {
		portStr := strconv.Itoa(port)
		ln, err := net.Listen("tcp", net.JoinHostPort(bindAddress, portStr))
		if err == nil {
			return ln, portStr, nil
		}
	}
	return nil, "", fmt.Errorf("no available TCP port found in range 8080-65535")
}

// applyPluginDefaults sets default configuration values across loaded plugins.
func applyPluginDefaults(cfg *config.Config) {
	for index := range cfg.Plugins {
		p := &cfg.Plugins[index]
		if run.IsReceiverType(p.Plugin) || p.IsReceiver() {
			if p.RedeliverHours == 0 {
				p.RedeliverHours = 24
			}
			if cfg.GlobalReceiverSecret != "" {
				p.Secret = cfg.GlobalReceiverSecret
			}
		} else {
			if p.TemplateDiskBuffer == 0 {
				if cfg.GlobalTemplateDiskBuffer > 0 {
					p.TemplateDiskBuffer = cfg.GlobalTemplateDiskBuffer
				} else {
					p.TemplateDiskBuffer = 10.0
				}
			}
			if cfg.GlobalSkipCPUAndMemoryResourceChecks {
				p.SkipCPUAndMemoryResourceChecks = true
			}
		}
	}
}

func main() {
	parentLogger := logging.New()
	parentCtx := context.Background()

	if version == "" {
		version = "dev"
	}

	flag.Parse()
	if *versionFlag {
		fmt.Println(version)
		os.Exit(0)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "unable to get user home directory", "error", err)
		os.Exit(1)
	}

	var configPath string
	var configFileName string
	if *configFlag != "" {
		configPath = *configFlag
		configFileName = filepath.Base(*configFlag)
	} else {
		envConfigFileName := os.Getenv("ANKLET_CONFIG_FILE_NAME")
		if envConfigFileName != "" {
			configFileName = envConfigFileName
		} else {
			configFileName = "config.yml"
		}
		configPath = filepath.Join(homeDir, ".config", "anklet", configFileName)
	}
	parentCtx = context.WithValue(parentCtx, config.ContextKey("configFileName"), configFileName)

	loadedConfig, err := config.LoadConfig(configPath)
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "unable to load config.yml (is it in the work_dir, or are you using an absolute path?)", "error", err)
		os.Exit(1)
	}
	loadedConfig, err = config.LoadInEnvs(loadedConfig)
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "unable to load config.yml from environment variables", "error", err)
		os.Exit(1)
	}

	applyPluginDefaults(&loadedConfig)
	// Config implements slog.LogValuer so secrets are automatically redacted.
	parentLogger.InfoContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))

	if err := config.ValidatePlugins(loadedConfig.Plugins); err != nil {
		parentLogger.ErrorContext(parentCtx, err.Error())
		os.Exit(1)
	}

	parentCtx = logging.AppendCtx(parentCtx, slog.String("version", version))

	var suffix string
	if loadedConfig.Metrics.Aggregator {
		suffix = "-aggregator"
	}
	parentCtx = context.WithValue(parentCtx, config.ContextKey("suffix"), suffix)

	if loadedConfig.Log.FileDir != "" {
		if !strings.HasSuffix(loadedConfig.Log.FileDir, "/") {
			loadedConfig.Log.FileDir += "/"
		}
		if _, err := os.Stat(loadedConfig.Log.FileDir); os.IsNotExist(err) {
			parentLogger.ErrorContext(parentCtx, "log directory does not exist", "directory", loadedConfig.Log.FileDir)
			os.Exit(1)
		}
	}

	parentCtx = context.WithValue(parentCtx, config.ContextKey("logger"), parentLogger)

	if loadedConfig.WorkDir == "" {
		loadedConfig.WorkDir = "./"
	}

	parentCtx = context.WithValue(parentCtx, config.ContextKey("config"), &loadedConfig)

	pluginsPath := loadedConfig.PluginsPath
	if pluginsPath == "" {
		pluginsPath = filepath.Join(homeDir, ".config", "anklet", "plugins")
	}
	parentLogger.InfoContext(parentCtx, "plugins path", slog.String("pluginsPath", pluginsPath))

	hostCPUCount, err := host.GetHostCPUCount(parentCtx)
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "error getting host cpu count", "error", err)
		os.Exit(1)
	}
	hostMemoryBytes, err := host.GetHostMemoryBytes(parentCtx)
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "error getting host memory bytes", "error", err)
		os.Exit(1)
	}
	parentCtx = logging.AppendCtx(parentCtx,
		slog.Int("hostCPUCount", hostCPUCount),
		slog.Uint64("hostMemoryBytes", hostMemoryBytes),
	)

	pluginsMap := make(map[string]map[string]*config.PluginGlobal)
	for _, p := range loadedConfig.Plugins {
		if pluginsMap[p.Plugin] == nil {
			pluginsMap[p.Plugin] = make(map[string]*config.PluginGlobal)
		}
		pluginsMap[p.Plugin][p.Name] = &config.PluginGlobal{}
	}

	parentCtx = context.WithValue(parentCtx, config.ContextKey("globals"), &config.Globals{
		RunPluginsOnce:  runOnce == "true",
		PluginsPath:     pluginsPath,
		DebugEnabled:    logging.IsDebugEnabled(),
		HostCPUCount:    hostCPUCount,
		HostMemoryBytes: hostMemoryBytes,
		TemplateTracker: config.NewTemplateTracker(),
		Plugins:         pluginsMap,
	})

	httpTransport := http.DefaultTransport
	parentCtx = context.WithValue(parentCtx, config.ContextKey("httpTransport"), httpTransport)

	sigChan := make(chan os.Signal, 2)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	worker(parentCtx, parentLogger, loadedConfig, configFileName, sigChan)
}

func worker(
	parentCtx context.Context,
	parentLogger *slog.Logger,
	loadedConfig config.Config,
	configFileName string,
	sigChan chan os.Signal,
) {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(parentCtx)
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "unable to get globals from context", "error", err)
		os.Exit(1)
	}
	for _, pluginGroup := range workerGlobals.Plugins {
		for _, pluginGlobal := range pluginGroup {
			pluginGlobal.Paused.Store(true)
		}
	}

	var toRunOnce atomic.Bool
	toRunOnce.Store(workerGlobals.RunPluginsOnce)
	workerCtx, workerCancel := context.WithCancel(parentCtx)

	defer func() {
		workerCancel()
		time.Sleep(time.Second)
		parentLogger.WarnContext(parentCtx, "anklet (and all plugins) shut down")
	}()

	suffix, _ := parentCtx.Value(config.ContextKey("suffix")).(string)
	parentLogger.InfoContext(workerCtx, "starting anklet"+suffix)

	go func() {
		defer signal.Stop(sigChan)
		var sigCount int
		for sig := range sigChan {
			switch sig {
			case syscall.SIGQUIT:
				parentLogger.WarnContext(workerCtx, "graceful shutdown, waiting for jobs to finish...")
				workerGlobals.ReturnAllToMainQueue.Store(true)
				toRunOnce.Store(true)
			default:
				sigCount++
				if sigCount >= 2 {
					parentLogger.WarnContext(workerCtx, "forceful shutdown after second interrupt... be sure to clean up any self-hosted runners and VMs!")
					os.Exit(1)
				}
				if !loadedConfig.Metrics.Aggregator {
					parentLogger.WarnContext(workerCtx, "best effort graceful shutdown, interrupting the job as soon as possible...")
				}
				workerGlobals.ReturnAllToMainQueue.Store(true)
				workerCancel()
			}
		}
	}()

	metricsLn, metricsPort, err := openMetricsListener(loadedConfig.Metrics.BindAddress, loadedConfig.Metrics.Port)
	if err != nil {
		parentLogger.ErrorContext(workerCtx, "unable to bind metrics port", "port", loadedConfig.Metrics.Port, "error", err)
		os.Exit(1)
	}
	metricsService := metrics.NewServerWithAddress(loadedConfig.Metrics.BindAddress, metricsPort)
	metricsDone := make(chan struct{})

	if loadedConfig.Metrics.Aggregator {
		dbCfg := config.ResolveDatabaseConfig(loadedConfig, loadedConfig.Metrics.Database)
		databaseContainer, err := database.NewClient(workerCtx, dbCfg)
		if err != nil {
			_ = metricsLn.Close()
			parentLogger.ErrorContext(workerCtx, "unable to access database", "error", err)
			os.Exit(1)
		}
		workerCtx = context.WithValue(workerCtx, config.ContextKey("database"), databaseContainer)
		go func() {
			defer close(metricsDone)
			metricsService.StartAggregatorServerWithListener(workerCtx, parentLogger, metricsLn)
		}()
		parentLogger.InfoContext(workerCtx, "metrics aggregator started on port "+metricsPort)

		if !toRunOnce.Load() {
			<-workerCtx.Done()
		} else {
			workerCancel()
		}
		select {
		case <-metricsDone:
		case <-time.After(5 * time.Second):
		}
		return
	}

	metricsData := &metrics.MetricsDataLock{}
	workerCtx = context.WithValue(workerCtx, config.ContextKey("metrics"), metricsData)
	parentLogger.InfoContext(workerCtx, "metrics server started on port "+metricsPort)
	metrics.UpdateSystemMetrics(workerCtx, metricsData)

	soloReceiver := computeSoloReceiver(loadedConfig.Plugins)
	go func() {
		defer close(metricsDone)
		metricsService.StartWithListener(workerCtx, metricsLn, soloReceiver)
	}()

	var wg sync.WaitGroup
	httpTransport, _ := parentCtx.Value(config.ContextKey("httpTransport")).(*http.Transport)

launchLoop:
	for index, p := range loadedConfig.Plugins {
		if index != 0 {
			prev := loadedConfig.Plugins[index-1]
			waitTicker := time.NewTicker(5 * time.Second)
		waitLoop:
			for {
				if workerGlobals.Plugins[prev.Plugin][prev.Name].FinishedInitialRun.Load() {
					workerGlobals.Plugins[p.Plugin][p.Name].Paused.Store(false)
					break waitLoop
				}
				select {
				case <-workerCtx.Done():
					waitTicker.Stop()
					break launchLoop
				case <-waitTicker.C:
				}
			}
			waitTicker.Stop()
		}

		wg.Add(1)
		go func(idx int, pluginCfg config.Plugin) {
			defer wg.Done()
			pluginCtx, pluginCancel := context.WithCancel(context.Background())
			defer pluginCancel()

			go func() {
				<-workerCtx.Done()
				pluginCancel()
			}()

			pluginLogger := parentLogger
			if loadedConfig.Log.SplitByPlugin {
				parentLogger.InfoContext(parentCtx, "writing "+pluginCfg.Name+" logs to "+loadedConfig.Log.FileDir+"anklet"+"-"+pluginCfg.Name+".log")
				var logFile *os.File
				var logErr error
				pluginLogger, _, logFile, logErr = logging.UpdateLoggerToFile(parentLogger, loadedConfig.Log.FileDir, "-"+pluginCfg.Name)
				if logErr != nil {
					parentLogger.ErrorContext(pluginCtx, "unable to update logger to file", "error", logErr)
					workerCancel()
					return
				}
				defer func() {
					_ = logFile.Close()
				}()
			}

			deps := plugin.Deps{
				Logger:         pluginLogger,
				Config:         &loadedConfig,
				PluginConfig:   pluginCfg,
				ConfigFileName: configFileName,
				Globals:        workerGlobals,
				Metrics:        metricsData,
				HTTPTransport:  httpTransport,
			}
			pluginCtx = deps.WithContext(pluginCtx)

			logging.Info(pluginCtx, "starting plugin")

			switch pluginCfg.GitHubScope() {
			case config.GitHubScopeEnterprise:
				logging.Info(pluginCtx, "enterprise set for plugin; using enterprise level plugin")
			case config.GitHubScopeOrganization:
				logging.Info(pluginCtx, "no repo set for plugin; assuming it's an organization level plugin")
			}

			isReceiver := run.IsReceiverType(pluginCfg.Plugin) || pluginCfg.IsReceiver()
			if !isReceiver {
				logging.Dev(pluginCtx, "plugin is not a receiver; loading the anka CLI")
				ankaCLI, cliErr := anka.NewCLI(pluginCtx)
				if cliErr != nil {
					logging.Error(pluginCtx, "unable to create anka cli", "error", cliErr)
					workerCancel()
					return
				}
				deps.AnkaCLI = ankaCLI
				pluginCtx = deps.WithContext(pluginCtx)
				logging.Dev(pluginCtx, "loaded the anka CLI")

				if config.AnyHostToGuestFolderMountsConfigured(loadedConfig) {
					if runtime.GOARCH == "amd64" {
						logging.Error(pluginCtx, "host-to-guest folder mounts are configured but Anka does not support this on Intel macOS hosts (Apple Silicon only); see Anka 3.9.0 release notes")
						workerCancel()
						return
					}
					if verErr := anka.ValidateAnkaVersionForHostToGuestFolderMounts(ankaCLI.Version); verErr != nil {
						logging.Error(pluginCtx, "mounting host folders into the guest VM requires Anka >= 3.9.0", "error", verErr, "ankaVersion", ankaCLI.Version)
						workerCancel()
						return
					}
					if len(pluginCfg.HostToGuestFolderMounts) > 0 {
						logging.Info(pluginCtx, "plugin host-to-guest folder mounts configured", "mounts", pluginCfg.HostToGuestFolderMounts)
					}
					if len(loadedConfig.GlobalHostToGuestFolderMounts) > 0 {
						logging.Info(pluginCtx, "global host-to-guest folder mounts configured", "mounts", loadedConfig.GlobalHostToGuestFolderMounts)
					}
				}

				if workerGlobals.TemplateTracker.Len() == 0 {
					logging.Dev(pluginCtx, "discovering existing templates on system")
					if discErr := ankaCLI.DiscoverAndPopulateExistingTemplates(pluginCtx, workerGlobals.TemplateTracker); discErr != nil {
						logging.Warn(pluginCtx, "failed to discover existing templates", "error", discErr)
					}
				}
				logging.Debug(pluginCtx, "populated existing templates in TemplateTracker", "templates", workerGlobals.TemplateTracker)
			}

			resolvedDB := config.ResolveDatabaseConfig(loadedConfig, pluginCfg.Database)
			if resolvedDB.URL != "" {
				logging.Dev(pluginCtx, "connecting to database")
				dbCtx := context.WithValue(context.Background(), config.ContextKey("logger"), pluginLogger)
				databaseClient, dbErr := database.NewClient(dbCtx, resolvedDB)
				if dbErr != nil {
					logging.Error(pluginCtx, "unable to access database", "error", dbErr)
					workerCancel()
					return
				}
				deps.Database = databaseClient
				pluginCtx = deps.WithContext(pluginCtx)
				logging.Dev(pluginCtx, "connected to database")

				if idx == 0 {
					capturedOwner := pluginCfg.MetricsOwner()
					capturedName := pluginCfg.Name
					capturedLogger := pluginLogger
					capturedDatabase := databaseClient
					defer func() {
						metricsCleanupCtx := context.WithValue(context.Background(), config.ContextKey("logger"), capturedLogger)
						cleanupDb := *capturedDatabase
						cleanupDb.MaxRetries = 0
						metricsCleanupCtx = context.WithValue(metricsCleanupCtx, config.ContextKey("database"), &cleanupDb)
						metrics.Cleanup(metricsCleanupCtx, capturedOwner, capturedName)
					}()
				}
			}

			if addErr := metricsData.AddPlugin(metrics.Plugin{
				PluginBase: metrics.PluginBase{
					Name:        pluginCfg.Name,
					PluginName:  pluginCfg.Plugin,
					RepoName:    pluginCfg.Repo,
					OwnerName:   pluginCfg.MetricsOwner(),
					IsReceiver:  isReceiver,
					Status:      "idle",
					StatusSince: time.Now(),
				},
			}); addErr != nil {
				parentLogger.ErrorContext(pluginCtx, "error adding plugin to metrics", "error", addErr)
				workerCancel()
				return
			}

			if idx == 0 {
				metrics.ExportMetricsToDB(workerCtx, pluginCtx, loadedConfig.Plugins[0].MetricsKeyEnding())
			}

			runner, buildErr := run.Build(deps)
			if buildErr != nil {
				logging.Error(pluginCtx, "error running plugin", "error", buildErr)
				workerCancel()
				return
			}

			runPluginLoop(
				workerCtx,
				workerCancel,
				pluginCtx,
				pluginCancel,
				pluginCfg,
				idx,
				workerGlobals,
				metricsData,
				&toRunOnce,
				runner,
			)
		}(index, p)
	}

	if len(loadedConfig.Plugins) == 0 {
		<-workerCtx.Done()
	}

	wg.Wait()
	workerCancel()
	select {
	case <-metricsDone:
	case <-time.After(5 * time.Second):
	}
}

func runPluginLoop(
	workerCtx context.Context,
	workerCancel context.CancelFunc,
	pluginCtx context.Context,
	pluginCancel context.CancelFunc,
	pluginCfg config.Plugin,
	idx int,
	workerGlobals *config.Globals,
	metricsData *metrics.MetricsDataLock,
	toRunOnce *atomic.Bool,
	runner plugin.Runner,
) {
	shutdownPlugin := func() {
		if statusErr := metricsData.SetStatus(pluginCtx, "stopped"); statusErr != nil {
			logging.Error(pluginCtx, "error setting plugin status", "error", statusErr)
		}
		logging.Warn(pluginCtx, shutDownMessage)
		pluginCancel()
		workerCancel()
	}

	for {
		select {
		case <-pluginCtx.Done():
			shutdownPlugin()
			return
		default:
			if workerGlobals.Plugins[pluginCfg.Plugin][pluginCfg.Name].PluginRunCount.Load() != 0 && idx != 0 {
				for workerGlobals.GetPausedPlugin() != "" {
					if workerCtx.Err() != nil || pluginCtx.Err() != nil {
						logging.Warn(pluginCtx, "context canceled while waiting for paused plugin")
						shutdownPlugin()
						return
					}
					logging.Dev(pluginCtx, "paused for another plugin to finish running")
					if statusErr := metricsData.SetStatus(pluginCtx, "paused"); statusErr != nil {
						logging.Error(pluginCtx, "error setting plugin status", "error", statusErr)
					}
					pauseTimer := time.NewTimer(3 * time.Second)
					select {
					case <-pauseTimer.C:
					case <-pluginCtx.Done():
						pauseTimer.Stop()
						shutdownPlugin()
						return
					case <-workerCtx.Done():
						pauseTimer.Stop()
						shutdownPlugin()
						return
					}
				}
			}

			preparing := false
			for siblingPluginName, siblingPlugin := range workerGlobals.Plugins[pluginCfg.Plugin] {
				if siblingPluginName == pluginCfg.Name {
					continue
				}
				if siblingPlugin.Preparing.Load() {
					logging.Dev(pluginCtx, "waiting for the previous plugin to finish preparing")
					preparing = true
					break
				}
			}
			if preparing {
				prepTimer := time.NewTimer(3 * time.Second)
				select {
				case <-prepTimer.C:
				case <-pluginCtx.Done():
					prepTimer.Stop()
					shutdownPlugin()
					return
				case <-workerCtx.Done():
					prepTimer.Stop()
					shutdownPlugin()
					return
				}
				continue
			}

			if statusErr := metricsData.SetStatus(pluginCtx, steadyStatus(pluginCfg)); statusErr != nil {
				logging.Error(pluginCtx, "error setting plugin status", "error", statusErr)
			}

			workerGlobals.IncrementPluginRunCount(pluginCfg.Name)

			iterationCtx := logging.AppendCtx(pluginCtx, slog.Int64("queueTargetIndex", workerGlobals.GetQueueTargetIndex()))
			updatedPluginCtx, runErr := run.Plugin(workerCtx, iterationCtx, pluginCancel, runner)
			if runErr != nil {
				logging.Error(updatedPluginCtx, "error running plugin", "error", runErr)
				pluginCancel()
				workerCancel()
				if proc, procErr := os.FindProcess(os.Getpid()); procErr != nil {
					logging.Error(updatedPluginCtx, "error finding process", "error", procErr)
				} else if sigErr := proc.Signal(syscall.SIGQUIT); sigErr != nil {
					logging.Error(updatedPluginCtx, "error sending SIGQUIT signal", "error", sigErr)
				}
				return
			}
			if workerCtx.Err() != nil || toRunOnce.Load() {
				shutdownPlugin()
				return
			}
			if statusErr := metricsData.SetStatus(updatedPluginCtx, steadyStatus(pluginCfg)); statusErr != nil {
				logging.Error(pluginCtx, "error setting plugin status", "error", statusErr)
			}

			sleepTimer := time.NewTimer(time.Duration(pluginCfg.SleepInterval) * time.Second)
			select {
			case <-sleepTimer.C:
			case <-pluginCtx.Done():
				sleepTimer.Stop()
				shutdownPlugin()
				return
			case <-workerCtx.Done():
				sleepTimer.Stop()
				shutdownPlugin()
				return
			}
		}
	}
}
