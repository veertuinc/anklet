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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
	"github.com/veertuinc/anklet/internal/host"
	"github.com/veertuinc/anklet/internal/logging"
	"github.com/veertuinc/anklet/internal/metrics"
	"github.com/veertuinc/anklet/internal/run"
)

var (
	version     = "dev"
	runOnce     = "false"
	versionFlag = flag.Bool("version", false, "Print the version")
	configFlag  = flag.String("c", "", "Path to the config file (defaults to ~/.config/anklet/config.yml)")
	// 	signalFlag  = flag.String("s", "", `Send signal to the daemon:
	//   drain — graceful shutdown, will wait until all jobs finish before exiting
	//   stop — best effort graceful shutdown, interrupting the job as soon as possible`)
	// attachFlag      = flag.Bool("attach", false, "Attach to the anklet and don't background it (useful for containers)")
	// stop            = make(chan struct{})
	// done            = make(chan struct{})
	shutDownMessage = "anklet plugin shut down"
)

// func termHandler(ctx context.Context, logger *slog.Logger) daemon.SignalHandlerFunc {
// 	return func(sig os.Signal) error {
// 		logger.WarnContext(ctx, "terminating anklet, please do not interrupt...")
// 		stop <- struct{}{}
// 		if sig == syscall.SIGQUIT {
// 			<-done
// 		}
// 		return daemon.ErrStop
// 	}
// }

func main() {

	parentLogger := logging.New()
	parentCtx := context.Background()

	if version == "" {
		version = "dev" // Default version if not set by go build
	}

	flag.Parse()
	if *versionFlag {
		fmt.Println(version)
		os.Exit(0)
	}
	// daemon.AddCommand(daemon.StringFlag(signalFlag, "drain"), syscall.SIGQUIT, termHandler(parentCtx, logger))
	// daemon.AddCommand(daemon.StringFlag(signalFlag, "stop"), syscall.SIGTERM, termHandler(parentCtx, logger))

	homeDir, err := os.UserHomeDir()
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "unable to get user home directory", "error", err)
		os.Exit(1)
	}

	var configPath string
	var configFileName string
	if *configFlag != "" {
		configPath = *configFlag
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

	// obtain config
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
	parentLogger.InfoContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))

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
		// logger, fileLocation, err = logging.UpdateLoggerToFile(logger, logFileDir, suffix)
		// if err != nil {
		// 	fmt.Printf("{\"time\":\"%s\",\"level\":\"ERROR\",\"msg\":\"%s\"}\n", time.Now().Format(time.RFC3339), err)
		// 	os.Exit(1)
		// }
		// logger.InfoContext(parentCtx, "writing logs to file", slog.String("fileLocation", fileLocation))
	}

	// must come after the log config is handled
	parentCtx = context.WithValue(parentCtx, config.ContextKey("logger"), parentLogger)

	// if loadedConfig.PidFileDir == "" {
	// 	loadedConfig.PidFileDir = "./"
	// }
	if loadedConfig.WorkDir == "" {
		loadedConfig.WorkDir = "./"
	}

	// Handle setting defaults for receiver plugins
	for index, plugin := range loadedConfig.Plugins {
		if strings.Contains(plugin.Plugin, "_receiver") {
			if plugin.RedeliverHours == 0 {
				loadedConfig.Plugins[index].RedeliverHours = 24
			}
			if loadedConfig.GlobalReceiverSecret != "" {
				loadedConfig.Plugins[index].Secret = loadedConfig.GlobalReceiverSecret
			}
		}
	}

	parentLogger.DebugContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))
	parentCtx = context.WithValue(parentCtx, config.ContextKey("config"), &loadedConfig)

	// daemonContext := &daemon.Context{
	// 	PidFileName: loadedConfig.PidFileDir + "anklet" + suffix + ".pid",
	// 	PidFilePerm: 0644,
	// 	LogFileName: loadedConfig.Log.FileDir + "anklet" + suffix + ".log",
	// 	LogFilePerm: 0640,
	// 	WorkDir:     loadedConfig.WorkDir,
	// 	Umask:       027,
	// 	Args:        []string{"anklet", "-c", configPath},
	// }

	// if len(daemon.ActiveFlags()) > 0 {
	// 	d, err := daemonContext.Search()
	// 	if err != nil {
	// 		log.Fatalf("Unable send signal to the daemon: %s", err.Error())
	// 	}
	// 	err = daemon.SendCommands(d)
	// 	if err != nil {
	// 		log.Fatalln(err.Error())
	// 	}
	// 	return
	// }

	var pluginsPath string
	if loadedConfig.PluginsPath != "" {
		pluginsPath = loadedConfig.PluginsPath
	} else {
		pluginsPath = filepath.Join(homeDir, ".config", "anklet", "plugins")
	}

	parentLogger.InfoContext(parentCtx, "plugins path", slog.String("pluginsPath", pluginsPath))

	hostCPUCount, err := host.GetHostCPUCount(parentCtx)
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "error getting host cpu count", "error", err)
		os.Exit(1)
	}
	parentCtx = logging.AppendCtx(parentCtx, slog.Int("hostCPUCount", hostCPUCount))
	hostMemoryBytes, err := host.GetHostMemoryBytes(parentCtx)
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "error getting host memory bytes", "error", err)
		os.Exit(1)
	}
	parentCtx = logging.AppendCtx(parentCtx, slog.Uint64("hostMemoryBytes", hostMemoryBytes))

	parentCtx = context.WithValue(parentCtx, config.ContextKey("globals"), &config.Globals{
		RunPluginsOnce:       runOnce == "true",
		FirstPluginStarted:   make(chan bool, 1),
		ReturnAllToMainQueue: atomic.Bool{},
		PullLock:             &sync.Mutex{},
		PluginsPath:          pluginsPath,
		DebugEnabled:         logging.IsDebugEnabled(),
		PluginsPaused:        atomic.Bool{},
		APluginIsPreparing:   atomic.Value{},
		HostCPUCount:         hostCPUCount,
		HostMemoryBytes:      hostMemoryBytes,
		QueueTargetIndex:     new(int64),
		Plugins: func() map[string]*config.PluginGlobal {
			plugins := make(map[string]*config.PluginGlobal)
			for _, p := range loadedConfig.Plugins {
				plugins[p.Name] = &config.PluginGlobal{
					PluginRunCount:     atomic.Uint64{},
					FinishedInitialRun: false,
				}
			}
			return plugins
		}(),
	})

	httpTransport := http.DefaultTransport
	parentCtx = context.WithValue(parentCtx, config.ContextKey("httpTransport"), httpTransport)

	githubPluginExists := false
	for _, plugin := range loadedConfig.Plugins {
		if plugin.Plugin == "github" || plugin.Plugin == "github_receiver" {
			githubPluginExists = true
		}
	}
	if githubPluginExists {
		rateLimiter, err := github_ratelimit.NewRateLimitWaiterClient(httpTransport)
		if err != nil {
			parentLogger.ErrorContext(parentCtx, "error creating github_ratelimit.NewRateLimitWaiterClient", "err", err)
			os.Exit(1)
		}
		parentCtx = context.WithValue(parentCtx, config.ContextKey("rateLimiter"), rateLimiter)
	}

	// if !*attachFlag {
	// 	d, err := daemonContext.Reborn()
	// 	if err != nil {
	// 		log.Fatalln(err)
	// 	}
	// 	if d != nil {
	// 		return
	// 	}
	// 	defer daemonContext.Release()
	// }

	// Capture ctrl+c and handle sending cancellation
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)

	//go
	worker(parentCtx, parentLogger, loadedConfig, sigChan)

	// err = daemon.ServeSignals()
	// if err != nil {
	// 	log.Printf("Error: %s", err.Error())
	// }
}

func worker(
	parentCtx context.Context,
	parentLogger *slog.Logger,
	loadedConfig config.Config,
	sigChan chan os.Signal,
) {
	workerGlobals, err := config.GetWorkerGlobalsFromContext(parentCtx)
	if err != nil {
		parentLogger.ErrorContext(parentCtx, "unable to get globals from context", "error", err)
		os.Exit(1)
	}
	toRunOnce := workerGlobals.RunPluginsOnce
	workerCtx, workerCancel := context.WithCancel(parentCtx)
	suffix := parentCtx.Value(config.ContextKey("suffix")).(string)
	parentLogger.InfoContext(workerCtx, "starting anklet"+suffix)
	var wg sync.WaitGroup
	go func() {
		defer signal.Stop(sigChan)
		defer close(sigChan)
		var sigCount int
		for sig := range sigChan {
			switch sig {
			case syscall.SIGQUIT: // doesn't work for receivers since they don't loop
				parentLogger.WarnContext(workerCtx, "graceful shutdown, waiting for jobs to finish...")
				workerGlobals.ReturnAllToMainQueue.Store(true)
				toRunOnce = true
			default:
				sigCount++
				if sigCount >= 2 {
					parentLogger.WarnContext(workerCtx, "forceful shutdown after second interrupt... be sure to clean up any self-hosted runners and VMs!")
					os.Exit(1)
				}
				parentLogger.WarnContext(workerCtx, "best effort graceful shutdown, interrupting the job as soon as possible...")
				workerCancel()
			}
		}
	}()

	// set global database variables
	var databaseURL string
	var databasePort int
	var databaseUser string
	var databasePassword string
	var databaseDatabase int
	if loadedConfig.GlobalDatabaseURL != "" {
		databaseURL = loadedConfig.GlobalDatabaseURL
		databasePort = loadedConfig.GlobalDatabasePort
		databaseUser = loadedConfig.GlobalDatabaseUser
		databasePassword = loadedConfig.GlobalDatabasePassword
		databaseDatabase = loadedConfig.GlobalDatabaseDatabase
	}

	// TODO: move this into a function/different file
	// Setup Metrics Server and context
	metricsPort := "8080"
	if loadedConfig.Metrics.Port != "" {
		metricsPort = loadedConfig.Metrics.Port
	} else {
		for {
			ln, err := net.Listen("tcp", ":"+metricsPort)
			if err == nil {
				ln.Close()
				break
			}
			port, _ := strconv.Atoi(metricsPort)
			port++
			metricsPort = strconv.Itoa(port)
		}
	}
	ln, err := net.Listen("tcp", ":"+metricsPort)
	if err != nil {
		parentLogger.ErrorContext(workerCtx, "metrics port already in use", "port", metricsPort, "error", err)
		os.Exit(1)
	}
	ln.Close()
	metricsService := metrics.NewServer(metricsPort)
	if loadedConfig.Metrics.Aggregator {
		if databaseURL == "" { // if no global database URL is set, use the metrics database URL
			databaseURL = loadedConfig.Metrics.Database.URL
			databasePort = loadedConfig.Metrics.Database.Port
			databaseUser = loadedConfig.Metrics.Database.User
			databasePassword = loadedConfig.Metrics.Database.Password
			databaseDatabase = loadedConfig.Metrics.Database.Database
		}
		databaseContainer, err := database.NewClient(workerCtx, config.Database{
			URL:      databaseURL,
			Port:     databasePort,
			User:     databaseUser,
			Password: databasePassword,
			Database: databaseDatabase,
		})
		if err != nil {
			parentLogger.ErrorContext(workerCtx, "unable to access database", "error", err)
			os.Exit(1)
		}
		workerCtx = context.WithValue(workerCtx, config.ContextKey("database"), databaseContainer)
		go metricsService.StartAggregatorServer(workerCtx, parentLogger, false)
		parentLogger.InfoContext(workerCtx, "metrics aggregator started on port "+metricsPort)
		wg.Add(1)
		defer wg.Done()
		pluginCtx, pluginCancel := context.WithCancel(workerCtx) // Inherit from parent context
		for {
			select {
			case <-workerCtx.Done():
				pluginCancel()
				parentLogger.WarnContext(pluginCtx, shutDownMessage)
				return
			default:
				if workerCtx.Err() != nil || toRunOnce {
					pluginCancel()
					break
				}
				select {
				case <-time.After(time.Duration(loadedConfig.Metrics.SleepInterval) * time.Second):
				case <-pluginCtx.Done():
					break
				}
			}
		}
	} else {
		// workerGlobals.FirstPluginStarted: always make sure the first plugin in the config starts first before any others.
		// this allows users to mix a receiver with multiple other plugins,
		// and let the receiver do its thing to prepare the db first.
		metricsData := &metrics.MetricsDataLock{}
		workerCtx = context.WithValue(workerCtx, config.ContextKey("metrics"), metricsData)
		parentLogger.InfoContext(workerCtx, "metrics server started on port "+metricsPort)
		metrics.UpdateSystemMetrics(workerCtx, parentLogger, metricsData)
		/////////////
		// Plugins //
		soloReceiver := false
		for index, plugin := range loadedConfig.Plugins {
			wg.Add(1)
			if index != 0 {
			waitLoop:
				for {
					select {
					case <-workerGlobals.FirstPluginStarted:
						break waitLoop
					case <-workerCtx.Done():
						return
					default:
						time.Sleep(100 * time.Millisecond)
					}
				}
			}
			go func(plugin config.Plugin) {
				defer wg.Done()
				pluginCtx, pluginCancel := context.WithCancel(workerCtx) // Inherit from parent context

				workerGlobals, err := config.GetWorkerGlobalsFromContext(workerCtx)
				if err != nil {
					parentLogger.ErrorContext(pluginCtx, "unable to get globals from context", "error", err)
					pluginCancel()
					workerCancel()
					return
				}

				if plugin.Name == "" {
					parentLogger.ErrorContext(pluginCtx, "name is required for plugins")
					pluginCancel()
					workerCancel()
					return
				}

				if strings.Contains(plugin.Name, " ") {
					parentLogger.ErrorContext(pluginCtx, "plugin name cannot contain spaces")
					pluginCancel()
					workerCancel()
					return
				}

				// support plugin specific log files
				var pluginLogger = parentLogger
				if loadedConfig.Log.SplitByPlugin {
					parentLogger.InfoContext(parentCtx, "writing "+plugin.Name+" logs to "+loadedConfig.Log.FileDir+"anklet"+"-"+plugin.Name+".log")
					pluginLogger, _, err = logging.UpdateLoggerToFile(parentLogger, loadedConfig.Log.FileDir, "-"+plugin.Name)
					if err != nil {
						parentLogger.ErrorContext(pluginCtx, "unable to update logger to file", "error", err)
						pluginCancel()
						workerCancel()
						return
					}
				}

				// must come after the log config is handled
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("logger"), pluginLogger)

				pluginCtx = logging.AppendCtx(pluginCtx, slog.String("pluginName", plugin.Name))

				if plugin.Repo == "" {
					pluginLogger.InfoContext(pluginCtx, "no repo set for plugin; assuming it's an organization level plugin")
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("isRepoSet"), false)
					// logging.DevContext(pluginCtx, "set isRepoSet to false")
				} else {
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("isRepoSet"), true)
					// logging.DevContext(pluginCtx, "set isRepoSet to true")
				}

				if plugin.PrivateKey == "" && loadedConfig.GlobalPrivateKey != "" {
					logging.DevContext(pluginCtx, "using global private key")
					plugin.PrivateKey = loadedConfig.GlobalPrivateKey
				}

				// keep this here or the changes to plugin don't get set in the pluginCtx
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("plugin"), plugin)

				if !strings.Contains(plugin.Plugin, "_receiver") {
					logging.DevContext(pluginCtx, "plugin is not a receiver; loading the anka CLI")
					ankaCLI, err := anka.NewCLI(pluginCtx)
					if err != nil {
						pluginLogger.ErrorContext(pluginCtx, "unable to create anka cli", "error", err)
						pluginCancel()
						workerCancel()
						return
					}
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("ankacli"), ankaCLI)
					logging.DevContext(pluginCtx, "loaded the anka CLI")
				}

				if databaseURL != "" || plugin.Database.URL != "" {
					if databaseURL == "" {
						databaseURL = plugin.Database.URL
						databasePort = plugin.Database.Port
						databaseUser = plugin.Database.User
						databasePassword = plugin.Database.Password
						databaseDatabase = plugin.Database.Database
					}
					logging.DevContext(pluginCtx, "connecting to database")
					databaseClient, err := database.NewClient(pluginCtx, config.Database{
						URL:      databaseURL,
						Port:     databasePort,
						User:     databaseUser,
						Password: databasePassword,
						Database: databaseDatabase,
					})
					if err != nil {
						pluginLogger.ErrorContext(pluginCtx, "unable to access database", "error", err)
						pluginCancel()
						workerCancel()
						return
					}
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("database"), databaseClient)
					// cleanup metrics data when the plugin is stopped (otherwise it's orphaned in the aggregator)
					if index == 0 { // only cleanup the first plugin's metrics data (they're aggregated by the first plugin's name)
						defer metrics.Cleanup(pluginCtx, pluginLogger, plugin.Owner, plugin.Name)
					}
					logging.DevContext(pluginCtx, "connected to database")
				}

				pluginLogger.InfoContext(pluginCtx, "starting plugin")

				for {
					select {
					case <-pluginCtx.Done():
						// logging.DevContext(pluginCtx, "plugin for loop::pluginCtx.Done()")
						metricsData.SetStatus(pluginCtx, pluginLogger, "stopped")
						pluginLogger.WarnContext(pluginCtx, shutDownMessage)
						pluginCancel()
						return
					default:

						// preparing here means the first start up of a plugin
						if workerGlobals.IsAPluginPreparingState() != "" {
							logging.DevContext(pluginCtx, "paused for another plugin to finish preparing")
							// When paused, sleep briefly and continue checking
							metricsData.SetStatus(pluginCtx, pluginLogger, "paused")
							time.Sleep(time.Second * 3)
							continue
						}

						if workerGlobals.ArePluginsPaused() {
							logging.DevContext(pluginCtx, "paused for another plugin to finish running")
							// When paused, sleep briefly and continue checking
							metricsData.SetStatus(pluginCtx, pluginLogger, "paused")
							time.Sleep(time.Second * 3)
							continue
						}

						workerGlobals.IncrementPluginRunCount(plugin.Name)

						pluginCtx = logging.AppendCtx(pluginCtx, slog.Int64("QueueTargetIndex", *workerGlobals.QueueTargetIndex))

						updatedPluginCtx, err := run.Plugin(
							workerCtx,
							pluginCtx,
							pluginCancel,
							pluginLogger,
							metricsData,
						)
						if err != nil {
							pluginLogger.ErrorContext(updatedPluginCtx, "error running plugin", "error", err)
							pluginCancel()
							workerCancel()
							// Send SIGQUIT to the main pid
							p, err := os.FindProcess(os.Getpid())
							if err != nil {
								pluginLogger.ErrorContext(updatedPluginCtx, "error finding process", "error", err)
							} else {
								err = p.Signal(syscall.SIGQUIT)
								if err != nil {
									pluginLogger.ErrorContext(updatedPluginCtx, "error sending SIGQUIT signal", "error", err)
								}
							}
							return
						}
						if workerCtx.Err() != nil || toRunOnce {
							pluginLogger.WarnContext(updatedPluginCtx, shutDownMessage)
							pluginCancel()
							return
						}
						metricsData.SetStatus(updatedPluginCtx, pluginLogger, "idle")
						select {
						case <-time.After(time.Duration(plugin.SleepInterval) * time.Second):
						case <-pluginCtx.Done():
							//logging.DevContext(pluginCtx, "plugin for loop::default::pluginCtx.Done()")
							break
						}
					}
				}
			}(plugin)
			// if the only service is a receiver, set the soloReceiver flag to true
			if strings.Contains(plugin.Plugin, "_receiver") {
				soloReceiver = true
			} else { // otherwise disable it if other plugins exist
				soloReceiver = false
			}
		}
		go metricsService.Start(workerCtx, parentLogger, soloReceiver)
	}
	wg.Wait()
	time.Sleep(time.Second) // prevents exiting before the logger has a chance to write the final log entry (from panics)
	parentLogger.WarnContext(parentCtx, "anklet (and all plugins) shut down")
	os.Exit(0)
}
