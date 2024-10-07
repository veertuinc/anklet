package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/veertuinc/anklet/internal/anka"
	"github.com/veertuinc/anklet/internal/config"
	"github.com/veertuinc/anklet/internal/database"
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

	logger := logging.New()
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
		panic(err)
	}
	var configPath string
	if *configFlag != "" {
		configPath = *configFlag
	} else {
		var configFileName string
		envConfigFileName := os.Getenv("ANKLET_CONFIG_FILE_NAME")
		if envConfigFileName != "" {
			configFileName = envConfigFileName
		} else {
			configFileName = "config.yml"
		}
		configPath = filepath.Join(homeDir, ".config", "anklet", configFileName)
	}

	// obtain config
	loadedConfig, err := config.LoadConfig(configPath)
	if err != nil {
		logger.ErrorContext(parentCtx, "unable to load config.yml (is it in the work_dir, or are you using an absolute path?)", "error", err)
		panic(err)
	}
	loadedConfig, err = config.LoadInEnvs(loadedConfig)
	if err != nil {
		panic(err)
	}
	logger.InfoContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))

	parentCtx = logging.AppendCtx(parentCtx, slog.String("ankletVersion", version))

	var suffix string
	if loadedConfig.Metrics.Aggregator {
		suffix = "-aggregator"
	}
	parentCtx = context.WithValue(parentCtx, config.ContextKey("suffix"), suffix)

	if loadedConfig.Log.FileDir != "" {
		logger, fileLocation, err := logging.UpdateLoggerToFile(logger, loadedConfig.Log.FileDir, suffix)
		if err != nil {
			logger.ErrorContext(parentCtx, "error updating logger to file", "error", err)
		}
		logger.InfoContext(parentCtx, "writing logs to file", slog.String("fileLocation", fileLocation))
		logger.InfoContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))
	}

	// if loadedConfig.PidFileDir == "" {
	// 	loadedConfig.PidFileDir = "./"
	// }
	if loadedConfig.WorkDir == "" {
		loadedConfig.WorkDir = "./"
	}

	logger.DebugContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))
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

	pluginsPath := filepath.Join(homeDir, ".config", "anklet", "plugins")
	parentCtx = context.WithValue(parentCtx, config.ContextKey("globals"), config.Globals{
		RunOnce:     runOnce,
		PullLock:    &sync.Mutex{},
		PluginsPath: pluginsPath,
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
			logger.ErrorContext(parentCtx, "error creating github_ratelimit.NewRateLimitWaiterClient", "err", err)
			return
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
	worker(parentCtx, logger, loadedConfig, sigChan)

	// err = daemon.ServeSignals()
	// if err != nil {
	// 	log.Printf("Error: %s", err.Error())
	// }
}

func worker(parentCtx context.Context, logger *slog.Logger, loadedConfig config.Config, sigChan chan os.Signal) {
	globals := config.GetGlobalsFromContext(parentCtx)
	toRunOnce := globals.RunOnce
	workerCtx, workerCancel := context.WithCancel(parentCtx)
	suffix := parentCtx.Value(config.ContextKey("suffix")).(string)
	logger.InfoContext(workerCtx, "starting anklet"+suffix)
	returnToMainQueue := make(chan bool, 1)
	workerCtx = context.WithValue(workerCtx, config.ContextKey("returnToMainQueue"), returnToMainQueue)
	var wg sync.WaitGroup
	go func() {
		defer signal.Stop(sigChan)
		defer close(sigChan)
		for sig := range sigChan {
			switch sig {
			// case syscall.SIGTERM:
			// 	logger.WarnContext(workerCtx, "best effort graceful shutdown, interrupting the job as soon as possible...")
			// 	workerCancel()
			case syscall.SIGQUIT: // doesn't work for receivers since they don't loop
				logger.WarnContext(workerCtx, "graceful shutdown, waiting for jobs to finish...")
				toRunOnce = "true"
			default:
				logger.WarnContext(workerCtx, "best effort graceful shutdown, interrupting the job as soon as possible...")
				workerCancel()
				returnToMainQueue <- true
			}
		}
	}()
	// Setup Metrics Server and context
	metricsPort := "8080"
	if loadedConfig.Metrics.Port != "" {
		metricsPort = loadedConfig.Metrics.Port
	}
	ln, err := net.Listen("tcp", ":"+metricsPort)
	if err != nil {
		logger.ErrorContext(workerCtx, "port already in use", "port", metricsPort, "error", err)
		panic(fmt.Sprintf("port %s is already in use", metricsPort))
	}
	ln.Close()
	metricsService := metrics.NewServer(metricsPort)
	if loadedConfig.Metrics.Aggregator {
		workerCtx = logging.AppendCtx(workerCtx, slog.Any("metrics_urls", loadedConfig.Metrics.MetricsURLs))
		databaseContainer, err := database.NewClient(workerCtx, loadedConfig.Metrics.Database)
		if err != nil {
			panic(fmt.Sprintf("unable to access database: %v", err))
		}
		workerCtx = context.WithValue(workerCtx, config.ContextKey("database"), databaseContainer)
		go metricsService.StartAggregatorServer(workerCtx, logger, false)
		logger.InfoContext(workerCtx, "metrics aggregator started on port "+metricsPort)
		for _, metricsURL := range loadedConfig.Metrics.MetricsURLs {
			wg.Add(1)
			go func(metricsURL string) {
				defer wg.Done()
				pluginCtx, pluginCancel := context.WithCancel(workerCtx) // Inherit from parent context
				pluginCtx = logging.AppendCtx(pluginCtx, slog.String("metrics_url", metricsURL))
				// check if valid URL
				_, err := url.Parse(metricsURL)
				if err != nil {
					logger.ErrorContext(pluginCtx, "invalid URL", "error", err)
					pluginCancel()
					return
				}
				for {
					select {
					case <-workerCtx.Done():
						pluginCancel()
						logger.WarnContext(pluginCtx, shutDownMessage)
						return
					default:
						// get metrics from endpoint and update the main list
						metrics.UpdatemetricsURLDBEntry(pluginCtx, logger, metricsURL)
						if workerCtx.Err() != nil || toRunOnce == "true" {
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
			}(metricsURL)
		}
	} else {
		// firstPluginStarted: always make sure the first plugin in the config starts first before any others.
		// this allows users to mix a receiver with multiple other plugins,
		// and let the receiver do its thing to prepare the db first.
		firstPluginStarted := make(chan bool, 1)
		metricsData := &metrics.MetricsDataLock{}
		workerCtx = context.WithValue(workerCtx, config.ContextKey("metrics"), metricsData)
		logger.InfoContext(workerCtx, "metrics server started on port "+metricsPort)
		metrics.UpdateSystemMetrics(workerCtx, logger, metricsData)
		/////////////
		// Plugins
		soloReceiver := false
		for index, plugin := range loadedConfig.Plugins {
			wg.Add(1)
			if index != 0 {
			waitLoop:
				for {
					select {
					case <-firstPluginStarted:
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

				if plugin.Name == "" {
					panic("name is required for plugins")
				}

				if plugin.Repo == "" {
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("isRepoSet"), false)
				} else {
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("isRepoSet"), true)
				}

				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("plugin"), plugin)
				pluginCtx = logging.AppendCtx(pluginCtx, slog.String("pluginName", plugin.Name))
				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("logger"), logger)

				ankaCLI, err := anka.NewCLI(pluginCtx)
				if err != nil {
					pluginCancel()
					logger.ErrorContext(pluginCtx, "unable to create anka cli", "error", err)
					return
				}

				pluginCtx = context.WithValue(pluginCtx, config.ContextKey("ankacli"), ankaCLI)

				if plugin.Database.Enabled {
					databaseClient, err := database.NewClient(pluginCtx, plugin.Database)
					if err != nil {
						panic(fmt.Sprintf("unable to access database: %v", err))
					}
					pluginCtx = context.WithValue(pluginCtx, config.ContextKey("database"), databaseClient)
				}

				logger.InfoContext(pluginCtx, "starting service")

				for {
					select {
					case <-pluginCtx.Done():
						metricsData.SetStatus(pluginCtx, logger, "stopped")
						logger.WarnContext(pluginCtx, shutDownMessage)
						pluginCancel()
						return
					default:
						run.Plugin(workerCtx, pluginCtx, pluginCancel, logger, firstPluginStarted, metricsData)
						if workerCtx.Err() != nil || toRunOnce == "true" {
							pluginCancel()
							logger.WarnContext(pluginCtx, shutDownMessage)
							return
						}
						metricsData.SetStatus(pluginCtx, logger, "idle")
						select {
						case <-time.After(time.Duration(plugin.SleepInterval) * time.Second):
						case <-pluginCtx.Done():
							fmt.Println("pluginCtx.Done()")
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
		go metricsService.Start(workerCtx, logger, soloReceiver)
	}
	wg.Wait()
	logger.WarnContext(workerCtx, "anklet (and all plugins) shut down")
	os.Exit(0)
}
