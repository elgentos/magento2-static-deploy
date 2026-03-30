# Magento Static Content Deployer (Go)

Experimental!! A high-performance static content deployment tool written in Go that significantly accelerates Magento 2 static asset deployment by leveraging true parallelization and efficient file I/O.

**Automatic Theme Detection**: The tool automatically detects whether a theme is Hyvä-based (uses fast Go deployment) or Luma-based (dispatches to `bin/magento setup:static-content:deploy` for proper LESS/RequireJS compilation).

## Performance

On this project, deployment improved from **~115 seconds** (Magento native) to **~0.3-0.5 seconds** for the frontend theme deployment:

- **Vendor/Hyva/frontend**: 11,126 files deployed in 0.3 seconds
- **Throughput**: ~40,000 files/second
- **Speedup**: **230-380x faster** than PHP implementation

## Why It's Faster

1. **Native Parallelization**: Go's goroutines handle true concurrent I/O across multiple CPU cores
2. **Low Overhead**: No full Magento bootstrap, no dependency injection container, no database access
3. **Efficient I/O**: Optimized file copying with buffered I/O and minimal memory allocation
4. **Minimal Compilation**: Only compiles email CSS (using PHP's wikimedia/less.php); main theme CSS handled by npm build

## Installation

### Build from Source

```bash
cd tools/magento2-static-deploy
go build -o magento2-static-deploy main.go watcher.go less.go less_preprocessor.go
```

### Requirements

- Go 1.21 or later
- PHP available in PATH (uses Magento's `wikimedia/less.php` for email CSS compilation)

## Usage

The CLI is designed to be compatible with Magento's `bin/magento setup:static-content:deploy` command.

### Basic Usage

Deploy Vendor/Hyva theme to frontend area:

```bash
./magento2-static-deploy -f --area=frontend --theme=Vendor/Hyva nl_NL
```

### With Verbose Output

```bash
./magento2-static-deploy -f -a frontend -t Vendor/Hyva -j 8 -v nl_NL en_US
```

### Note on Admin Themes

By default, only `frontend` area is deployed. This is because:
- Admin themes (Magento/backend, MageOS/m137-admin-theme) are part of Magento core
- They don't typically need custom deployment unless you have custom admin theme
- If deployment encounters a missing theme, it gracefully skips it

To deploy admin themes if they exist:
```bash
./magento2-static-deploy -f -a frontend -a adminhtml -v nl_NL
```

### All Options

```
Arguments:
  languages    Space-separated list of ISO-639 language codes (e.g., nl_NL en_US)

Options:
  -r, --root string              Path to Magento root directory (default ".")

  -a, --area stringArray         Generate files only for the specified areas
                                 Can be repeated: -a frontend -a adminhtml
                                 Default: frontend

  -t, --theme stringArray        Generate static view files for only the specified themes
                                 Can be repeated: -t Vendor/Hyva -t Hyva/reset
                                 Default: Vendor/Hyva

  -l, --language stringArray     Generate files only for the specified languages
                                 Can be repeated: -l nl_NL -l en_US
                                 Alternative to positional arguments

  -j, --jobs int                 Enable parallel processing using the specified number of jobs
                                 Default: 0 (auto-detect CPU count)

  -s, --strategy string          Deploy files using specified strategy (default "quick")
                                 Note: Currently informational only

  -f, --force                    Deploy files in any mode

      --content-version string   Custom version of static content
                                 Default: auto-generate timestamp

  -v, --verbose                  Verbose output showing per-deployment progress

      --no-luma-dispatch         Disable automatic dispatch of Luma themes to bin/magento
                                 Treats all themes as Hyvä (fast copy-only deployment)

      --php string               Path to PHP binary for Luma theme dispatch (default "php")

      --symlink string           Use symlinks instead of file copies to reduce disk usage:
                                 'file'   - per-file relative symlinks to source files
                                 'locale' - directory-level symlinks for identical locales
                                            (also uses per-file symlinks for the base locale)
```

## Examples

### Deploy Single Locale/Theme

```bash
./magento2-static-deploy -f -r /var/www/magento -a frontend -t Vendor/Hyva nl_NL
```

### Deploy Multiple Locales and Themes

```bash
./magento2-static-deploy -f \
  -a frontend \
  -t Vendor/Hyva -t Magento/blank \
  nl_NL en_US de_DE
```

### Sequential Processing (1 Job)

```bash
./magento2-static-deploy -f -j 1 -v nl_NL
```

### Full Admin + Frontend Deployment

```bash
./magento2-static-deploy -f \
  -a frontend -a adminhtml \
  -t Vendor/Hyva \
  nl_NL
```

### Reuse Content Version (for Split Deployments)

When splitting deployments across multiple runs (e.g., deploying different locales or themes in parallel), you can reuse the same content version:

```bash
# First deployment
./magento2-static-deploy -f \
  -t Vendor/Hyva \
  --content-version=1234567890 \
  nl_NL

# Second deployment with the same version
./magento2-static-deploy -f \
  -t Vendor/Hyva \
  --content-version=1234567890 \
  en_US de_DE
```

This is useful for deployment tools like [Deployer](https://github.com/deployphp/deployer) or Hypernode Deploy that optimize deployments by splitting locale-theme combinations across multiple processes.

## Automatic Theme Detection

The tool automatically detects whether each theme is Hyvä-based or Luma-based:

**Hyvä themes** are detected by:
1. Checking if the theme inherits from `Hyva/default` or `Hyva/reset`
2. Looking for `web/tailwind/tailwind.config.js` or `web/tailwind/tailwind-source.css` in the theme. (For Tailwind v3 and v4 respectively)

**Luma themes** are everything else (including Magento/blank, Magento/luma, and custom Luma-based themes).

### Mixed Theme Deployment

When deploying multiple themes, the tool automatically handles them appropriately:

```bash
./magento2-static-deploy -f -a frontend -t Vendor/Hyva -t Magento/luma -v nl_NL

# Output:
# 🎨 Vendor/Hyva detected as Hyvä theme
# 🎨 Magento/luma detected as Luma theme
#
# Deploying Hyvä themes using Go binary...
# ✓ Vendor/Hyva/frontend (nl_NL) - 4497 files - 0.2s
#
# Dispatching Luma themes to bin/magento...
# Executing: php bin/magento setup:static-content:deploy -f --area=frontend --theme=Magento/luma nl_NL
# ...
```

This makes the Go binary a drop-in replacement that handles both theme types intelligently.

### Disable Luma Dispatch

If you want to treat all themes as Hyvä (copy-only deployment without LESS/RequireJS compilation):

```bash
./magento2-static-deploy -f --no-luma-dispatch -t Magento/luma nl_NL
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

## Symlink Modes

The `--symlink` flag reduces disk usage by creating symlinks instead of copying files.

### Per-File Symlinks (`--symlink=file`)

Creates relative symlinks from each destination file back to its source:

    ./magento2-static-deploy -f --symlink=file -t Vendor/Hyva nl_NL en_US

Each file in `pub/static/frontend/Vendor/Hyva/nl_NL/` will be a symlink pointing
to its source in `vendor/`, `app/design/`, or `lib/web/`.

### Locale-Level Symlinks (`--symlink=locale`)

For Hyva themes, all locales produce identical file trees (locale is only used as
a path segment, not for file selection). This mode deploys only one locale per
theme+area combination and symlinks the rest:

    ./magento2-static-deploy -f --symlink=locale -t Vendor/Hyva nl_NL en_US de_DE

Result:
- `pub/static/frontend/Vendor/Hyva/nl_NL/` — real deployment (with per-file symlinks)
- `pub/static/frontend/Vendor/Hyva/en_US` → `nl_NL` (directory symlink)
- `pub/static/frontend/Vendor/Hyva/de_DE` → `nl_NL` (directory symlink)

### Web Server Configuration

Your web server must follow symlinks when serving from `pub/static/`. This is typically
the default but verify your configuration:

- **Nginx**: Follows symlinks by default (no config needed)
- **Apache**: Ensure `Options +FollowSymlinks` is set for the `pub/static/` directory

### Limitations

- **Windows**: Symlinks require elevated privileges or Developer Mode on Windows
- **Luma themes**: Symlink modes only apply to Hyva theme deployments (Luma themes
  dispatched to `bin/magento` are unaffected)

## What It Doesn't Do (Yet)

This version performs file copying plus email CSS compilation. The following are handled separately:

- **Full LESS/SCSS Compilation**: Done by Hyva theme's npm build process (email CSS is compiled by this tool using PHP)
- **JavaScript Minification**: Done by npm/webpack
- **CSS Minification**: Done by build tools (email CSS is minified by wikimedia/less.php)
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
   ./magento2-static-deploy -v
   ```

3. **Cache Clear** (if needed):
   ```bash
   bin/magento cache:clean
   ```

## Limitations

### Hyva Themes Only

**This tool is designed specifically for Hyva-based themes** and will not produce identical output to Magento's native `setup:static-content:deploy` for Luma/Blank themes.

#### What's Missing for Luma Support

Magento's native static deploy performs several compilation and generation steps that this tool does not:

| Feature | Magento Native | This Tool |
|---------|---------------|-----------|
| File copying | ✅ | ✅ |
| Email CSS compilation | ✅ | ✅ (via wikimedia/less.php) |
| LESS → CSS compilation (full) | ✅ | ❌ |
| RequireJS config merging | ✅ | ❌ |
| JS translation generation | ✅ | ❌ |
| JavaScript bundling | ✅ | ❌ |
| SRI hash generation | ✅ | ❌ |

#### Why Not Implement Full Luma Support?

Implementing full Luma compatibility would require:

1. **Full LESS Compilation** - Compiling all theme LESS files (not just email CSS) with proper source file resolution
2. **RequireJS Config Merging** - Parsing and merging JavaScript config objects from all modules
3. **JS Translation Generation** - Reading Magento's PHP translation dictionaries and converting to JSON
4. **JavaScript Bundling** - Implementing Magento's complex bundling logic

While we now use PHP's `wikimedia/less.php` for email CSS compilation (matching Magento's behavior), implementing full Luma LESS compilation would require recreating Magento's complex source file resolution and preprocessing logic.

**For Hyva themes, none of this is needed** because:
- Hyva uses Tailwind CSS (pre-built), not LESS
- Hyva doesn't use RequireJS
- Hyva handles translations differently
- JavaScript is bundled via npm/webpack during theme build

#### Recommendation

- **Hyva themes**: Use this tool for 70-90x faster deployments
- **Luma/Blank themes**: Continue using `bin/magento setup:static-content:deploy`

### Current Capabilities

- ✅ Fast parallel file copying
- ✅ Multi-locale/theme/area support
- ✅ Theme module overrides (`app/design/{area}/{vendor}/{theme}/{Module}/web/`)
- ✅ Vendor module web assets
- ✅ Library files (`lib/web/`)
- ✅ Content version management
- ✅ Verbose progress reporting
- ✅ Email CSS compilation (email.css, email-inline.css, email-fonts.css)
- ✅ Symlink modes for reduced disk usage

### Not Implemented

- ❌ Full LESS/SCSS compilation (use npm for Hyva)
- ❌ RequireJS config merging
- ❌ JavaScript bundling
- ❌ JS translation generation
- ❌ Symlink fallback strategy
- ❌ Incremental deployment detection

### Email CSS Differences

The email CSS output is nearly identical to Magento's native output since we use the same PHP LESS compiler (`wikimedia/less.php`). Minor differences may occur:

1. **Font family**: The Go binary correctly resolves theme variable inheritance (e.g., `'Open Sans'` from Blank theme), while Magento's preprocessing may produce different results depending on the theme hierarchy.

2. **URL placeholders**: Both use the correct `{{base_url_path}}` format for email-fonts.css imports.

These differences are functionally equivalent and should not affect email rendering.

## Development

### Code Structure

- `main.go`: CLI interface, orchestration logic
- `watcher.go`: File change detection (for future watch mode)
- `less.go`: LESS to CSS compilation using PHP's wikimedia/less.php (same as Magento)
- `less_preprocessor.go`: Magento-style LESS preprocessing (@magento_import, source staging)

### Building

```bash
go build -o magento2-static-deploy main.go watcher.go less.go less_preprocessor.go
```

### Performance Profiling

```bash
time ./magento2-static-deploy -v
```

## Integration with Existing Workflow

Since this tool only copies files, it integrates well with existing Magento setups:

1. Hyva theme builds are done via npm
2. Static files are copied to pub/static by this tool
3. Cache can be cleared separately as needed

## CI/CD Integration

This tool is designed as a **drop-in replacement** for Magento's `bin/magento setup:static-content:deploy` with Magento-compatible CLI flags. Typical results:

- **115 seconds → 0.3 seconds** for frontend deployment
- **230-380x faster** than the PHP implementation
- **40,000+ files/second** throughput

### GitLab CI/CD

If you're using a GitLab CI/CD pipeline with a `setup-static-content-deploy` job, you can replace it entirely with this Go binary. The tool automatically detects Hyvä vs Luma themes and dispatches Luma themes to `bin/magento` when needed:

```yaml
# Replace setup-static-content-deploy with Go binary (230x faster for Hyvä, auto-fallback for Luma)
setup-static-content-deploy:
  stage: build
  script:
    - source ~/.nvm/nvm.sh
    - nvm use ${NODE_VERSION:---lts} || nvm install ${NODE_VERSION:---lts}
    # Build Hyva theme assets
    - if [ ! -z $THEME_PATH ]; then npm --prefix $THEME_PATH ci --no-audit; fi
    - if [ ! -z $THEME_PATH ]; then NODE_ENV=production npm --prefix $THEME_PATH run build-prod; fi
    # Download Go binary
    - curl -sL -o /tmp/magento2-static-deploy https://github.com/elgentos/magento2-static-deploy/releases/latest/download/magento2-static-deploy-linux-amd64
    - chmod +x /tmp/magento2-static-deploy
    # Deploy all static content (Hyvä themes use Go, Luma themes auto-dispatch to bin/magento)
    - /tmp/magento2-static-deploy --force --area=frontend --area=adminhtml --theme=${THEMES} --verbose ${STATIC_LOCALES}
```

The Go binary acts as a drop-in replacement - Hyvä themes get the fast Go deployment while Luma/adminhtml themes are automatically handed off to `bin/magento setup:static-content:deploy`.

### Deployer (Pull Approach)

If you use [Deployer](https://deployer.org/) with a pull-based deployment strategy, you can override the `magento:deploy:assets` task. The Go binary handles both Hyvä and Luma themes automatically:

```php
task('magento:deploy:assets', function () {
    // Download the Go binary
    within("{{release_or_current_path}}", function () {
        run('curl -sL -o /tmp/magento2-static-deploy https://github.com/elgentos/magento2-static-deploy/releases/latest/download/magento2-static-deploy-linux-amd64');
        run('chmod +x /tmp/magento2-static-deploy');
    });

    // Deploy all static content (Hyvä uses Go, Luma auto-dispatches to bin/magento)
    $themes = implode(' ', array_map(fn($t) => "--theme=$t", get('magento_themes')));
    $locales = implode(' ', get('magento_locales'));

    within("{{release_or_current_path}}", function () use ($themes, $locales) {
        run("/tmp/magento2-static-deploy --force --area=frontend --area=adminhtml $themes --verbose $locales");
    });
});
```

Make sure your configuration is set up in your `deploy.php`:

```php
set('magento_themes', ['Vendor/Hyva', 'Magento/backend']);
set('magento_locales', ['nl_NL', 'en_US']);
```
