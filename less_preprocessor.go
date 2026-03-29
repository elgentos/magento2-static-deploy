package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// LessPreprocessor handles Magento-style LESS preprocessing
type LessPreprocessor struct {
	magentoRoot string
	stagingDir  string
	verbose     bool
}

// NewLessPreprocessor creates a new preprocessor
func NewLessPreprocessor(magentoRoot string, verbose bool) *LessPreprocessor {
	return &LessPreprocessor{
		magentoRoot: magentoRoot,
		verbose:     verbose,
	}
}

// PreprocessAndCompile preprocesses LESS files and compiles them to CSS
func (lp *LessPreprocessor) PreprocessAndCompile(destDir, area, theme, locale string) error {
	// Create a temporary staging directory in the Magento root (accessible from Docker-based PHP)
	stagingDir := filepath.Join(lp.magentoRoot, ".less-staging-tmp")
	os.RemoveAll(stagingDir) // Clean up any previous staging directory
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return fmt.Errorf("failed to create staging directory: %w", err)
	}
	defer os.RemoveAll(stagingDir) // Clean up after compilation
	lp.stagingDir = stagingDir

	if lp.verbose {
		fmt.Printf("    Staging directory: %s\n", stagingDir)
	}

	// Stage all LESS source files
	if err := lp.stageSourceFiles(area, theme); err != nil {
		return fmt.Errorf("failed to stage source files: %w", err)
	}

	// Process @magento_import directives
	if err := lp.processMagentoImports(); err != nil {
		return fmt.Errorf("failed to process @magento_import: %w", err)
	}

	// Compile the email LESS files using lessc
	compiler, err := NewLessCompiler(lp.magentoRoot, lp.verbose)
	if err != nil {
		return fmt.Errorf("LESS compiler not available: %w", err)
	}

	if err := compiler.CompileEmailCSS(lp.stagingDir, destDir, area, theme, locale); err != nil {
		return fmt.Errorf("failed to compile email CSS: %w", err)
	}

	return nil
}

// stageSourceFiles copies all LESS source files to the staging directory
func (lp *LessPreprocessor) stageSourceFiles(area, theme string) error {
	themeParts := strings.Split(theme, "/")
	if len(themeParts) != 2 {
		return fmt.Errorf("invalid theme format: %s", theme)
	}
	themeVendor := themeParts[0]
	themeName := themeParts[1]

	// Source locations to copy from (in priority order - later overrides earlier)
	sources := []struct {
		path   string
		prefix string // subdirectory in staging
	}{
		// lib/web base
		{filepath.Join(lp.magentoRoot, "lib/web"), ""},
		{filepath.Join(lp.magentoRoot, "vendor/mage-os/magento2-base/lib/web"), ""},

		// Blank theme (base)
		{filepath.Join(lp.magentoRoot, "vendor/mage-os/theme-frontend-blank/web"), ""},

		// Luma theme
		{filepath.Join(lp.magentoRoot, "vendor/mage-os/theme-frontend-luma/web"), ""},

		// Hyva email module
		{filepath.Join(lp.magentoRoot, "vendor/hyva-themes/magento2-email-module/src/view", area, "web"), ""},

		// Theme's own web directory
		{filepath.Join(lp.magentoRoot, "app/design", area, themeVendor, themeName, "web"), ""},
	}

	for _, source := range sources {
		if _, err := os.Stat(source.path); os.IsNotExist(err) {
			continue
		}

		destPrefix := filepath.Join(lp.stagingDir, source.prefix)
		if err := lp.copyLessFiles(source.path, destPrefix); err != nil {
			if lp.verbose {
				fmt.Printf("    Warning: failed to copy from %s: %v\n", source.path, err)
			}
		}
	}

	// Stage module LESS files from vendor packages (e.g. Magento_Catalog/css/source/_email.less)
	// These are needed for @magento_import expansion in email.less and email-inline.less
	lp.stageModuleLessFiles(area)

	return nil
}

// stageModuleLessFiles stages LESS files from vendor and app/code modules into the
// staging directory under their module name prefix (e.g. Magento_Catalog/css/source/_email.less).
// This enables findModuleImports() to resolve @magento_import directives.
func (lp *LessPreprocessor) stageModuleLessFiles(area string) {
	// Stage from vendor packages
	vendorDir := filepath.Join(lp.magentoRoot, "vendor")
	vendorEntries, err := os.ReadDir(vendorDir)
	if err != nil {
		return
	}

	for _, vendorEntry := range vendorEntries {
		if !vendorEntry.IsDir() {
			continue
		}

		vendorPath := filepath.Join(vendorDir, vendorEntry.Name())
		packageEntries, err := os.ReadDir(vendorPath)
		if err != nil {
			continue
		}

		for _, packageEntry := range packageEntries {
			if !packageEntry.IsDir() {
				continue
			}
			packagePath := filepath.Join(vendorPath, packageEntry.Name())

			moduleName := getModuleName(packagePath)
			if moduleName != "" {
				lp.stageModuleWebLess(packagePath, moduleName, area)
			}

			// Check for multi-module packages (src/*/view/...)
			srcPath := filepath.Join(packagePath, "src")
			srcEntries, err := os.ReadDir(srcPath)
			if err != nil {
				continue
			}
			for _, srcEntry := range srcEntries {
				if !srcEntry.IsDir() {
					continue
				}
				subModulePath := filepath.Join(srcPath, srcEntry.Name())
				subModuleName := getModuleName(subModulePath)
				if subModuleName != "" {
					lp.stageModuleWebLess(subModulePath, subModuleName, area)
				}
			}
		}
	}

	// Stage from app/code modules
	appCodeDir := filepath.Join(lp.magentoRoot, "app", "code")
	appCodeVendors, err := os.ReadDir(appCodeDir)
	if err != nil {
		return
	}

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
			if moduleName != "" {
				lp.stageModuleWebLess(modulePath, moduleName, area)
			}
		}
	}
}

// stageModuleWebLess copies LESS files from a module's view/{area}/web and view/base/web
// directories into the staging directory under the module name prefix.
func (lp *LessPreprocessor) stageModuleWebLess(modulePath, moduleName, area string) {
	webDirs := []string{
		filepath.Join(modulePath, "view", area, "web"),
		filepath.Join(modulePath, "view", "base", "web"),
	}

	for _, webDir := range webDirs {
		if _, err := os.Stat(webDir); os.IsNotExist(err) {
			continue
		}
		destPrefix := filepath.Join(lp.stagingDir, moduleName)
		lp.copyLessFiles(webDir, destPrefix)
	}
}

// copyLessFiles recursively copies LESS and related files
func (lp *LessPreprocessor) copyLessFiles(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // Skip errors
		}

		if info.IsDir() {
			return nil
		}

		// Only copy LESS files and related assets
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".less" && ext != ".css" {
			return nil
		}

		relPath, _ := filepath.Rel(src, path)
		destPath := filepath.Join(dst, relPath)

		// Create destination directory
		os.MkdirAll(filepath.Dir(destPath), 0755)

		// Copy file (overwrite if exists - later sources take priority)
		return copyFileLess(path, destPath)
	})
}

// processMagentoImports expands @magento_import directives and fixes import references
func (lp *LessPreprocessor) processMagentoImports() error {
	// Find all LESS files in staging
	return filepath.Walk(lp.stagingDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		if !strings.HasSuffix(path, ".less") {
			return nil
		}

		// Read and process the file
		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		newContent := lp.expandMagentoImports(string(content), filepath.Dir(path))

		// Note: email.less uses @import (reference) to only output .email-non-inline() mixin content
		// email-inline.less doesn't use (reference) and outputs all inline styles

		if newContent != string(content) {
			os.WriteFile(path, []byte(newContent), 0644)
		}

		return nil
	})
}

// expandMagentoImports replaces @magento_import with actual imports
func (lp *LessPreprocessor) expandMagentoImports(content string, baseDir string) string {
	// Pattern: //@magento_import 'source/_module.less';
	// or: @magento_import (reference) 'source/_email.less';
	re := regexp.MustCompile(`(?m)^(?://)?@magento_import\s*(?:\(reference\))?\s*['"]([^'"]+)['"];?\s*$`)

	return re.ReplaceAllStringFunc(content, func(match string) string {
		// Extract the import pattern
		submatches := re.FindStringSubmatch(match)
		if len(submatches) < 2 {
			return match
		}

		pattern := submatches[1]
		isReference := strings.Contains(match, "(reference)")

		// Find all matching files from modules
		imports := lp.findModuleImports(pattern)

		if len(imports) == 0 {
			return "// @magento_import: no matches for " + pattern
		}

		// Generate import statements
		var result []string
		for _, imp := range imports {
			if isReference {
				result = append(result, fmt.Sprintf("@import (reference) '%s';", imp))
			} else {
				result = append(result, fmt.Sprintf("@import '%s';", imp))
			}
		}

		return strings.Join(result, "\n")
	})
}

// findModuleImports finds all module LESS files matching a pattern
func (lp *LessPreprocessor) findModuleImports(pattern string) []string {
	var imports []string

	// Pattern like 'source/_email.less' should find Vendor_Module/css/source/_email.less
	// in the staging directory

	// Search in module directories within staging
	moduleDirs, _ := filepath.Glob(filepath.Join(lp.stagingDir, "*_*"))
	for _, moduleDir := range moduleDirs {
		checkPath := filepath.Join(moduleDir, "css", pattern)
		if _, err := os.Stat(checkPath); err == nil {
			// Make path relative to staging dir's css folder
			relPath, _ := filepath.Rel(filepath.Join(lp.stagingDir, "css"), checkPath)
			imports = append(imports, relPath)
		}
	}

	return imports
}

// copyFileLess copies a single file
func copyFileLess(src, dst string) error {
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
