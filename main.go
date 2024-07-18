package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/gofri/go-github-ratelimit/github_ratelimit"
	"github.com/norsegaud/go-daemon"
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
	signalFlag  = flag.String("s", "", `Send signal to the daemon:
  drain — graceful shutdown, will wait until all jobs finish before exiting
  stop — best effort graceful shutdown, interrupting the job as soon as possible`)
	attachFlag      = flag.Bool("attach", false, "Attach to the anklet and don't background it (useful for containers)")
	stop            = make(chan struct{})
	done            = make(chan struct{})
	shutDownMessage = "anklet service shut down"
)

func termHandler(ctx context.Context, logger *slog.Logger) daemon.SignalHandlerFunc {
	return func(sig os.Signal) error {
		logger.WarnContext(ctx, "terminating anklet, please do not interrupt...")
		stop <- struct{}{}
		if sig == syscall.SIGQUIT {
			<-done
		}
		return daemon.ErrStop
	}
}

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
	daemon.AddCommand(daemon.StringFlag(signalFlag, "drain"), syscall.SIGQUIT, termHandler(parentCtx, logger))
	daemon.AddCommand(daemon.StringFlag(signalFlag, "stop"), syscall.SIGTERM, termHandler(parentCtx, logger))

	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	var configPath string
	if *configFlag != "" {
		configPath = *configFlag
	} else {
		configPath = filepath.Join(homeDir, ".config", "anklet", "config.yml")
	}

	// obtain config
	loadedConfig, err := config.LoadConfig(configPath)
	if err != nil {
		logger.InfoContext(parentCtx, "unable to load config.yml", "error", err)
		// panic(err)
	}
	logger.InfoContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))
	loadedConfig, err = config.LoadInEnvs(loadedConfig)
	if err != nil {
		panic(err)
	}

	parentCtx = logging.AppendCtx(parentCtx, slog.String("ankletVersion", version))

	var suffix string
	if loadedConfig.Metrics.Aggregator {
		suffix = "-aggregator"
	}
	parentCtx = context.WithValue(parentCtx, config.ContextKey("suffix"), suffix)

	if loadedConfig.Log.FileDir == "" {
		loadedConfig.Log.FileDir = "./"
	}
	if loadedConfig.PidFileDir == "" {
		loadedConfig.PidFileDir = "./"
	}
	if loadedConfig.WorkDir == "" {
		loadedConfig.WorkDir = "./"
	}

	logger.DebugContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))
	parentCtx = context.WithValue(parentCtx, config.ContextKey("config"), &loadedConfig)

	daemonContext := &daemon.Context{
		PidFileName: loadedConfig.PidFileDir + "anklet" + suffix + ".pid",
		PidFilePerm: 0644,
		LogFileName: loadedConfig.Log.FileDir + "anklet" + suffix + ".log",
		LogFilePerm: 0640,
		WorkDir:     loadedConfig.WorkDir,
		Umask:       027,
		Args:        []string{"anklet", "-c", configPath},
	}

	if len(daemon.ActiveFlags()) > 0 {
		d, err := daemonContext.Search()
		if err != nil {
			log.Fatalf("Unable send signal to the daemon: %s", err.Error())
		}
		err = daemon.SendCommands(d)
		if err != nil {
			log.Fatalln(err.Error())
		}
		return
	}

	pluginsPath := filepath.Join(homeDir, ".config", "anklet", "plugins")
	parentCtx = context.WithValue(parentCtx, config.ContextKey("globals"), config.Globals{
		RunOnce:     runOnce,
		PullLock:    &sync.Mutex{},
		PluginsPath: pluginsPath,
	})

	httpTransport := http.DefaultTransport
	parentCtx = context.WithValue(parentCtx, config.ContextKey("httpTransport"), httpTransport)

	githubServiceExists := false
	for _, service := range loadedConfig.Services {
		if service.Plugin == "github" {
			githubServiceExists = true
		}
	}
	if githubServiceExists {
		rateLimiter, err := github_ratelimit.NewRateLimitWaiterClient(httpTransport)
		if err != nil {
			logger.ErrorContext(parentCtx, "error creating github_ratelimit.NewRateLimitWaiterClient", "err", err)
			return
		}
		parentCtx = context.WithValue(parentCtx, config.ContextKey("rateLimiter"), rateLimiter)
	}

	if !*attachFlag {
		d, err := daemonContext.Reborn()
		if err != nil {
			log.Fatalln(err)
		}
		if d != nil {
			return
		}
		defer daemonContext.Release()
	}

	go worker(parentCtx, logger, loadedConfig)

	err = daemon.ServeSignals()
	if err != nil {
		log.Printf("Error: %s", err.Error())
	}
}

func worker(parentCtx context.Context, logger *slog.Logger, loadedConfig config.Config) {
	globals := config.GetGlobalsFromContext(parentCtx)
	toRunOnce := globals.RunOnce
	workerCtx, workerCancel := context.WithCancel(parentCtx)
	suffix := parentCtx.Value(config.ContextKey("suffix")).(string)
	logger.InfoContext(workerCtx, "starting anklet"+suffix)
	var wg sync.WaitGroup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGQUIT)
	go func() {
		defer signal.Stop(sigChan)
		defer close(sigChan)
		for sig := range sigChan {
			switch sig {
			case syscall.SIGTERM:
				logger.WarnContext(workerCtx, "best effort graceful shutdown, interrupting the job as soon as possible...")
				workerCancel()
			case syscall.SIGQUIT:
				logger.WarnContext(workerCtx, "graceful shutdown, waiting for jobs to finish...")
				toRunOnce = "true"
			}
		}
	}()

	// Setup Metrics Server and context
	metricsPort := "8080"
	if loadedConfig.Metrics.Port != "" {
		metricsPort = loadedConfig.Metrics.Port
	}
	metricsService := metrics.NewServer(metricsPort)
	if loadedConfig.Metrics.Aggregator {
		workerCtx = logging.AppendCtx(workerCtx, slog.Any("metrics_urls", loadedConfig.Metrics.MetricsURLs))
		databaseContainer, err := database.NewClient(workerCtx, loadedConfig.Metrics.Database)
		if err != nil {
			panic(fmt.Sprintf("unable to access database: %v", err))
		}
		workerCtx = context.WithValue(workerCtx, config.ContextKey("database"), databaseContainer)
		go metricsService.StartAggregatorServer(workerCtx, logger)
		logger.InfoContext(workerCtx, "metrics aggregator started on port "+metricsPort)
		for _, metricsURL := range loadedConfig.Metrics.MetricsURLs {
			wg.Add(1)
			go func(metricsURL string) {
				defer wg.Done()
				serviceCtx, serviceCancel := context.WithCancel(workerCtx) // Inherit from parent context
				serviceCtx = logging.AppendCtx(serviceCtx, slog.String("metrics_url", metricsURL))
				// check if valid URL
				_, err := url.Parse(metricsURL)
				if err != nil {
					logger.ErrorContext(serviceCtx, "invalid URL", "error", err)
					serviceCancel()
					return
				}
				for {
					select {
					case <-workerCtx.Done():
						serviceCancel()
						logger.WarnContext(serviceCtx, shutDownMessage)
						return
					default:
						// get metrics from endpoint and update the main list
						metrics.UpdatemetricsURLDBEntry(serviceCtx, logger, metricsURL)
						if workerCtx.Err() != nil || toRunOnce == "true" {
							serviceCancel()
							break
						}
						select {
						case <-time.After(time.Duration(loadedConfig.Metrics.SleepInterval) * time.Second):
						case <-serviceCtx.Done():
							break
						}
					}
				}
			}(metricsURL)
		}
	} else {
		metricsData := &metrics.MetricsDataLock{}
		workerCtx = context.WithValue(workerCtx, config.ContextKey("metrics"), metricsData)
		go metricsService.Start(workerCtx, logger)
		logger.InfoContext(workerCtx, "metrics server started on port "+metricsPort)
		metrics.UpdateSystemMetrics(workerCtx, logger, metricsData)
		/////////////
		// Services
		for _, service := range loadedConfig.Services {
			wg.Add(1)
			go func(service config.Service) {
				defer wg.Done()
				serviceCtx, serviceCancel := context.WithCancel(workerCtx) // Inherit from parent context
				// sigChan := make(chan os.Signal, 1)
				// signal.Notify(sigChan, syscall.SIGTERM, syscall.SIGQUIT)
				// go func() {
				// 	defer signal.Stop(sigChan)
				// 	defer close(sigChan)
				// 	for sig := range sigChan {
				// 		switch sig {
				// 		case syscall.SIGTERM:
				// 			serviceCancel()
				// 		case syscall.SIGQUIT:
				// 			runOnce = "true"
				// 		}
				// 	}
				// }()

				if service.Name == "" {
					panic("name is required for services")
				}

				serviceCtx = context.WithValue(serviceCtx, config.ContextKey("service"), service)
				serviceCtx = logging.AppendCtx(serviceCtx, slog.String("serviceName", service.Name))
				serviceCtx = context.WithValue(serviceCtx, config.ContextKey("logger"), logger)

				ankaCLI, err := anka.NewCLI(serviceCtx)
				if err != nil {
					panic(fmt.Sprintf("unable to create anka cli: %v", err))
				}

				serviceCtx = context.WithValue(serviceCtx, config.ContextKey("ankacli"), ankaCLI)

				if service.Database.Enabled {
					databaseClient, err := database.NewClient(serviceCtx, service.Database)
					if err != nil {
						panic(fmt.Sprintf("unable to access database: %v", err))
					}
					serviceCtx = context.WithValue(serviceCtx, config.ContextKey("database"), databaseClient)
				}

				logger.InfoContext(serviceCtx, "started service")
				metricsData.AddService(metrics.Service{
					Name:               service.Name,
					PluginName:         service.Plugin,
					RepoName:           service.Repo,
					OwnerName:          service.Owner,
					Status:             "idle",
					StatusRunningSince: time.Now(),
				})

				for {
					select {
					case <-serviceCtx.Done():
						metrics.UpdateService(workerCtx, serviceCtx, logger, metrics.Service{
							Status: "stopped",
						})
						logger.WarnContext(serviceCtx, shutDownMessage)
						serviceCancel()
						return
					default:
						run.Plugin(workerCtx, serviceCtx, serviceCancel, logger)
						if workerCtx.Err() != nil || toRunOnce == "true" {
							serviceCancel()
							logger.WarnContext(serviceCtx, shutDownMessage)
							return
						}
						metrics.UpdateService(workerCtx, serviceCtx, logger, metrics.Service{
							Status: "idle",
						})
						select {
						case <-time.After(time.Duration(service.SleepInterval) * time.Second):
						case <-serviceCtx.Done():
							logger.WarnContext(serviceCtx, shutDownMessage)
							break
						}
					}
				}
			}(service)
		}
	}
	wg.Wait()
	logger.WarnContext(workerCtx, "anklet (and all services) shut down")
	os.Exit(0)
}
