package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileWatcher monitors for changes in theme source directories
type FileWatcher struct {
	root      string
	sourceDir string
	destDir   string
	tickChan  <-chan time.Time
	done      chan bool
	mu        sync.Mutex
	fileHashes map[string]string
}

// NewFileWatcher creates a new file watcher
func NewFileWatcher(root, sourceDir, destDir string, interval time.Duration) *FileWatcher {
	ticker := time.NewTicker(interval)
	return &FileWatcher{
		root:       root,
		sourceDir: sourceDir,
		destDir:   destDir,
		tickChan:  ticker.C,
		done:      make(chan bool),
		fileHashes: make(map[string]string),
	}
}

// Start begins watching for file changes
func (w *FileWatcher) Start() {
	go func() {
		// Initial hash of all files
		w.updateHashes()

		for {
			select {
			case <-w.tickChan:
				if w.hasChanges() {
					fmt.Println("Changes detected. Running deployment...")
					version := fmt.Sprintf("%d", time.Now().Unix())
				fileCount, err := deployTheme(w.root, DeployJob{
					Locale: "nl_NL",
					Theme:  "Vendor/Hyva",
					Area:   "frontend",
				}, version, false)
					if err != nil {
						fmt.Printf("Error during deployment: %v\n", err)
					} else {
						fmt.Printf("✓ Deployment complete: %d files deployed\n", fileCount)
					}
				}
			case <-w.done:
				return
			}
		}
	}()
}

// Stop stops the file watcher
func (w *FileWatcher) Stop() {
	w.done <- true
}

// updateHashes computes hashes of all files in the source directory
func (w *FileWatcher) updateHashes() error {
	newHashes := make(map[string]string)

	err := filepath.Walk(w.sourceDir, func(path string, fileInfo os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if fileInfo.IsDir() || strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}

		relPath, _ := filepath.Rel(w.sourceDir, path)
		newHashes[relPath] = fmt.Sprintf("%d:%d", fileInfo.ModTime().Unix(), fileInfo.Size())

		return nil
	})

	w.mu.Lock()
	w.fileHashes = newHashes
	w.mu.Unlock()

	return err
}

// hasChanges checks if any files have changed
func (w *FileWatcher) hasChanges() bool {
	currentHashes := make(map[string]string)

	filepath.Walk(w.sourceDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}

		relPath, _ := filepath.Rel(w.sourceDir, path)
		currentHashes[relPath] = fmt.Sprintf("%d:%d", info.ModTime().Unix(), info.Size())
		return nil
	})

	w.mu.Lock()
	defer w.mu.Unlock()

	// Check for new or modified files
	for path, currentHash := range currentHashes {
		if prevHash, exists := w.fileHashes[path]; !exists || prevHash != currentHash {
			w.fileHashes = currentHashes
			return true
		}
	}

	// Check for deleted files
	for path := range w.fileHashes {
		if _, exists := currentHashes[path]; !exists {
			w.fileHashes = currentHashes
			return true
		}
	}

	return false
}
