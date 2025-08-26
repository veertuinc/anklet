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
		ReturnAllToMainQueue: atomic.Bool{},
		PluginsPath:          pluginsPath,
		DebugEnabled:         logging.IsDebugEnabled(),
		HostCPUCount:         hostCPUCount,
		HostMemoryBytes:      hostMemoryBytes,
		QueueTargetIndex:     new(int64),
		TemplateTracker:      config.NewTemplateTracker(),
		Plugins: func() map[string]map[string]*config.PluginGlobal {
			plugins := make(map[string]map[string]*config.PluginGlobal)
			for _, p := range loadedConfig.Plugins {
				if plugins[p.Plugin] == nil {
					plugins[p.Plugin] = make(map[string]*config.PluginGlobal)
				}
				plugins[p.Plugin][p.Name] = &config.PluginGlobal{
					PluginRunCount:     atomic.Uint64{},
					Preparing:          atomic.Bool{}, // false
					FinishedInitialRun: atomic.Bool{}, // false
					Paused:             atomic.Bool{}, // true
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
	// Set each plugin's state
	for _, pluginGroup := range workerGlobals.Plugins {
		for _, pluginGlobal := range pluginGroup {
			// paused by default
			pluginGlobal.Paused.Store(true)
		}
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
				// Don't call workerCancel() immediately - let plugins finish gracefully
				// The plugins will detect ReturnAllToMainQueue and return jobs to main queue
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

	// TODO: move this into a function/different file
	// Setup Metrics Server and context
	metricsPort := "8080"
	if loadedConfig.Metrics.Port != "" {
		metricsPort = loadedConfig.Metrics.Port
	} else {
		for {
			ln, err := net.Listen("tcp", ":"+metricsPort)
			if err == nil {
				err = ln.Close()
				if err != nil {
					parentLogger.ErrorContext(workerCtx, "error closing metrics port", "port", metricsPort, "error", err)
					os.Exit(1)
				}
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
	err = ln.Close()
	if err != nil {
		parentLogger.ErrorContext(workerCtx, "error closing metrics port", "port", metricsPort, "error", err)
		os.Exit(1)
	}
	metricsService := metrics.NewServer(metricsPort)
	if loadedConfig.Metrics.Aggregator {
		var metricsServiceDatabaseURL = loadedConfig.GlobalDatabaseURL
		var metricsServiceDatabasePort = loadedConfig.GlobalDatabasePort
		var metricsServiceDatabaseUser = loadedConfig.GlobalDatabaseUser
		var metricsServiceDatabasePassword = loadedConfig.GlobalDatabasePassword
		var metricsServiceDatabaseDatabase = loadedConfig.GlobalDatabaseDatabase
		if metricsServiceDatabaseURL == "" {
			metricsServiceDatabaseURL = loadedConfig.Metrics.Database.URL
		}
		if metricsServiceDatabasePort == 0 {
			metricsServiceDatabasePort = loadedConfig.Metrics.Database.Port
		}
		if metricsServiceDatabaseUser == "" {
			metricsServiceDatabaseUser = loadedConfig.Metrics.Database.User
		}
		if metricsServiceDatabasePassword == "" {
			metricsServiceDatabasePassword = loadedConfig.Metrics.Database.Password
		}
		if metricsServiceDatabaseDatabase == 0 {
			metricsServiceDatabaseDatabase = loadedConfig.Metrics.Database.Database
		}
		databaseContainer, err := database.NewClient(workerCtx, config.Database{
			URL:      metricsServiceDatabaseURL,
			Port:     metricsServiceDatabasePort,
			User:     metricsServiceDatabaseUser,
			Password: metricsServiceDatabasePassword,
			Database: metricsServiceDatabaseDatabase,
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
		pluginCtx, pluginCancel := context.WithCancel(context.Background())
		for {
			select {
			case <-workerCtx.Done():
				pluginCancel()
				// parentLogger.WarnContext(pluginCtx, shutDownMessage+" inside plugin loop (aggregator)")
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
		metricsData := &metrics.MetricsDataLock{}
		workerCtx = context.WithValue(workerCtx, config.ContextKey("metrics"), metricsData)
		parentLogger.InfoContext(workerCtx, "metrics server started on port "+metricsPort)
		metrics.UpdateSystemMetrics(workerCtx, metricsData)
		/////////////
		// Plugins //
		soloReceiver := false
		for index, plugin := range loadedConfig.Plugins {
			wg.Add(1)
			// support starting the plugins in the order they're listed in the config one by one
			if index != 0 {
			waitLoop:
				for {
					select {
					case <-workerCtx.Done():
						return
					default:
						if workerGlobals.Plugins[loadedConfig.Plugins[index-1].Plugin][loadedConfig.Plugins[index-1].Name].FinishedInitialRun.Load() {
							// parentLogger.DebugContext(workerCtx, "previous plugin finished initial run, continuing", "plugin.Name", plugin.Name, "plugin.Plugin", plugin.Plugin)
							workerGlobals.Plugins[plugin.Plugin][plugin.Name].Paused.Store(false)
							break waitLoop
						}
						// Check for shutdown signal before sleeping
						if workerCtx.Err() != nil {
							logging.Warn(workerCtx, "context canceled while waiting for previous plugin to finish initial run")
							return
						}
						time.Sleep(time.Second * 5)
					}
				}
			}
			go func(plugin config.Plugin) {
				defer wg.Done()
				pluginCtx, pluginCancel := context.WithCancel(context.Background()) // we don't use workerCtx here as it causes Attrs to race condition and be incorrect

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
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("config"), workerCtx.Value(config.ContextKey("config")))
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("globals"), workerCtx.Value(config.ContextKey("globals")))
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("httpTransport"), workerCtx.Value(config.ContextKey("httpTransport")))
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("configFileName"), workerCtx.Value(config.ContextKey("configFileName")))

				// so logging with attributes works
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("plugin"), plugin)

				logging.Info(pluginCtx, "starting plugin")

				if plugin.Repo == "" {
					logging.Info(pluginCtx, "no repo set for plugin; assuming it's an organization level plugin")
				}

				if plugin.PrivateKey == "" && loadedConfig.GlobalPrivateKey != "" {
					logging.Dev(pluginCtx, "using global private key")
					plugin.PrivateKey = loadedConfig.GlobalPrivateKey
				}

				// keep this here or the changes to plugin don't get set in the pluginCtx
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("plugin"), plugin)

				if !strings.Contains(plugin.Plugin, "_receiver") {
					logging.Dev(pluginCtx, "plugin is not a receiver; loading the anka CLI")
					ankaCLI, err := anka.NewCLI(pluginCtx)
					if err != nil {
						logging.Error(pluginCtx, "unable to create anka cli", "error", err)
						pluginCancel()
						workerCancel()
						return
					}
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("ankacli"), ankaCLI)
					logging.Dev(pluginCtx, "loaded the anka CLI")

					// Discover and populate existing templates in the TemplateTracker
					// This only needs to be done once per system, so we check if templates are already populated
					if len(workerGlobals.TemplateTracker.Templates) == 0 {
						logging.Dev(pluginCtx, "discovering existing templates on system")
						err = ankaCLI.DiscoverAndPopulateExistingTemplates(pluginCtx, workerGlobals.TemplateTracker)
						if err != nil {
							logging.Warn(pluginCtx, "failed to discover existing templates", "error", err)
							// Don't fail the plugin startup for this, just log the warning
						}
					}
					logging.Debug(pluginCtx, "populated existing templates in TemplateTracker", "templates", workerGlobals.TemplateTracker.Templates)
				}

				var databaseURL = loadedConfig.GlobalDatabaseURL
				var databasePort = loadedConfig.GlobalDatabasePort
				var databaseUser = loadedConfig.GlobalDatabaseUser
				var databasePassword = loadedConfig.GlobalDatabasePassword
				var databaseDatabase = loadedConfig.GlobalDatabaseDatabase

				if databaseURL != "" || plugin.Database.URL != "" {
					if databaseURL == "" {
						databaseURL = plugin.Database.URL
					}
					if databasePort == 0 {
						databasePort = plugin.Database.Port
					}
					if databaseUser == "" {
						databaseUser = plugin.Database.User
					}
					if databasePassword == "" {
						databasePassword = plugin.Database.Password
					}
					if databaseDatabase == 0 {
						databaseDatabase = plugin.Database.Database
					}
					logging.Dev(pluginCtx, "connecting to database")
					databaseClient, err := database.NewClient(pluginCtx, config.Database{
						URL:      databaseURL,
						Port:     databasePort,
						User:     databaseUser,
						Password: databasePassword,
						Database: databaseDatabase,
					})
					if err != nil {
						logging.Error(pluginCtx, "unable to access database", "error", err)
						pluginCancel()
						workerCancel()
						return
					}
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("database"), databaseClient)
					logging.Dev(pluginCtx, "connected to database")
					// cleanup metrics data when the plugin is stopped (otherwise it's orphaned in the aggregator)
					if index == 0 {
						defer metrics.Cleanup(pluginCtx, plugin.Owner, plugin.Name)
					}
				}

				// Metrics for the plugin
				err = metricsData.AddPlugin(metrics.Plugin{
					PluginBase: &metrics.PluginBase{
						Name:        plugin.Name,
						PluginName:  plugin.Plugin,
						RepoName:    plugin.Repo,
						OwnerName:   plugin.Owner,
						Status:      "idle",
						StatusSince: time.Now(),
					},
				})
				if err != nil {
					parentLogger.ErrorContext(pluginCtx, "error adding plugin to metrics", "error", err)
					workerCancel()
					pluginCancel()
					return
				}
				// the key is the first plugin in the list's name
				// The goRoutine inside shouild continue to run so only run this once
				if index == 0 {
					metrics.ExportMetricsToDB(workerCtx, pluginCtx, loadedConfig.Plugins[0].Owner+"/"+loadedConfig.Plugins[0].Name)
				}

				for {
					select {
					case <-pluginCtx.Done():
						err = metricsData.SetStatus(pluginCtx, "stopped")
						if err != nil {
							logging.Error(pluginCtx, "error setting plugin status", "error", err)
						}
						logging.Warn(pluginCtx, shutDownMessage)
						pluginCancel()
						workerCancel()
						return
					default:
						// don't paused if it's the initial start and first plugin
						if workerGlobals.Plugins[plugin.Plugin][plugin.Name].PluginRunCount.Load() != 0 &&
							index != 0 {
							// pause all other plugins when we find one that's paused
							for workerGlobals.GetPausedPlugin() != "" {
								// Check for shutdown signal
								if workerCtx.Err() != nil || pluginCtx.Err() != nil {
									logging.Warn(pluginCtx, "context canceled while waiting for paused plugin")
									pluginCancel()
									workerCancel()
									return
								}

								logging.Dev(pluginCtx, "paused for another plugin to finish running")
								// When paused, sleep briefly and continue checking
								err = metricsData.SetStatus(pluginCtx, "paused")
								if err != nil {
									logging.Error(pluginCtx, "error setting plugin status", "error", err)
								}
								time.Sleep(time.Second * 3)
								continue
							}
						}

						// we need to wait for other plugins (of same type) on this host to finish preparing
						preparing := false
						for siblingPluginName, siblingPlugin := range workerGlobals.Plugins[plugin.Plugin] {
							if siblingPluginName == plugin.Name {
								continue
							}
							if siblingPlugin.Preparing.Load() {
								logging.Dev(pluginCtx, "paused for the previous plugin to finish preparing")
								err = metricsData.SetStatus(pluginCtx, "paused")
								if err != nil {
									logging.Error(pluginCtx, "error setting plugin status", "error", err)
								}
								preparing = true
								break
							}
						}
						if preparing {
							time.Sleep(time.Second * 3)
							continue
						}

						workerGlobals.IncrementPluginRunCount(plugin.Name)

						// Create a fresh context for this iteration to avoid accumulating QueueTargetIndex
						iterationCtx := logging.AppendCtx(pluginCtx, slog.Int64("queueTargetIndex", *workerGlobals.QueueTargetIndex))

						updatedPluginCtx, err := run.Plugin(
							workerCtx,
							iterationCtx,
							pluginCancel,
						)
						if err != nil {
							logging.Error(updatedPluginCtx, "error running plugin", "error", err)
							pluginCancel()
							workerCancel()
							// Send SIGQUIT to the main pid
							p, err := os.FindProcess(os.Getpid())
							if err != nil {
								logging.Error(updatedPluginCtx, "error finding process", "error", err)
							} else {
								err = p.Signal(syscall.SIGQUIT)
								if err != nil {
									logging.Error(updatedPluginCtx, "error sending SIGQUIT signal", "error", err)
								}
							}
							return
						}
						if workerCtx.Err() != nil || toRunOnce {
							// logging.Warn(updatedPluginCtx, shutDownMessage+" inside plugin loop 2")
							pluginCancel()
							break
						}
						err = metricsData.SetStatus(updatedPluginCtx, "idle")
						if err != nil {
							logging.Error(updatedPluginCtx, "error setting plugin status", "error", err)
						}
						select {
						case <-time.After(time.Duration(plugin.SleepInterval) * time.Second):
						case <-pluginCtx.Done():
							//logging.Dev(pluginCtx, "plugin for loop::default::pluginCtx.Done()")
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
		// wg.Add(1)
		go func() {
			metricsService.Start(workerCtx, soloReceiver)
		}()
	}
	wg.Wait()
	time.Sleep(time.Second) // prevents exiting before the logger has a chance to write the final log entry (from panics)
	parentLogger.WarnContext(parentCtx, "anklet (and all plugins) shut down")
	os.Exit(0)
}
