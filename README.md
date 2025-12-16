# Magento Static Content Deployer (Go)

Experimental!! A high-performance static content deployment tool written in Go that significantly accelerates Magento 2 static asset deployment by leveraging true parallelization and efficient file I/O.

## Performance

On this project, deployment improved from **~115 seconds** (Magento native) to **~0.3-0.5 seconds** for the frontend theme deployment:

- **Vendor/Hyva/frontend**: 11,126 files deployed in 0.3 seconds
- **Throughput**: ~40,000 files/second
- **Speedup**: **230-380x faster** than PHP implementation

## Why It's Faster

1. **Native Parallelization**: Go's goroutines handle true concurrent I/O across multiple CPU cores
2. **Low Overhead**: No PHP bootstrap, no Magento dependency injection, no database access
3. **Efficient I/O**: Optimized file copying with buffered I/O and minimal memory allocation
4. **Simple Logic**: Deployment doesn't require PHP preprocessing (LESS compilation, etc. handled by NPM)

## Installation

### Build from Source

```bash
cd tools/static-deploy
go build -o static-deploy main.go watcher.go
```

### Requirements

- Go 1.21 or later

## Usage

### Basic Usage

Deploy Vendor/Hyva theme to frontend area:

```bash
./tools/static-deploy/static-deploy \
  -root . \
  -locales nl_NL \
  -themes Vendor/Hyva \
  -areas frontend
```

### With Verbose Output

```bash
./tools/static-deploy/static-deploy \
  -root . \
  -locales nl_NL,en_US \
  -themes Vendor/Hyva \
  -areas frontend \
  -jobs 8 \
  -v
```

### Note on Admin Themes

By default, only `frontend` area is deployed. This is because:
- Admin themes (Magento/backend, MageOS/m137-admin-theme) are part of Magento core
- They don't typically need custom deployment unless you have custom admin theme
- If deployment encounters a missing theme, it gracefully skips it

To deploy admin themes if they exist:
```bash
./static-deploy -areas frontend,adminhtml -v
```

### All Options

```
  -root string
        Path to Magento root directory (default ".")

  -locales string
        Comma-separated locales (default "nl_NL")
        Example: nl_NL,en_US,de_DE

  -themes string
        Comma-separated themes (default "Vendor/Hyva")
        Example: Vendor/Hyva,Magento/blank,Hyva/reset

  -areas string
        Comma-separated areas (default "frontend,adminhtml")
        Options: frontend, adminhtml

  -jobs int
        Number of parallel jobs (default 0 = auto-detect CPU count)
        Use -jobs 1 for sequential processing

  -strategy string
        Deployment strategy (default "quick")
        Note: Currently only copies files; strategy is informational

  -force
        Force deployment even if files exist (always copies)

  -v    Verbose output showing per-deployment progress
```

## Examples

### Deploy Single Locale/Theme

```bash
./static-deploy -root /var/www/magento -locales nl_NL -themes Vendor/Hyva -areas frontend
```

### Deploy Multiple Locales and Themes

```bash
./static-deploy \
  -locales nl_NL,en_US,de_DE \
  -themes Vendor/Hyva,Magento/blank \
  -areas frontend
```

### Sequential Processing (1 Job)

```bash
./static-deploy -jobs 1 -v
```

### Full Admin + Frontend Deployment

```bash
./static-deploy \
  -locales nl_NL \
  -themes Vendor/Hyva \
  -areas frontend,adminhtml
```

## What It Does

1. Creates combinations of (locale, theme, area)
2. For each combination:
   - Verifies source theme directory exists
   - Creates destination directory in `pub/static`
   - Recursively copies all files from source to destination
   - Counts files deployed

3. Processes jobs in parallel using goroutines
4. Reports results with timing and throughput metrics

## What It Doesn't Do (Yet)

This version performs pure file copying. The following are handled separately:

- **LESS/SCSS Compilation**: Done by Hyva theme's npm build process
- **JavaScript Minification**: Done by npm/webpack
- **CSS Minification**: Done by build tools
- **Symlink Fallback**: Not implemented
- **Admin Theme Deployment**: Skipped if theme doesn't exist (Magento core themes don't need custom deployment)
- **Vendor Theme Path Resolution**: Gracefully skips themes not found in app/design or vendor paths

## Comparison with Magento CLI

### Magento `setup:static-content:deploy`
- Requires full Magento bootstrap
- Single-threaded or limited parallelization
- PHP overhead for each file
- ~115 seconds for 28,500 files

### Go Tool
- Direct file operations
- True goroutine-based parallelization
- Minimal overhead per file
- ~0.3-0.5 seconds for 11,126 files

## Typical Workflow

1. **Development**: Use Hyva theme's npm build and cache-clean watch
   ```bash
   npm --prefix app/design/frontend/Vendor/Hyva/web/tailwind run dev
   ```

2. **Deployment Prep**: Run this tool to stage static files
   ```bash
   ./static-deploy -v
   ```

3. **Cache Clear** (if needed):
   ```bash
   bin/magento cache:clean
   ```

## Limitations and Future Improvements

Current version:
- ✓ Simple, fast file copying
- ✓ Parallel processing
- ✓ Multi-locale/theme support
- ✓ Verbose progress reporting

Not yet implemented:
- LESS/SCSS compilation (use npm instead)
- JavaScript bundling (use npm instead)
- Content version management
- Symlink fallback strategy
- Incremental deployment detection
- CDN push notifications
- File checksums/integrity checks

## Development

### Code Structure

- `main.go`: CLI interface, orchestration logic
- `watcher.go`: File change detection (for future watch mode)

### Building

```bash
go build -o static-deploy main.go watcher.go
```

### Performance Profiling

```bash
time ./static-deploy -v
```

## Integration with Existing Workflow

Since this tool only copies files, it integrates well with existing Magento setups:

1. Hyva theme builds are done via npm
2. Static files are copied to pub/static by this tool
3. Cache can be cleared separately as needed
