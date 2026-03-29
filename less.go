package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LessCompiler handles LESS to CSS compilation using PHP (wikimedia/less.php)
// This matches Magento's built-in LESS compilation behavior
type LessCompiler struct {
	magentoRoot string
	verbose     bool
	phpPath     string
}

// NewLessCompiler creates a new LESS compiler instance
func NewLessCompiler(magentoRoot string, verbose bool) (*LessCompiler, error) {
	// Find PHP in PATH
	phpPath, err := exec.LookPath("php")
	if err != nil {
		return nil, fmt.Errorf("php not found in PATH")
	}

	// Verify wikimedia/less.php is installed
	lessPhpPath := filepath.Join(magentoRoot, "vendor/wikimedia/less.php/lessc.inc.php")
	if _, err := os.Stat(lessPhpPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("wikimedia/less.php not found at %s", lessPhpPath)
	}

	return &LessCompiler{
		magentoRoot: magentoRoot,
		verbose:     verbose,
		phpPath:     phpPath,
	}, nil
}

// CompileEmailCSS compiles the email LESS files to CSS for a given theme/locale/area
func (lc *LessCompiler) CompileEmailCSS(stagingDir, destDir, area, theme, locale string) error {
	// Email LESS files to compile
	emailFiles := []string{
		"email.less",
		"email-inline.less",
		"email-fonts.less",
	}

	for _, lessFileName := range emailFiles {
		cssFileName := strings.TrimSuffix(lessFileName, ".less") + ".css"
		cssPath := filepath.Join(destDir, "css", cssFileName)

		// Ensure css directory exists
		os.MkdirAll(filepath.Join(destDir, "css"), 0755)

		// If the theme (or a parent) provides a pre-compiled CSS file, use it instead of
		// compiling from LESS. The staging dir applies correct priority (theme overrides base),
		// so a CSS file here means the theme intends to use its own pre-built version.
		stagedCssPath := filepath.Join(stagingDir, "css", cssFileName)
		if _, err := os.Stat(stagedCssPath); err == nil {
			if err := copyFileSimple(stagedCssPath, cssPath); err == nil {
				if lc.verbose {
					fmt.Printf("    ✓ Using pre-compiled css/%s from theme\n", cssFileName)
				}
				continue
			}
		}

		sourcePath := filepath.Join(stagingDir, "css", lessFileName)

		if _, err := os.Stat(sourcePath); os.IsNotExist(err) {
			if lc.verbose {
				fmt.Printf("    ⊘ %s not found\n", lessFileName)
			}
			continue
		}

		// Compile LESS to CSS using PHP
		if err := lc.compileLessFile(sourcePath, cssPath, stagingDir, area, theme, locale); err != nil {
			if lc.verbose {
				fmt.Printf("    ✗ Failed to compile %s: %v\n", lessFileName, err)
			}
			continue
		}

		if lc.verbose {
			fmt.Printf("    ✓ Compiled %s → css/%s\n", lessFileName, cssFileName)
		}
	}

	return nil
}

// compileLessFile compiles a single LESS file to CSS using PHP wikimedia/less.php
func (lc *LessCompiler) compileLessFile(sourcePath, destPath, stagingDir, area, theme, locale string) error {
	// Build include paths for @import resolution
	includePaths := []string{
		stagingDir,
		filepath.Join(stagingDir, "css"),
		filepath.Join(stagingDir, "css", "source"),
		filepath.Join(stagingDir, "css", "source", "lib"),
	}

	// Create a PHP script to compile the LESS file
	// This uses the same Less.php library that Magento uses
	phpScript := fmt.Sprintf(`<?php
error_reporting(E_ALL & ~E_DEPRECATED & ~E_USER_DEPRECATED);

require_once '%s/vendor/autoload.php';

$lessFile = '%s';
$cssFile = '%s';
$includePaths = %s;
$area = '%s';
$theme = '%s';
$locale = '%s';

try {
    $parser = new Less_Parser([
        'compress' => true,
        'relativeUrls' => false,
        'import_dirs' => array_fill_keys($includePaths, ''),
    ]);

    $parser->parseFile($lessFile, '');
    $css = $parser->getCss();

    // Fix the @import url for email-fonts.css to match Magento's format
    // Magento uses: {{base_url_path}}frontend/Theme/Name/locale/css/email-fonts.css
    $css = preg_replace(
        '#@import url\(["\']?([^"\'()]+email-fonts\.css)["\']?\)#',
        '@import url("{{base_url_path}}' . $area . '/' . $theme . '/{{locale}}/css/email-fonts.css")',
        $css
    );

    file_put_contents($cssFile, $css);
    echo "OK";
} catch (Exception $e) {
    fwrite(STDERR, "LESS compilation error: " . $e->getMessage() . "\n");
    exit(1);
}
`,
		lc.magentoRoot,
		sourcePath,
		destPath,
		phpArrayString(includePaths),
		area,
		theme,
		locale,
	)

	// Write the PHP script to the Magento root (accessible from Docker-based PHP)
	tmpFileName := filepath.Join(lc.magentoRoot, ".less-compile-tmp.php")

	if err := os.WriteFile(tmpFileName, []byte(phpScript), 0644); err != nil {
		return fmt.Errorf("failed to write PHP script to %s: %w", tmpFileName, err)
	}

	// Execute the PHP script from the magento root directory
	cmd := exec.Command(lc.phpPath, tmpFileName)
	cmd.Dir = lc.magentoRoot
	output, err := cmd.CombinedOutput()

	// Clean up temp file after execution
	os.Remove(tmpFileName)

	if err != nil {
		return fmt.Errorf("PHP compilation failed: %v\nOutput: %s", err, string(output))
	}

	// Verify output file was created and has content
	info, err := os.Stat(destPath)
	if err != nil {
		return fmt.Errorf("output file not created: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("output file is empty")
	}

	return nil
}

// copyFileSimple copies a single file from src to dst
func copyFileSimple(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

// phpArrayString converts a Go string slice to PHP array syntax
func phpArrayString(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("'%s'", item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
