package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh/terminal"

	"github.com/fsnotify/fsnotify"
	"github.com/kitproj/kit/internal/proc"
	"github.com/kitproj/kit/internal/types"
	"github.com/kitproj/kit/internal/util"
	"sigs.k8s.io/yaml"
)

//go:generate sh -c "git describe --tags > tag"
//go:embed tag
var tag string

// GitHub Actions
var isCI = os.Getenv("CI") != "" || // Travis CI, CircleCI, GitLab CI, AppVeyor, CodeShip, dsari
	os.Getenv("BUILD_ID") != "" || // Jenkins, TeamCity
	os.Getenv("RUN_ID") != "" || // TaskCluster, Codefresh
	os.Getenv("GITHUB_ACTIONS") == "true"

const defaultConfigFile = "tasks.yaml"

type taskStatus struct {
	reason  string
	backoff backoff
}

func main() {
	help := false
	printVersion := false
	configFile := ""
	noWatch := os.Getenv("WATCH") == "0" || os.Getenv("KIT_WATCH") == "0"

	flag.BoolVar(&help, "h", false, "print help and exit")
	flag.BoolVar(&printVersion, "v", false, "print version and exit")
	flag.StringVar(&configFile, "f", defaultConfigFile, "config file")
	flag.BoolVar(&noWatch, "W", false, "do not watch for changes, defaults to KIT_WATCH=0 env var")
	flag.Parse()
	args := flag.Args()

	if help {
		flag.Usage()
		os.Exit(0)
	}

	if printVersion {
		fmt.Println(tag)
		os.Exit(0)
	}

	err := func() error {

		ctx, stopEverything := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
		defer stopEverything()

		pod := &types.Pod{}

		in, err := os.ReadFile(configFile)
		if err != nil {
			return err
		}
		if err = yaml.UnmarshalStrict(in, pod); err != nil {
			return err
		}

		// make sure that the file is valid,
		// this helps Copilot to auto-complete the file,
		// no need to have any IDE plugin - welcome to the future
		if pod.ApiVersion != "kit/v1" {
			return errors.New("invalid apiVersion, must be 'kit/v1")
		}
		if pod.Kind != "Tasks" {
			return errors.New("invalid kind, must be 'Tasks'")
		}
		if pod.Metadata.Name == "" {
			return errors.New("metadata.name is required")
		}

		log.SetFlags(0)

		// clear the screen
		fmt.Print("\x1b[2J")

		log.Printf("tag=%v\n", tag)
		log.Printf("isCI=%v\n", isCI)
		log.Printf("noWatch=%v\n", noWatch)

		tasks := pod.Spec.Tasks.NeededFor(args)

		statuses := sync.Map{}
		for _, task := range tasks {

			x := &taskStatus{
				reason:  "waiting",
				backoff: defaultBackoff,
			}

			statuses.Store(task.Name, x)
		}
		terminating := false
		printTasks := func() {
			defer handleCrash(stopEverything)

			_, height, _ := terminal.GetSize(0)
			if height == 0 {
				height = 24
			}

			// create a string builder
			buf := &strings.Builder{}

			// move home
			buf.WriteString("\x1b[H")

			// clear current line
			buf.WriteString("\x1b[2K")

			for _, t := range pod.Spec.Tasks {
				v, ok := statuses.Load(t.Name)
				if !ok {
					continue
				}
				status := v.(*taskStatus)
				reason := status.reason
				switch reason {
				case "waiting":
					buf.WriteString("\x1b[2m") // faint
				case "starting":
					buf.WriteString("\x1b[33m") // yellow
				case "running":
					buf.WriteString("\x1b[32m") // green
				case "success":
					buf.WriteString("\x1b[34m") // blueF
				case "error":
					buf.WriteString("\x1b[31m") // red
				}

				buf.WriteString(t.Name)
				buf.WriteString(" ")
				buf.WriteString(status.reason)
				buf.WriteString(" ")
				// faint
				buf.WriteString("\x1b[2m")
				// write the hosts
				buf.WriteString(fmt.Sprint(t.GetHostPorts()))
				// three spaces
				buf.WriteString("   ")
				// reset
				buf.WriteString("\x1b[0m")
			}

			// clear to the end of the line
			buf.WriteString("\x1b[K")

			// new line
			buf.WriteString("\n")

			// move to the bottom
			buf.WriteString(fmt.Sprintf("\x1b[%d;0H", height))

			// print the buffer
			fmt.Print(buf.String())
		}

		// every few milli-seconds print the current status to the terminal
		if !isCI {
			go func() {
				defer handleCrash(stopEverything)
				for {
					printTasks()
					time.Sleep(10 * time.Millisecond)
				}
			}()
		}

		stopRuns := &sync.Map{}
		work := make(chan types.Task)

		semaphores := util.NewSemaphores(pod.Spec.Semaphores)

		go func() {
			defer handleCrash(stopEverything)
			for _, t := range tasks.GetLeaves() {
				work <- t
			}
		}()

		go func() {
			defer handleCrash(stopEverything)
			<-ctx.Done()
			terminating = true
			close(work)
		}()

		wg := sync.WaitGroup{}

		stop := &sync.Map{}

		maybeStartDownstream := func(name string) {
			select {
			case <-ctx.Done():
			default:
				for _, downstream := range tasks.GetDownstream(name) {
					fulfilled := true
					for _, upstream := range downstream.Dependencies {
						v, ok := statuses.Load(upstream)
						if ok {
							status := v.(*taskStatus)
							fulfilled = fulfilled && (status.reason == "success" || status.reason == "running" && tasks.Get(upstream).IsBackground())
						} else {
							fulfilled = false
						}
					}
					if fulfilled {
						work <- downstream
					}
				}
			}
		}

		go func() {
			defer handleCrash(stopEverything)
			for {
				// stop everything if all tasks are complete/in error
				allComplete := tasks.All(func(task types.Task) bool {
					if v, ok := statuses.Load(task.Name); ok {
						status := v.(*taskStatus)
						return !task.IsBackground() && (status.reason == "success" || status.reason == "error")
					}
					return false
				})
				// non-restarting tasks in error
				anyError := tasks.Any(func(task types.Task) bool {
					if v, ok := statuses.Load(task.Name); ok {
						status := v.(*taskStatus)
						return task.GetRestartPolicy() == "Never" && status.reason == "error"
					}
					return false

				})
				if allComplete || anyError {
					stopEverything()
				}
				time.Sleep(time.Second)
			}
		}()

		for t := range work {
			name := t.Name

			v, _ := statuses.Load(t.Name)
			status := v.(*taskStatus)

			code := 0

			for _, x := range t.Name {
				code += int(x)
			}

			code = 30 + code%7

			log := log.New(os.Stdout, fmt.Sprintf("\033[0;%dm[%s] ", code, t.Name), 0)
			prc := proc.New(t, log, pod.Spec)

			processCtx, stopProcess := context.WithCancel(ctx)
			defer stopProcess()

			// watch files for changes
			if !noWatch {
				go func(t types.Task, stopProcess context.CancelFunc) {
					defer handleCrash(stopEverything)
					watcher, err := fsnotify.NewWatcher()
					if err != nil {
						panic(err)
					}
					defer watcher.Close()
					for _, w := range t.Watch {

						stat, err := os.Stat(w)
						if err != nil {
							panic(err)
						}
						if err := watcher.Add(w); err != nil {
							panic(err)
						}
						if stat.IsDir() {
							if err := filepath.WalkDir(w, func(path string, d fs.DirEntry, err error) error {
								if err != nil {
									return err
								}
								if d.IsDir() {
									return watcher.Add(path)
								}
								return nil
							}); err != nil {
								panic(err)
							}
						}
					}

					timer := time.AfterFunc(100*365*24*time.Hour, func() {
						work <- t
					})
					defer timer.Stop()

					for {
						select {
						case <-processCtx.Done():
							return
						case e := <-watcher.Events:
							// ignore chmod events, these can be triggered by the editor, but we don't care
							if e.Op != fsnotify.Chmod {
								log.Printf("%v changed\n", e)
								timer.Reset(time.Second)
							}
						case err := <-watcher.Errors:
							panic(err)
						}
					}
				}(t, stopProcess)
			}

			// run the process
			wg.Add(1)
			go func(t types.Task, status *taskStatus, stopProcess context.CancelFunc) {
				defer handleCrash(stopEverything)
				defer wg.Done()

				if f, ok := stop.Load(name); ok {
					log.Printf("stopping process\n")
					f.(context.CancelFunc)()
				}

				stop.Store(name, stopProcess)

				if m := t.Mutex; m != "" {
					mutex := util.GetMutex(m)
					log.Printf("waiting for mutex %q\n", m)
					mutex.Lock()
					log.Printf("locked mutex %q\n", m)
					defer mutex.Unlock()
				}

				if s := t.Semaphore; s != "" {
					log.Printf("waiting for semaphore %q\n", s)
					semaphore := semaphores.Get(s)
					if err := semaphore.Acquire(ctx, 1); err != nil {
						return
					}
					log.Printf("acquired semaphore %q\n", s)
					defer semaphore.Release(1)
				}

				go func() {
					defer handleCrash(stopEverything)
					<-ctx.Done()
					stopProcess()
				}()

				for {
					select {
					case <-processCtx.Done():
						return
					default:

						// if the task targets exist, we can skip the task
						if t.Skip() {
							log.Printf("skipping process\n")
							status.reason = "success"
							maybeStartDownstream(name)
							break
						}

						err := func() error {
							runCtx, stopRun := context.WithCancel(processCtx)
							defer stopRun()
							stopRuns.Store(t.Name, stopRun)
							status.reason = "starting"
							if err := prc.Reset(runCtx); err != nil {
								return err
							}
							for _, port := range t.GetHostPorts() {
								log.Printf("waiting for port %d to be free\n", port)
								if err := isPortFree(port); err != nil {
									return err
								}
							}
							if probe := t.GetLivenessProbe(); probe != nil {
								log.Printf("liveness probe=%v\n", probe)
								liveFunc := func(live bool, err error) {
									if !live {
										log.Printf("is dead, stopping\n")
										stopRun()
									}
								}
								go probeLoop(runCtx, stopEverything, *probe, liveFunc)
							}
							if probe := t.GetReadinessProbe(); probe != nil {
								status.reason = "starting"
								log.Printf("readiness probe=%v\n", probe)
								readyFunc := func(ready bool, err error) {
									if ready {
										log.Printf("is ready, starting downstream\n")
										status.reason = "running"
										maybeStartDownstream(name)
									} else {
										log.Printf("is not ready\n")
										status.reason = "error"
									}
								}
								go probeLoop(runCtx, stopEverything, *probe, readyFunc)
							} else {
								status.reason = "running"
							}

							// the log.Writer does not add the prefix, so we need to add it manually
							out := funcWriter(func(bytes []byte) (int, error) {
								log.Print(string(bytes))
								return len(bytes), nil
							})

							return prc.Run(runCtx, out, out)
						}()

						if err != nil {
							if errors.Is(err, context.Canceled) {
								return
							}
							status.reason = "error"
							log.Printf("task failed: %v\n", err)
							status.backoff = status.backoff.next()
						} else {
							status.reason = "success"
							status.backoff = defaultBackoff
							maybeStartDownstream(name)
							if !t.IsRestart() {
								return
							}
						}
						if t.GetRestartPolicy() == "Never" {
							return
						}
					}
					if !terminating {
						log.Printf("backing off %s\n", status.backoff.Duration)
						time.Sleep(status.backoff.Duration)
					}
				}
			}(t, status, stopProcess)
		}

		wg.Wait()

		for _, task := range tasks {
			if v, ok := statuses.Load(task.Name); ok && v.(*taskStatus).reason == "error" && task.GetRestartPolicy() == "Never" {
				return fmt.Errorf("%s errored", task.Name)
			}
		}
		return nil
	}()

	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "%s\n", err.Error())
		os.Exit(1)
	}
}

func handleCrash(stop context.CancelFunc) {
	if r := recover(); r != nil {
		fmt.Println(r)
		debug.PrintStack()
		stop()
	}
}
