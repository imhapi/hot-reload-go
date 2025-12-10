package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"
)

var (
	buildCmd    string
	runCmd      string
	watchDirs   []string
	excludeDirs []string
	extensions  []string
	delay       time.Duration
	verbose     bool
	version     = "dev"
	buildTime   = "unknown"
)

var rootCmd = &cobra.Command{
	Use:   "hrhapi [flags]",
	Short: "A hot reload CLI tool for Go projects",
	Long:  `A hot reload CLI tool for Go projects`,
	Run:   runHotReload,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version number",
	Long:  `Print the version number and build time of hrhapi.`,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("hrhapi version %s\n", version)
		if buildTime != "unknown" {
			fmt.Printf("Build time: %s\n", buildTime)
		}
	},
}

func init() {
	rootCmd.Flags().StringVarP(&buildCmd, "build", "b", "go build", "Build command to use")
	rootCmd.Flags().StringVarP(&runCmd, "run", "r", "", "Run command (defaults to running the built binary)")
	rootCmd.Flags().StringSliceVarP(&watchDirs, "watch", "w", []string{"."}, "Directories to watch for changes")
	rootCmd.Flags().StringSliceVarP(&excludeDirs, "exclude", "e", []string{".git", "vendor", "node_modules"}, "Directories to exclude from watching")
	rootCmd.Flags().StringSliceVarP(&extensions, "ext", "x", []string{".go"}, "File extensions to watch")
	rootCmd.Flags().DurationVarP(&delay, "delay", "d", 500*time.Millisecond, "Delay before rebuilding after file change")
	rootCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")

	rootCmd.AddCommand(versionCmd)
}

type HotReloader struct {
	watcher     *fsnotify.Watcher
	buildCmd    string
	runCmd      string
	excludeDirs map[string]bool
	extensions  map[string]bool
	delay       time.Duration
	verbose     bool
	cmd         *exec.Cmd
	ctx         context.Context
	cancel      context.CancelFunc
	rebuildChan chan struct{}
}

func NewHotReloader(buildCmd, runCmd string, excludeDirs, extensions []string, delay time.Duration, verbose bool) (*HotReloader, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	excludeMap := make(map[string]bool)
	for _, dir := range excludeDirs {
		excludeMap[dir] = true
	}

	extMap := make(map[string]bool)
	for _, ext := range extensions {
		extMap[ext] = true
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &HotReloader{
		watcher:     watcher,
		buildCmd:    buildCmd,
		runCmd:      runCmd,
		excludeDirs: excludeMap,
		extensions:  extMap,
		delay:       delay,
		verbose:     verbose,
		ctx:         ctx,
		cancel:      cancel,
		rebuildChan: make(chan struct{}, 1),
	}, nil
}

func (hr *HotReloader) shouldWatch(path string) bool {
	for excludeDir := range hr.excludeDirs {
		if strings.Contains(path, excludeDir) {
			return false
		}
	}

	ext := filepath.Ext(path)
	return hr.extensions[ext]
}

func (hr *HotReloader) addWatchPath(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	if info.IsDir() {
		base := filepath.Base(path)
		if hr.excludeDirs[base] {
			return nil
		}

		if hr.verbose {
			log.Printf("Watching directory: %s", path)
		}
		return hr.watcher.Add(path)
	}

	return nil
}

func (hr *HotReloader) watchRecursive(root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}

		if info.IsDir() {
			base := filepath.Base(path)
			if hr.excludeDirs[base] {
				return filepath.SkipDir
			}
			return hr.addWatchPath(path)
		}

		return nil
	})
}

func (hr *HotReloader) build() error {
	parts := strings.Fields(hr.buildCmd)
	if len(parts) == 0 {
		return fmt.Errorf("invalid build command")
	}

	cmd := exec.CommandContext(hr.ctx, parts[0], parts[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if hr.verbose {
		log.Printf("Building: %s", hr.buildCmd)
	}

	return cmd.Run()
}

func (hr *HotReloader) run() error {
	hr.stop()

	var cmd *exec.Cmd
	if hr.runCmd != "" {
		parts := strings.Fields(hr.runCmd)
		if len(parts) == 0 {
			return fmt.Errorf("invalid run command")
		}
		cmd = exec.CommandContext(hr.ctx, parts[0], parts[1:]...)
	} else {
		wd, _ := os.Getwd()
		moduleName := filepath.Base(wd)
		binary := "./" + moduleName

		if _, err := os.Stat(binary); os.IsNotExist(err) {
			entries, _ := os.ReadDir(".")
			found := false
			for _, entry := range entries {
				if !entry.IsDir() {
					info, err := entry.Info()
					if err != nil {
						continue
					}
					if info.Mode()&0111 != 0 || strings.HasSuffix(entry.Name(), ".exe") {
						binary = "./" + entry.Name()
						found = true
						break
					}
				}
			}
			if !found {
				return fmt.Errorf("no executable found. Please specify --run flag or ensure build creates an executable")
			}
		}
		cmd = exec.CommandContext(hr.ctx, binary)
	}

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	if hr.verbose {
		log.Printf("Running: %s", strings.Join(cmd.Args, " "))
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	hr.cmd = cmd

	go func() {
		if err := cmd.Wait(); err != nil {
			if hr.verbose {
				log.Printf("Process exited: %v", err)
			}
		}
	}()

	return nil
}

func (hr *HotReloader) stop() {
	if hr.cmd != nil && hr.cmd.Process != nil {
		if hr.verbose {
			log.Println("Stopping process...")
		}
		hr.cmd.Process.Signal(syscall.SIGTERM)
		time.Sleep(100 * time.Millisecond)
		if hr.cmd.ProcessState == nil || !hr.cmd.ProcessState.Exited() {
			hr.cmd.Process.Kill()
		}
		hr.cmd = nil
	}
}

func (hr *HotReloader) rebuild() {
	select {
	case hr.rebuildChan <- struct{}{}:
	default:

	}
}

func (hr *HotReloader) rebuildLoop() {
	ticker := time.NewTicker(hr.delay)
	defer ticker.Stop()

	var pending bool

	for {
		select {
		case <-hr.ctx.Done():
			return
		case <-hr.rebuildChan:
			pending = true
			ticker.Reset(hr.delay)
		case <-ticker.C:
			if pending {
				pending = false
				hr.performRebuild()
			}
		}
	}
}

func (hr *HotReloader) performRebuild() {
	log.Println(" File changed, rebuilding...")

	if err := hr.build(); err != nil {
		log.Printf(" Build failed: %v", err)
		return
	}

	log.Println(" Build successful, restarting...")

	if err := hr.run(); err != nil {
		log.Printf(" Run failed: %v", err)
	}
}

func (hr *HotReloader) Start() error {
	if err := hr.build(); err != nil {
		return fmt.Errorf("initial build failed: %w", err)
	}

	if err := hr.run(); err != nil {
		return fmt.Errorf("initial run failed: %w", err)
	}

	go hr.rebuildLoop()

	go func() {
		for {
			select {
			case <-hr.ctx.Done():
				return
			case event, ok := <-hr.watcher.Events:
				if !ok {
					return
				}

				if event.Op&fsnotify.Write == fsnotify.Write || event.Op&fsnotify.Create == fsnotify.Create {
					if hr.shouldWatch(event.Name) {
						if hr.verbose {
							log.Printf("File changed: %s", event.Name)
						}
						hr.rebuild()
					}
				}
			case err, ok := <-hr.watcher.Errors:
				if !ok {
					return
				}
				log.Printf("Watcher error: %v", err)
			}
		}
	}()

	return nil
}

func (hr *HotReloader) Stop() {
	hr.cancel()
	hr.stop()
	hr.watcher.Close()
}

func getModulePath() string {
	goModPath := "go.mod"
	if _, err := os.Stat(goModPath); err != nil {
		return ""
	}

	content, err := os.ReadFile(goModPath)
	if err != nil {
		return ""
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				return parts[1]
			}
		}
	}

	return ""
}

func findMainPackage() (string, string) {
	if entries, err := os.ReadDir("./cmd"); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				mainPath := fmt.Sprintf("./cmd/%s", entry.Name())
				mainFile := filepath.Join("cmd", entry.Name(), "main.go")
				if _, err := os.Stat(mainFile); err == nil {

					modulePath := getModulePath()
					var pkgPath string
					if modulePath != "" {
						pkgPath = fmt.Sprintf("%s/cmd/%s", modulePath, entry.Name())
					} else {
						pkgPath = mainPath
					}
					return mainPath, pkgPath
				}
			}
		}
	}

	if _, err := os.Stat("./main.go"); err == nil {
		return ".", "."
	}

	return "", ""
}

func runHotReload(cmd *cobra.Command, args []string) {
	if buildCmd == "go build" {
		relPath, pkgPath := findMainPackage()
		if relPath != "" && relPath != "." {
			binaryName := filepath.Base(relPath)
			buildCmd = fmt.Sprintf("go build -o %s %s", binaryName, relPath)
			if verbose {
				log.Printf("Auto-detected main package: %s (package: %s)", relPath, pkgPath)
			}
		}
	}

	if runCmd == "" {
		if strings.Contains(buildCmd, "-o") {
			parts := strings.Fields(buildCmd)
			for i, part := range parts {
				if part == "-o" && i+1 < len(parts) {
					runCmd = "./" + parts[i+1]
					break
				}
			}
		}

		if runCmd == "" {
			relPath, _ := findMainPackage()
			if relPath != "" && relPath != "." {
				binaryName := filepath.Base(relPath)
				runCmd = "./" + binaryName
			} else {
				wd, _ := os.Getwd()
				moduleName := filepath.Base(wd)
				runCmd = "./" + moduleName
			}
		}
	}

	hr, err := NewHotReloader(buildCmd, runCmd, excludeDirs, extensions, delay, verbose)
	if err != nil {
		log.Fatalf("Failed to create hot reloader: %v", err)
	}
	defer hr.Stop()

	for _, dir := range watchDirs {
		if err := hr.watchRecursive(dir); err != nil {
			log.Printf("Warning: failed to watch %s: %v", dir, err)
		}
	}

	if err := hr.Start(); err != nil {
		log.Fatalf("Failed to start hot reloader: %v", err)
	}

	log.Println(" Hot reload started. Press Ctrl+C to stop.")

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Println("\n Shutting down...")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}
