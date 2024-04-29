package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
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
	"github.com/veertuinc/anklet/internal/run"
)

var (
	version    = "dev"
	runOnce    = "false"
	signalFlag = flag.String("s", "", `Send signal to the daemon:
  drain — graceful shutdown, will wait until all jobs finish before exiting
  stop — best effort graceful shutdown, interrupting the job as soon as possible`)
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
	daemon.AddCommand(daemon.StringFlag(signalFlag, "drain"), syscall.SIGQUIT, termHandler(parentCtx, logger))
	daemon.AddCommand(daemon.StringFlag(signalFlag, "stop"), syscall.SIGTERM, termHandler(parentCtx, logger))

	homeDir, err := os.UserHomeDir()
	if err != nil {
		panic(err)
	}
	configPath := filepath.Join(homeDir, ".config", "anklet", "config.yml")

	// obtain config
	loadedConfig, err := config.LoadConfig(configPath)
	if err != nil {
		panic(err)
	}

	parentCtx = logging.AppendCtx(parentCtx, slog.String("ankletVersion", version))

	daemonContext := &daemon.Context{
		PidFileName: loadedConfig.PidFileDir + "anklet.pid",
		PidFilePerm: 0644,
		LogFileName: loadedConfig.Log.FileDir + "anklet.log",
		LogFilePerm: 0640,
		WorkDir:     loadedConfig.WorkDir,
		Umask:       027,
		Args:        []string{"anklet"},
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

	logger.DebugContext(parentCtx, "loaded config", slog.Any("config", loadedConfig))

	if loadedConfig.Log.FileDir == "" {
		logger.ErrorContext(parentCtx, "log > file_dir is not set in the configuration; be sure to use an absolute path")
		return
	}

	pluginsPath := filepath.Join(homeDir, ".config", "anklet", "plugins")
	parentCtx = context.WithValue(parentCtx, config.ContextKey("globals"), config.Globals{
		RunOnce:     runOnce,
		PullLock:    &sync.Mutex{},
		PluginsPath: pluginsPath,
	})

	githubServiceExists := false
	for _, service := range loadedConfig.Services {
		if service.Plugin == "github" {
			githubServiceExists = true
		}
	}
	if githubServiceExists {
		rateLimiter, err := github_ratelimit.NewRateLimitWaiterClient(nil)
		if err != nil {
			logger.ErrorContext(parentCtx, "error creating github_ratelimit.NewRateLimitWaiterClient", "err", err)
			return
		}
		fmt.Println("rateLimiter", rateLimiter)
		parentCtx = context.WithValue(parentCtx, config.ContextKey("rateLimiter"), rateLimiter)
	}

	d, err := daemonContext.Reborn()
	if err != nil {
		log.Fatalln(err)
	}
	if d != nil {
		return
	}
	defer daemonContext.Release()

	go worker(parentCtx, logger, *loadedConfig)

	err = daemon.ServeSignals()
	if err != nil {
		log.Printf("Error: %s", err.Error())
	}
}

func worker(parentCtx context.Context, logger *slog.Logger, loadedConfig config.Config) {
	globals := config.GetGlobalsFromContext(parentCtx)
	toRunOnce := globals.RunOnce
	workerCtx, cancel := context.WithCancel(parentCtx)
	logger.InfoContext(workerCtx, "starting anklet")
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
				cancel()
			case syscall.SIGQUIT:
				logger.WarnContext(workerCtx, "graceful shutdown, waiting for jobs to finish...")
				toRunOnce = "true"
			}
		}
	}()
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
				serviceCtx = context.WithValue(serviceCtx, config.ContextKey("database"), database.Database{
					Client: databaseClient,
				})
			}

			logger.InfoContext(serviceCtx, "started service")

			for {
				select {
				case <-serviceCtx.Done():
					serviceCancel()
					logger.WarnContext(serviceCtx, shutDownMessage)
					return
				default:
					run.Plugin(serviceCtx, logger)
					if workerCtx.Err() != nil || toRunOnce == "true" {
						serviceCancel()
						break
					}
					time.Sleep(time.Duration(service.SleepInterval) * time.Second)
				}
			}
		}(service)
	}
	wg.Wait()
	logger.WarnContext(workerCtx, "anklet (and all services) shut down")
	os.Exit(0)
}
