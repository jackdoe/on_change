package main

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

func executeCommand(command string, files []string) {
	fmt.Printf("[%s] Executing: %s\n", strings.Join(files, ", "), command)
	
	// Use shell to execute the command to support pipes, redirects, etc.
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			fmt.Printf("[%s] Command exited with code %d\n", 
				strings.Join(files, ", "), exitErr.ExitCode())
		} else {
			fmt.Printf("[%s] Command error: %v\n", strings.Join(files, ", "), err)
		}
	}
	fmt.Println()
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: %s <file1> [file2 ...] -- <command>\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s main.c utils.c -- 'make'\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Example: %s *.go -- 'go build'\n", os.Args[0])
		os.Exit(1)
	}
	
	// Find the -- separator
	separatorIndex := -1
	for i, arg := range os.Args[1:] {
		if arg == "--" {
			separatorIndex = i + 1
			break
		}
	}
	
	if separatorIndex == -1 || separatorIndex == 1 || separatorIndex == len(os.Args)-1 {
		fmt.Fprintf(os.Stderr, "Error: Must specify files before -- and command after --\n")
		fmt.Fprintf(os.Stderr, "Usage: %s <file1> [file2 ...] -- <command>\n", os.Args[0])
		os.Exit(1)
	}
	
	files := os.Args[1:separatorIndex]
	command := strings.Join(os.Args[separatorIndex+1:], " ")
	
	// Expand globs and verify files exist
	var watchedFiles []string
	for _, pattern := range files {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error processing pattern '%s': %v\n", pattern, err)
			os.Exit(1)
		}
		if len(matches) == 0 {
			// Not a glob pattern, use as-is
			matches = []string{pattern}
		}
		for _, file := range matches {
			if _, err := os.Stat(file); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: Cannot stat file '%s': %v\n", file, err)
			} else {
				watchedFiles = append(watchedFiles, file)
			}
		}
	}
	
	if len(watchedFiles) == 0 {
		fmt.Fprintf(os.Stderr, "Error: No valid files to watch\n")
		os.Exit(1)
	}
	
	// Create watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}
	defer watcher.Close()
	
	// Add files to watcher
	for _, file := range watchedFiles {
		err = watcher.Add(file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error watching '%s': %v\n", file, err)
		}
	}
	
	fmt.Printf("Watching %d file(s): %s\n", len(watchedFiles), strings.Join(watchedFiles, ", "))
	fmt.Printf("Will execute: %s\n", command)
	fmt.Println("Press Ctrl+C to stop.\n")
	
	// Initial execution
	executeCommand(command, watchedFiles)
	
	// Debouncing: collect events for a short period before executing
	var mu sync.Mutex
	var timer *time.Timer
	lastExec := time.Now()
	
	// Handle Ctrl+C
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			
			// Filter out some events we don't care about
			if event.Op&fsnotify.Chmod == fsnotify.Chmod {
				continue // Skip permission-only changes
			}
			
			mu.Lock()
			if timer != nil {
				timer.Stop()
			}
			
			// Debounce: wait 100ms for more changes before executing
			timer = time.AfterFunc(100*time.Millisecond, func() {
				mu.Lock()
				defer mu.Unlock()
				
				// Prevent executing too frequently (min 500ms between executions)
				if time.Since(lastExec) < 500*time.Millisecond {
					return
				}
				
				now := time.Now()
				fmt.Printf("[%s] Change detected at %s\n", 
					filepath.Base(event.Name), now.Format("15:04:05"))
				
				executeCommand(command, watchedFiles)
				lastExec = now
				
				// Re-add file if it was removed and recreated
				if event.Op&fsnotify.Remove == fsnotify.Remove {
					// Try to re-add after a short delay
					go func() {
						time.Sleep(100 * time.Millisecond)
						if _, err := os.Stat(event.Name); err == nil {
							watcher.Add(event.Name)
						}
					}()
				}
			})
			mu.Unlock()
			
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			fmt.Printf("Error: %v\n", err)
			
		case <-sigChan:
			fmt.Println("\nStopping file watcher...")
			return
		}
	}
}
