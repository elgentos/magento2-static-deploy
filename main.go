package main

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	flag "github.com/spf13/pflag"
)

// DeployJob represents a single deployment job (locale/theme/area combo)
type DeployJob struct {
	Locale string
	Theme  string
	Area   string
}

// DeployResult tracks the result of a deployment job
type DeployResult struct {
	Job        DeployJob
	FilesCount int64
	Duration   time.Duration
	Error      string
}

// ModuleConfig represents a Magento module.xml structure
type ModuleConfig struct {
	XMLName xml.Name `xml:"config"`
	Module  struct {
		Name string `xml:"name,attr"`
	} `xml:"module"`
}

// ThemeConfig represents a Magento theme.xml structure
type ThemeConfig struct {
	XMLName xml.Name `xml:"theme"`
	Parent  string   `xml:"parent"`
}

// CLI flags (Magento-compatible)
var (
	magentoRoot      string
	areasFlag        []string
	themesFlag       []string
	languagesFlag    []string
	jobsFlag         int
	strategyFlag     string
	forceFlag        bool
	verboseFlag      bool
	contentVersion   string
	noLumaDispatch   bool
	phpBinary        string
)

func init() {
	// Magento-compatible flags
	flag.StringVarP(&magentoRoot, "root", "r", ".", "Path to Magento root directory")
	flag.StringArrayVarP(&areasFlag, "area", "a", []string{}, "Generate files only for the specified areas (can be repeated)")
	flag.StringArrayVarP(&themesFlag, "theme", "t", []string{}, "Generate static view files for only the specified themes (can be repeated)")
	flag.StringArrayVarP(&languagesFlag, "language", "l", []string{}, "Generate files only for the specified languages (can be repeated)")
	flag.IntVarP(&jobsFlag, "jobs", "j", 0, "Enable parallel processing using the specified number of jobs (0 = auto-detect)")
	flag.StringVarP(&strategyFlag, "strategy", "s", "quick", "Deploy files using specified strategy")
	flag.BoolVarP(&forceFlag, "force", "f", false, "Deploy files in any mode")
	flag.BoolVarP(&verboseFlag, "verbose", "v", false, "Verbose output")
	flag.StringVar(&contentVersion, "content-version", "", "Custom version of static content")
	flag.BoolVar(&noLumaDispatch, "no-luma-dispatch", false, "Disable automatic dispatch of Luma themes to bin/magento")
	flag.StringVar(&phpBinary, "php", "php", "Path to PHP binary for Luma theme dispatch")

	// Custom usage message
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s [options] [languages...]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Deploys static view files (Magento-compatible CLI)\n\n")
		fmt.Fprintf(os.Stderr, "Arguments:\n")
		fmt.Fprintf(os.Stderr, "  languages    Space-separated list of ISO-639 language codes\n\n")
		fmt.Fprintf(os.Stderr, "Options:\n")
		flag.PrintDefaults()
		fmt.Fprintf(os.Stderr, "\nExamples:\n")
		fmt.Fprintf(os.Stderr, "  %s nl_NL en_US\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -f --area=frontend --theme=Vendor/Hyva nl_NL\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "  %s -f -a frontend -a adminhtml -t Vendor/Hyva -j 4 nl_NL en_US\n", os.Args[0])
	}
}

func main() {
	flag.Parse()

	// Collect languages from positional arguments and --language flags
	languages := collectLanguages()
	if len(languages) == 0 {
		languages = []string{"en_US"} // Default
	}

	// Collect areas (default to frontend if not specified)
	areas := areasFlag
	if len(areas) == 0 {
		areas = []string{"frontend"}
	}

	// Collect themes (default if not specified)
	themes := themesFlag
	if len(themes) == 0 {
		themes = []string{"Vendor/Hyva"}
	}

	numJobs := jobsFlag
	if numJobs <= 0 {
		numJobs = runtime.NumCPU()
	}

	if verboseFlag {
		fmt.Printf("Magento Static Content Deployer (Go)\n")
		fmt.Printf("Root: %s\n", magentoRoot)
		fmt.Printf("Languages: %v\n", languages)
		fmt.Printf("Themes: %v\n", themes)
		fmt.Printf("Areas: %v\n", areas)
		fmt.Printf("Parallel Jobs: %d\n", numJobs)
		fmt.Printf("Strategy: %s\n\n", strategyFlag)
	}

	// Classify themes into Hyvä and Luma
	var hyvaThemes, lumaThemes []string
	if noLumaDispatch {
		// Treat all themes as Hyvä (user explicitly disabled Luma dispatch)
		hyvaThemes = themes
		if verboseFlag {
			fmt.Println("Luma dispatch disabled - treating all themes as Hyvä")
		}
	} else {
		hyvaThemes, lumaThemes = classifyThemes(magentoRoot, themes, areas, verboseFlag)
	}

	hasErrors := false
	start := time.Now()

	// Deploy Hyvä themes using Go binary
	if len(hyvaThemes) > 0 {
		if verboseFlag && len(lumaThemes) > 0 {
			fmt.Println("\nDeploying Hyvä themes using Go binary...")
		}
		results := deployStatic(
			magentoRoot,
			languages,
			hyvaThemes,
			areas,
			numJobs,
			verboseFlag,
			contentVersion,
		)

		printResults(results, time.Since(start))

		// Check for actual errors (not skipped themes)
		for _, result := range results {
			if result.Error != "" && !strings.Contains(result.Error, "theme not found") {
				hasErrors = true
				break
			}
		}
	}

	// Deploy Luma themes using bin/magento
	if len(lumaThemes) > 0 {
		err := deployLumaThemes(magentoRoot, lumaThemes, areas, languages, numJobs, forceFlag, verboseFlag, contentVersion)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error deploying Luma themes: %v\n", err)
			hasErrors = true
		}
	}

	if hasErrors {
		os.Exit(1)
	}
}

// collectLanguages gathers languages from both positional args and --language flags
func collectLanguages() []string {
	var languages []string

	// Add languages from --language/-l flags
	languages = append(languages, languagesFlag...)

	// Add positional arguments (space-separated languages like Magento)
	languages = append(languages, flag.Args()...)

	// Remove duplicates while preserving order
	seen := make(map[string]bool)
	unique := []string{}
	for _, lang := range languages {
		if !seen[lang] {
			seen[lang] = true
			unique = append(unique, lang)
		}
	}

	return unique
}

// deployStatic orchestrates the parallel deployment
func deployStatic(magentoRoot string, locales, themes, areas []string, numJobs int, verbose bool, contentVersion string) []DeployResult {
	// Use provided content version or generate one based on current timestamp
	version := contentVersion
	if version == "" {
		version = fmt.Sprintf("%d", time.Now().Unix())
	}

	// Create deployment jobs
	jobs := createDeployJobs(locales, themes, areas)

	if verbose {
		fmt.Printf("Created %d deployment jobs\n", len(jobs))
		fmt.Printf("Deployment version: %s\n\n", version)
	}

	// Process jobs in parallel
	results := processJobs(magentoRoot, jobs, numJobs, verbose, version)

	// Ensure both minified and unminified versions of CSS/JS files exist
	ensureMinifiedCounterparts(magentoRoot, results, verbose)

	// Compile LESS files (email CSS) after file copying is complete
	compileLessForResults(magentoRoot, results, verbose)

	// Create deployment version file if any files were deployed
	totalFiles := int64(0)
	for _, result := range results {
		totalFiles += result.FilesCount
	}
	if totalFiles > 0 {
		createDeploymentVersionFile(magentoRoot, version, verbose)
	}

	return results
}

// ensureMinifiedCounterparts walks each deployed directory and ensures that both
// minified (.min.js/.min.css) and unminified versions exist for every CSS/JS file.
// Magento's RequireJS resolver expects both to be present depending on store config.
func ensureMinifiedCounterparts(magentoRoot string, results []DeployResult, verbose bool) {
	if verbose {
		fmt.Printf("\nEnsuring minified/unminified counterparts...\n")
	}

	for _, result := range results {
		if result.Error != "" {
			continue
		}

		destDir := filepath.Join(magentoRoot, "pub/static", result.Job.Area, result.Job.Theme, result.Job.Locale)
		var created int64

		filepath.Walk(destDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}

			name := info.Name()

			switch {
			// .min.js exists but .js counterpart does not
			case strings.HasSuffix(name, ".min.js"):
				counterpart := strings.TrimSuffix(path, ".min.js") + ".js"
				if _, err := os.Stat(counterpart); os.IsNotExist(err) {
					if copyFile(path, counterpart) == nil {
						created++
					}
				}
			// .js exists but .min.js counterpart does not
			case strings.HasSuffix(name, ".js"):
				counterpart := strings.TrimSuffix(path, ".js") + ".min.js"
				if _, err := os.Stat(counterpart); os.IsNotExist(err) {
					if copyFile(path, counterpart) == nil {
						created++
					}
				}
			// .min.css exists but .css counterpart does not
			case strings.HasSuffix(name, ".min.css"):
				counterpart := strings.TrimSuffix(path, ".min.css") + ".css"
				if _, err := os.Stat(counterpart); os.IsNotExist(err) {
					if copyFile(path, counterpart) == nil {
						created++
					}
				}
			// .css exists but .min.css counterpart does not
			case strings.HasSuffix(name, ".css"):
				counterpart := strings.TrimSuffix(path, ".css") + ".min.css"
				if _, err := os.Stat(counterpart); os.IsNotExist(err) {
					if copyFile(path, counterpart) == nil {
						created++
					}
				}
			}

			return nil
		})

		if verbose && created > 0 {
			fmt.Printf("  %s/%s (%s): created %d counterpart files\n",
				result.Job.Theme, result.Job.Area, result.Job.Locale, created)
		}
	}

	if verbose {
		fmt.Println()
	}
}

// compileLessForResults compiles LESS files for all successful deployment results
func compileLessForResults(magentoRoot string, results []DeployResult, verbose bool) {
	if verbose {
		fmt.Printf("\nCompiling email CSS...\n")
	}

	for _, result := range results {
		if result.Error != "" {
			continue // Skip failed deployments
		}

		destDir := filepath.Join(magentoRoot, "pub/static", result.Job.Area, result.Job.Theme, result.Job.Locale)

		if verbose {
			fmt.Printf("  %s/%s (%s):\n", result.Job.Theme, result.Job.Area, result.Job.Locale)
		}

		// Use preprocessor to handle Magento's complex LESS structure
		preprocessor := NewLessPreprocessor(magentoRoot, verbose)
		if err := preprocessor.PreprocessAndCompile(destDir, result.Job.Area, result.Job.Theme, result.Job.Locale); err != nil {
			if verbose {
				fmt.Printf("    ✗ LESS preprocessing error: %v\n", err)
			}
		}
	}

	if verbose {
		fmt.Println()
	}
}

// createDeployJobs generates all combinations of locales/themes/areas to deploy
func createDeployJobs(locales, themes, areas []string) []DeployJob {
	var jobs []DeployJob

	for _, locale := range locales {
		for _, theme := range themes {
			for _, area := range areas {
				jobs = append(jobs, DeployJob{
					Locale: locale,
					Theme:  theme,
					Area:   area,
				})
			}
		}
	}

	return jobs
}

// themeExists checks if a theme can be found
func themeExists(magentoRoot string, area string, themeName string) bool {
	sourceDirs := []string{
		filepath.Join(magentoRoot, "app/design", area, themeName),
		filepath.Join(magentoRoot, getVendorThemePath(area, themeName)),
	}

	for _, dir := range sourceDirs {
		if _, err := os.Stat(dir); err == nil {
			return true
		}
	}

	// Fallback: try auto-discovery for non-standard vendor package names
	discoveredPath := discoverVendorThemePath(magentoRoot, area, themeName)
	return discoveredPath != ""
}

// getThemePath returns the physical path of a theme
func getThemePath(magentoRoot string, area string, themeName string) string {
	// Check app/design first
	appDesignPath := filepath.Join(magentoRoot, "app/design", area, themeName)
	if _, err := os.Stat(appDesignPath); err == nil {
		return appDesignPath
	}

	// Check vendor path using conventional naming
	vendorPath := filepath.Join(magentoRoot, getVendorThemePath(area, themeName))
	if _, err := os.Stat(vendorPath); err == nil {
		return vendorPath
	}

	// Fallback: auto-discover vendor theme path by scanning registration.php files
	// This handles non-standard Composer package naming conventions
	discoveredPath := discoverVendorThemePath(magentoRoot, area, themeName)
	if discoveredPath != "" {
		return discoveredPath
	}

	return ""
}

// getThemeParent reads theme.xml and returns the parent theme name
func getThemeParent(themePath string) string {
	themeXmlPath := filepath.Join(themePath, "theme.xml")
	data, err := os.ReadFile(themeXmlPath)
	if err != nil {
		return ""
	}

	var config ThemeConfig
	if err := xml.Unmarshal(data, &config); err != nil {
		return ""
	}

	return strings.TrimSpace(config.Parent)
}

// getThemeParentChain builds the complete parent theme chain for a theme
// Returns themes in order from the theme itself to its most distant ancestor
// e.g., for GHDE/default -> [GHDE/default, GHNL/default, Sudac/default, Hyva/reset]
// This order ensures child theme files are copied first and not overwritten by parent files
func getThemeParentChain(magentoRoot string, area string, themeName string) []string {
	var chain []string
	visited := make(map[string]bool)
	current := themeName

	for current != "" && !visited[current] {
		visited[current] = true
		chain = append(chain, current) // Append to get child-first order

		themePath := getThemePath(magentoRoot, area, current)
		if themePath == "" {
			break
		}

		current = getThemeParent(themePath)
	}

	return chain
}

// isHyvaTheme checks if a theme is Hyvä-based by checking its parent chain
func isHyvaTheme(magentoRoot string, area string, themeName string, visited map[string]bool) bool {
	// Prevent infinite loops
	if visited[themeName] {
		return false
	}
	visited[themeName] = true

	// Check if this theme is a known Hyvä theme
	hyvaThemes := []string{
		"Hyva/default",
		"Hyva/reset",
	}
	for _, hyva := range hyvaThemes {
		if themeName == hyva {
			return true
		}
	}

	// Check for Tailwind config file (strong indicator of Hyvä)
	themePath := getThemePath(magentoRoot, area, themeName)
	if themePath != "" {
		tailwindPaths := []string{
			filepath.Join(themePath, "web/tailwind/tailwind.config.js"),
			filepath.Join(themePath, "web/tailwind/tailwind.config.cjs"),
			filepath.Join(themePath, "web/tailwind/tailwind-source.css"),
		}
		for _, tailwindPath := range tailwindPaths {
			if _, err := os.Stat(tailwindPath); err == nil {
				return true
			}
		}

	}

	// Check parent theme
	if themePath != "" {
		parent := getThemeParent(themePath)
		if parent != "" {
			return isHyvaTheme(magentoRoot, area, parent, visited)
		}
	}

	return false
}

// classifyThemes separates themes into Hyvä and Luma categories
func classifyThemes(magentoRoot string, themes []string, areas []string, verbose bool) (hyvaThemes []string, lumaThemes []string) {
	// Check each theme against each area (a theme might be Hyvä in frontend but not exist in adminhtml)
	themeClassification := make(map[string]bool) // true = Hyvä, false = Luma

	for _, theme := range themes {
		isHyva := false
		for _, area := range areas {
			if themeExists(magentoRoot, area, theme) {
				visited := make(map[string]bool)
				if isHyvaTheme(magentoRoot, area, theme, visited) {
					isHyva = true
					break
				}
			}
		}
		themeClassification[theme] = isHyva
	}

	for theme, isHyva := range themeClassification {
		if isHyva {
			hyvaThemes = append(hyvaThemes, theme)
			if verbose {
				fmt.Printf("🎨 %s detected as Hyvä theme\n", theme)
			}
		} else {
			lumaThemes = append(lumaThemes, theme)
			if verbose {
				fmt.Printf("🎨 %s detected as Luma theme\n", theme)
			}
		}
	}

	return hyvaThemes, lumaThemes
}

// deployLumaThemes dispatches Luma theme deployment to bin/magento
func deployLumaThemes(magentoRoot string, themes []string, areas []string, languages []string, numJobs int, force bool, verbose bool, contentVersion string) error {
	if len(themes) == 0 {
		return nil
	}

	fmt.Println("\nDispatching Luma themes to bin/magento...")

	// Build the command arguments
	args := []string{filepath.Join(magentoRoot, "bin/magento"), "setup:static-content:deploy"}

	if force {
		args = append(args, "-f")
	}

	for _, area := range areas {
		args = append(args, "--area="+area)
	}

	for _, theme := range themes {
		args = append(args, "--theme="+theme)
	}

	if numJobs > 0 {
		args = append(args, fmt.Sprintf("--jobs=%d", numJobs))
	}

	if contentVersion != "" {
		args = append(args, "--content-version="+contentVersion)
	}

	// Add languages as positional arguments
	args = append(args, languages...)

	// Show the command being executed
	cmdStr := phpBinary + " " + strings.Join(args, " ")
	fmt.Printf("Executing: %s\n\n", cmdStr)

	// Execute the command
	cmd := exec.Command(phpBinary, args...)
	cmd.Dir = magentoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// deployTask wraps a job and result tracking
type deployTask struct {
	job       DeployJob
	resultIdx int
	results   []DeployResult
}

// worker processes deployment jobs
func worker(wg *sync.WaitGroup, jobChan <-chan *deployTask, magentoRoot string, verbose bool, version string) {
	defer wg.Done()

	for task := range jobChan {
		start := time.Now()
		fileCount, err := deployTheme(magentoRoot, task.job, version)

		result := DeployResult{
			Job:        task.job,
			FilesCount: fileCount,
			Duration:   time.Since(start),
		}

		if err != nil {
			// Check if it's a "not found" error - if so, mark as skipped instead of error
			if strings.Contains(err.Error(), "theme directory not found") {
				result.Error = "" // Don't treat as error
				if verbose {
					fmt.Printf("⊘ %s/%s (%s) - theme not found (skipped)\n", task.job.Theme, task.job.Area, task.job.Locale)
				}
			} else {
				result.Error = fmt.Sprintf("%s/%s (%s): %v", task.job.Theme, task.job.Area, task.job.Locale, err)
				if verbose {
					fmt.Printf("✗ %s/%s (%s) - %v\n", task.job.Theme, task.job.Area, task.job.Locale, err)
				}
			}
		} else {
			if verbose {
				fmt.Printf("✓ %s/%s (%s) - %d files - %.1fs\n", task.job.Theme, task.job.Area, task.job.Locale, fileCount, result.Duration.Seconds())
			}
		}

		task.results[task.resultIdx] = result
	}
}

// processJobs executes deployment jobs with parallelization
func processJobs(magentoRoot string, jobs []DeployJob, numJobs int, verbose bool, version string) []DeployResult {
	results := make([]DeployResult, len(jobs))
	jobChan := make(chan *deployTask, numJobs)
	var wg sync.WaitGroup

	// Start worker goroutines
	for i := 0; i < numJobs; i++ {
		wg.Add(1)
		go worker(&wg, jobChan, magentoRoot, verbose, version)
	}

	// Send jobs to channel
	go func() {
		for i := range jobs {
			jobChan <- &deployTask{
				job:       jobs[i],
				resultIdx: i,
				results:   results,
			}
		}
		close(jobChan)
	}()

	wg.Wait()
	return results
}

// deployTheme handles the actual deployment for a theme/locale/area
// For Hyva-based themes, this copies from:
// 1. Theme web directory: app/design/{area}/{vendor}/{theme}/web (including parent themes)
// 2. Library files: vendor/mage-os/magento2-base/lib/web/
// 3. Extension view files from multiple locations:
//    - vendor/*/view/{area}/web/
//    - vendor/*/src/view/{area}/web/
//    - vendor/*/view/base/web/
//    - vendor/*/src/view/base/web/
func deployTheme(magentoRoot string, job DeployJob, version string) (int64, error) {
	// Get the theme vendor/name
	parts := strings.Split(job.Theme, "/")
	if len(parts) != 2 {
		return 0, fmt.Errorf("invalid theme name: %s", job.Theme)
	}

	// Destination directory - deploy to pub/static/ (nginx handles versioning via URL rewriting)
	destDir := filepath.Join(magentoRoot, "pub/static", job.Area, job.Theme, job.Locale)

	// Create destination directory
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return 0, fmt.Errorf("failed to create destination directory: %w", err)
	}

	var fileCount int64

	// 1. Build parent theme chain and copy from all themes (child-first so child files take priority)
	// e.g., for GHDE/default: [GHDE/default, GHNL/default, Sudac/default, Hyva/reset]
	// Since copyDirectory skips existing files, child theme files won't be overwritten by parents
	themeChain := getThemeParentChain(magentoRoot, job.Area, job.Theme)

	for _, chainTheme := range themeChain {
		chainParts := strings.Split(chainTheme, "/")
		if len(chainParts) != 2 {
			continue
		}
		chainVendor := chainParts[0]
		chainName := chainParts[1]

		// Try app/design path first
		themeWebDir := filepath.Join(magentoRoot, "app/design", job.Area, chainVendor, chainName, "web")
		if _, err := os.Stat(themeWebDir); err == nil {
			count, err := copyDirectory(themeWebDir, destDir)
			if err != nil {
				// Log but continue with other themes in chain
				continue
			}
			fileCount += count
		}

		// Also try vendor path for themes installed via composer
		vendorThemePath := getThemePath(magentoRoot, job.Area, chainTheme)
		if vendorThemePath != "" {
			vendorWebDir := filepath.Join(vendorThemePath, "web")
			if _, err := os.Stat(vendorWebDir); err == nil {
				count, err := copyDirectory(vendorWebDir, destDir)
				if err != nil {
					continue
				}
				fileCount += count
			}
		}

		// 1b. Copy theme module overrides ({themePath}/{ModuleName}/web/)
		// These override module web assets in the theme
		// Check both app/design and vendor theme paths
		themeBaseDirs := []string{
			filepath.Join(magentoRoot, "app/design", job.Area, chainVendor, chainName),
		}
		if vendorThemePath != "" {
			themeBaseDirs = append(themeBaseDirs, vendorThemePath)
		}
		for _, themeBaseDir := range themeBaseDirs {
			if themeEntries, err := os.ReadDir(themeBaseDir); err == nil {
				for _, entry := range themeEntries {
					// Skip non-directories and the "web" directory itself
					if !entry.IsDir() || entry.Name() == "web" {
						continue
					}
					// Check if this is a module override (contains a web directory)
					moduleWebDir := filepath.Join(themeBaseDir, entry.Name(), "web")
					if _, err := os.Stat(moduleWebDir); err == nil {
						// This is a module override - deploy to ModuleName/ prefix
						moduleName := entry.Name()
						count, err := copyDirectoryWithModulePrefix(moduleWebDir, destDir, moduleName)
						if err != nil {
							continue
						}
						fileCount += count
					}
				}
			}
		}
	}

	// 2. Copy lib files from multiple possible locations
	// Priority: Magento root lib/web first, then vendor/mage-os/magento2-base/lib/web
	libDirs := []string{
		filepath.Join(magentoRoot, "lib/web"),
		filepath.Join(magentoRoot, "vendor/mage-os/magento2-base/lib/web"),
	}
	for _, libDir := range libDirs {
		if _, err := os.Stat(libDir); err == nil {
			count, err := copyDirectory(libDir, destDir)
			if err != nil {
				return 0, fmt.Errorf("failed to copy library files from %s: %w", libDir, err)
			}
			fileCount += count
		}
	}

	// 3. Copy extension view files from all vendors (vendor/*/view/{area}/web/)
	vendorDir := filepath.Join(magentoRoot, "vendor")
	vendorEntries, err := os.ReadDir(vendorDir)
	if err == nil {
		for _, vendorEntry := range vendorEntries {
			if !vendorEntry.IsDir() {
				continue
			}
			vendorName := vendorEntry.Name()

			// Read each package in the vendor
			vendorPath := filepath.Join(vendorDir, vendorName)
			packageEntries, err := os.ReadDir(vendorPath)
			if err != nil {
				continue
			}

			for _, packageEntry := range packageEntries {
				if !packageEntry.IsDir() {
					continue
				}
				packageName := packageEntry.Name()
				packagePath := filepath.Join(vendorPath, packageName)

				// Get module name for this package
				moduleName := getModuleName(packagePath)

				// Check for view/{area}/web/ directory
				extensionWebDir := filepath.Join(packagePath, "view", job.Area, "web")
				if _, err := os.Stat(extensionWebDir); err == nil {
					count, err := copyDirectoryWithModulePrefix(extensionWebDir, destDir, moduleName)
					if err != nil {
						// Log but don't fail on extension file errors
						continue
					}
					fileCount += count
				}

				// Also check src/view/{area}/web/ (for some packages)
				extensionWebDirSrc := filepath.Join(packagePath, "src", "view", job.Area, "web")
				if _, err := os.Stat(extensionWebDirSrc); err == nil {
					count, err := copyDirectoryWithModulePrefix(extensionWebDirSrc, destDir, moduleName)
					if err != nil {
						continue
					}
					fileCount += count
				}

				// Also check view/base/web/ (for shared vendor modules like hyva-themes)
				extensionBaseDir := filepath.Join(packagePath, "view", "base", "web")
				if _, err := os.Stat(extensionBaseDir); err == nil {
					count, err := copyDirectoryWithModulePrefix(extensionBaseDir, destDir, moduleName)
					if err != nil {
						continue
					}
					fileCount += count
				}

				// Also check src/view/base/web/ (for some packages)
				extensionBaseDirSrc := filepath.Join(packagePath, "src", "view", "base", "web")
				if _, err := os.Stat(extensionBaseDirSrc); err == nil {
					count, err := copyDirectoryWithModulePrefix(extensionBaseDirSrc, destDir, moduleName)
					if err != nil {
						continue
					}
					fileCount += count
				}

				// Check for src/*/view/{area}/web/ (for multi-module packages like elasticsuite, hyva-themes/commerce-module-cms)
				srcModulesPath := filepath.Join(packagePath, "src")
				if srcModuleEntries, err := os.ReadDir(srcModulesPath); err == nil {
					for _, srcModuleEntry := range srcModuleEntries {
						if !srcModuleEntry.IsDir() {
							continue
						}
						moduleDir := filepath.Join(srcModulesPath, srcModuleEntry.Name())

						// Only process if it has an etc/module.xml (it's a Magento module)
						subModuleName := getModuleName(moduleDir)
						if subModuleName == "" {
							continue
						}

						moduleWebDir := filepath.Join(moduleDir, "view", job.Area, "web")
						if _, err := os.Stat(moduleWebDir); err == nil {
							count, err := copyDirectoryWithModulePrefix(moduleWebDir, destDir, subModuleName)
							if err != nil {
								continue
							}
							fileCount += count
						}

						// Also check view/base/web/
						moduleBaseDir := filepath.Join(moduleDir, "view", "base", "web")
						if _, err := os.Stat(moduleBaseDir); err == nil {
							count, err := copyDirectoryWithModulePrefix(moduleBaseDir, destDir, subModuleName)
							if err != nil {
								continue
							}
							fileCount += count
						}
					}
				}
			}
		}
	}

	// 4. Copy extension view files from app/code modules (app/code/{Vendor}/{Module}/view/{area}/web/)
	appCodeDir := filepath.Join(magentoRoot, "app", "code")
	appCodeVendors, err := os.ReadDir(appCodeDir)
	if err == nil {
		for _, appVendorEntry := range appCodeVendors {
			if !appVendorEntry.IsDir() {
				continue
			}

			appVendorPath := filepath.Join(appCodeDir, appVendorEntry.Name())
			moduleEntries, err := os.ReadDir(appVendorPath)
			if err != nil {
				continue
			}

			for _, moduleEntry := range moduleEntries {
				if !moduleEntry.IsDir() {
					continue
				}
				modulePath := filepath.Join(appVendorPath, moduleEntry.Name())

				moduleName := getModuleName(modulePath)

				// Check for view/{area}/web/
				moduleWebDir := filepath.Join(modulePath, "view", job.Area, "web")
				if _, err := os.Stat(moduleWebDir); err == nil {
					count, err := copyDirectoryWithModulePrefix(moduleWebDir, destDir, moduleName)
					if err != nil {
						continue
					}
					fileCount += count
				}

				// Check for view/base/web/
				moduleBaseDir := filepath.Join(modulePath, "view", "base", "web")
				if _, err := os.Stat(moduleBaseDir); err == nil {
					count, err := copyDirectoryWithModulePrefix(moduleBaseDir, destDir, moduleName)
					if err != nil {
						continue
					}
					fileCount += count
				}
			}
		}
	}

	if fileCount == 0 {
		return 0, fmt.Errorf("theme directory not found for %s/%s", job.Area, job.Theme)
	}

	return fileCount, nil
}

// copyDirectoryWithModulePrefix copies files with an optional module name prefix in the path
func copyDirectoryWithModulePrefix(src, dst string, modulePrefix string) (int64, error) {
	var fileCount int64

	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Calculate relative path
		relPath, _ := filepath.Rel(src, path)

		// Skip exclusions
		if shouldSkipFile(relPath) {
			return nil
		}

		// Add module prefix to destination path if provided
		if modulePrefix != "" {
			destPath := filepath.Join(dst, modulePrefix, relPath)
			// Create destination subdirectory
			os.MkdirAll(filepath.Dir(destPath), 0755)
			// Skip if destination exists
			if _, err := os.Stat(destPath); err == nil {
				return nil
			}
			// Copy file
			if err := copyFile(path, destPath); err != nil {
				return err
			}
		} else {
			destPath := filepath.Join(dst, relPath)
			// Create destination subdirectory
			os.MkdirAll(filepath.Dir(destPath), 0755)
			// Skip if destination exists
			if _, err := os.Stat(destPath); err == nil {
				return nil
			}
			// Copy file
			if err := copyFile(path, destPath); err != nil {
				return err
			}
		}

		atomic.AddInt64(&fileCount, 1)
		return nil
	})

	return fileCount, err
}

// copyDirectory recursively copies files from src to dst
func copyDirectory(src, dst string) (int64, error) {
	return copyDirectoryWithModulePrefix(src, dst, "")
}

// copyDirectoryWithModulePrefixOld recursively copies files from src to dst (old version kept for reference)
func copyDirectoryOld(src, dst string) (int64, error) {
	var fileCount int64

	err := filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		// Calculate relative path
		relPath, _ := filepath.Rel(src, path)
		destPath := filepath.Join(dst, relPath)

		// Create destination subdirectory
		os.MkdirAll(filepath.Dir(destPath), 0755)

		// Skip if destination exists (don't overwrite theme files with lib files)
		if _, err := os.Stat(destPath); err == nil {
			return nil
		}

		// Copy file
		if err := copyFile(path, destPath); err != nil {
			return err
		}

		atomic.AddInt64(&fileCount, 1)
		return nil
	})

	return fileCount, err
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return err
	}
	defer source.Close()

	destination, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destination.Close()

	_, err = io.Copy(destination, source)
	return err
}

// printResults prints deployment results summary
func printResults(results []DeployResult, totalDuration time.Duration) {
	fmt.Printf("\n%s\n", "─────────────────────────────────────────────────────────")
	fmt.Printf("Deployment Results\n")
	fmt.Printf("%s\n", "─────────────────────────────────────────────────────────")

	successCount := 0
	totalFiles := int64(0)

	for _, result := range results {
		if result.Error == "" {
			successCount++
			totalFiles += result.FilesCount
			fmt.Printf("✓ %s/%s (%s): %d files in %.1fs\n",
				result.Job.Theme, result.Job.Area, result.Job.Locale, result.FilesCount, result.Duration.Seconds())
		} else {
			fmt.Printf("✗ %s\n", result.Error)
		}
	}

	fmt.Printf("%s\n", "─────────────────────────────────────────────────────────")
	fmt.Printf("Total: %d/%d successful | %d files | %.1fs total\n",
		successCount, len(results), totalFiles, totalDuration.Seconds())
	if totalDuration.Seconds() > 0 {
		fmt.Printf("Average: %.1f files/sec\n", float64(totalFiles)/totalDuration.Seconds())
	}
}

// createDeploymentVersionFile creates the required Magento deployment version file
func createDeploymentVersionFile(magentoRoot string, version string, verbose bool) error {
	versionFile := filepath.Join(magentoRoot, "pub/static/deployed_version.txt")

	// Create the file with the version
	err := os.WriteFile(versionFile, []byte(version), 0644)
	if err != nil {
		return fmt.Errorf("failed to create deployment version file: %w", err)
	}

	if verbose {
		fmt.Printf("✓ Created deployment version file: %s\n", version)
	}

	return nil
}

// vendorThemeCache caches discovered vendor theme paths to avoid repeated filesystem scans
var vendorThemeCache = make(map[string]string)
var vendorThemeCacheMutex sync.RWMutex

// discoverVendorThemePath scans vendor/ directory for theme registration.php files
// to dynamically find theme paths regardless of Composer package naming conventions.
// Returns the discovered path or empty string if not found.
func discoverVendorThemePath(magentoRoot string, area string, themeName string) string {
	cacheKey := area + "/" + themeName

	// Check cache first
	vendorThemeCacheMutex.RLock()
	if cachedPath, found := vendorThemeCache[cacheKey]; found {
		vendorThemeCacheMutex.RUnlock()
		return cachedPath
	}
	vendorThemeCacheMutex.RUnlock()

	vendorDir := filepath.Join(magentoRoot, "vendor")
	vendorEntries, err := os.ReadDir(vendorDir)
	if err != nil {
		return ""
	}

	// The registration pattern we're looking for:
	// ComponentRegistrar::register(ComponentRegistrar::THEME, 'frontend/Vendor/default', __DIR__);
	// or with double quotes
	searchPattern := area + "/" + themeName

	for _, vendorEntry := range vendorEntries {
		if !vendorEntry.IsDir() {
			continue
		}
		vendorName := vendorEntry.Name()
		vendorPath := filepath.Join(vendorDir, vendorName)

		packageEntries, err := os.ReadDir(vendorPath)
		if err != nil {
			continue
		}

		for _, packageEntry := range packageEntries {
			if !packageEntry.IsDir() {
				continue
			}
			packagePath := filepath.Join(vendorPath, packageEntry.Name())

			// Check for registration.php
			registrationPath := filepath.Join(packagePath, "registration.php")
			data, err := os.ReadFile(registrationPath)
			if err != nil {
				continue
			}

			content := string(data)

			// Check if this is a theme registration for our target theme
			// Look for both single and double quote patterns
			if strings.Contains(content, "ComponentRegistrar::THEME") &&
				(strings.Contains(content, "'"+searchPattern+"'") ||
					strings.Contains(content, "\""+searchPattern+"\"")) {
				// Found it! Cache and return the path
				vendorThemeCacheMutex.Lock()
				vendorThemeCache[cacheKey] = packagePath
				vendorThemeCacheMutex.Unlock()
				if verboseFlag {
					fmt.Printf("  Auto-discovered vendor theme: %s -> %s\n", themeName, packagePath)
				}
				return packagePath
			}
		}
	}

	// Cache negative result too to avoid repeated scans
	vendorThemeCacheMutex.Lock()
	vendorThemeCache[cacheKey] = ""
	vendorThemeCacheMutex.Unlock()
	return ""
}

// getVendorThemePath converts a theme name to its vendor package path
// e.g., "Magento/backend" with adminhtml -> "vendor/magento/theme-adminhtml-backend"
// e.g., "Hyva/reset" with frontend -> "vendor/hyva-themes/magento2-hyva-reset"
// e.g., "MageOS/m137-admin-theme" with adminhtml -> "vendor/mage-os/theme-adminhtml-m137"
func getVendorThemePath(area string, themeName string) string {
	parts := strings.Split(themeName, "/")
	if len(parts) != 2 {
		return ""
	}

	vendor := strings.ToLower(parts[0])
	theme := strings.ToLower(parts[1])

	// Special case mappings for known vendor packages
	switch vendor {
	case "magento":
		if area == "adminhtml" {
			return filepath.Join("vendor", vendor, "theme-"+area+"-"+theme)
		}
		return filepath.Join("vendor", vendor, "theme-frontend-"+theme)
	case "hyva":
		return filepath.Join("vendor", "hyva-themes", "magento2-hyva-"+theme)
	case "mage-os", "mageos":
		return filepath.Join("vendor", "mage-os", "theme-"+area+"-"+theme)
	default:
		// Generic fallback for custom vendors
		if area == "adminhtml" {
			return filepath.Join("vendor", vendor, "theme-adminhtml-"+theme)
		}
		return filepath.Join("vendor", vendor, "theme-frontend-"+theme)
	}
}

// getModuleName extracts the module name from a package's module.xml file
func getModuleName(packagePath string) string {
	moduleXmlPath := filepath.Join(packagePath, "etc", "module.xml")
	if _, err := os.Stat(moduleXmlPath); err != nil {
		// Try src/etc/module.xml
		moduleXmlPath = filepath.Join(packagePath, "src", "etc", "module.xml")
		if _, err := os.Stat(moduleXmlPath); err != nil {
			return ""
		}
	}

	data, err := os.ReadFile(moduleXmlPath)
	if err != nil {
		return ""
	}

	var cfg ModuleConfig
	if err := xml.Unmarshal(data, &cfg); err != nil {
		return ""
	}

	return cfg.Module.Name
}

// shouldSkipFile determines if a file should be excluded from deployment
func shouldSkipFile(relPath string) bool {
	// Normalize path separators for cross-platform compatibility
	normalizedPath := strings.ReplaceAll(relPath, "\\", "/")
	fileName := filepath.Base(relPath)

	// Exclude hidden files (files starting with .)
	if strings.HasPrefix(fileName, ".") {
		return true
	}

	// Exclude LESS source files (Magento compiles these, we don't)
	if strings.HasSuffix(fileName, ".less") {
		return true
	}

	// Exclude documentation directories
	if strings.Contains(normalizedPath, "/docs/") {
		return true
	}

	// Exclude tailwind source directory
	if strings.HasPrefix(normalizedPath, "tailwind/") {
		return true
	}

	// Exclude css/source directories (LESS source files)
	if strings.Contains(normalizedPath, "/css/source/") || strings.HasPrefix(normalizedPath, "css/source/") {
		return true
	}

	// Exclude node_modules directories
	if strings.Contains(normalizedPath, "/node_modules/") || strings.HasPrefix(normalizedPath, "node_modules/") {
		return true
	}

	// Exclude playwright/test directories
	if strings.Contains(normalizedPath, "/playwright/") || strings.HasPrefix(normalizedPath, "playwright/") {
		return true
	}

	// Exclude test-results directories
	if strings.Contains(normalizedPath, "/test-results/") || strings.HasPrefix(normalizedPath, "test-results/") {
		return true
	}

	return false
}

