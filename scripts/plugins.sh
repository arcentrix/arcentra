#!/bin/bash

# ================================================================
# Plugin Management Script
# Purpose: Build, package, checksum and test plugins
# Usage: 
#   ./scripts/plugins.sh build      # Build plugins only
#   ./scripts/plugins.sh package    # Package plugins only (requires build first)
#   ./scripts/plugins.sh all        # Build and package (default)
#   ./scripts/plugins.sh checksum <plugin_file>  # Generate plugin checksum
#   ./scripts/plugins.sh test       # Test plugin auto-loading
#   ./scripts/plugins.sh [JOBS]     # Specify parallel jobs count
# ================================================================

set -euo pipefail

# Color definitions
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Configuration
PLUGINS_SRC_DIR="pkg/plugins"
PLUGINS_OUT_DIR="plugins"

# Get project root directory
PROJECT_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$PROJECT_ROOT"

# Check if running from project root
if [ ! -f "go.mod" ]; then
    echo -e "${RED}Error: Please run this script from the project root directory${NC}"
    exit 1
fi

# Parse arguments
ACTION="${1:-all}"
JOBS="${2:-$(getconf _NPROCESSORS_ONLN 2>/dev/null || echo 4)}"

# If first argument is a number, treat it as JOBS and set ACTION to all
if [[ "$ACTION" =~ ^[0-9]+$ ]]; then
    JOBS="$ACTION"
    ACTION="all"
fi

# Build plugins function
build_plugins() {
    echo -e "${BLUE}>> Building RPC plugins from ${PLUGINS_SRC_DIR} to ${PLUGINS_OUT_DIR}${NC}"

    # Create output directory
    mkdir -p "$PLUGINS_OUT_DIR"

    # Use go list to find all main package plugin directories
    # Then use awk to process output and determine output filenames
    go list -f '{{if eq .Name "main"}}{{.Dir}} {{range .GoFiles}}{{.}} {{end}}{{end}}' ./${PLUGINS_SRC_DIR}/... \
    | sed -e '/^[[:space:]]*$/d' \
    | awk '
    function basename(p){sub(".*/","",p); return p}
    {
      dir=$1; outbase="";
      for(i=2;i<=NF;i++){ if($i=="main.go"){ outbase="main"; break } }
      if(outbase==""){ for(i=2;i<=NF;i++){ f=$i; sub(/\.go$/,"",f); if(f ~ /^main.*/){ outbase=f; break } } }
      if(outbase==""){ outbase=basename(dir) }
      printf "%s %s/%s\n", dir, "'"${PLUGINS_OUT_DIR}"'", outbase;
    }' \
    | xargs -P "$JOBS" -n 2 sh -c '
        dir="$1"; out="$2";
        echo "   -> $dir  ==>  $out";
        cd "'"${PROJECT_ROOT}"'" && \
        go build -o "$out" "$dir"
    ' sh

    echo -e "${GREEN}>> RPC plugins build completed${NC}"
    echo -e "${BLUE}>> Built plugins:${NC}"
    ls -lh "$PLUGINS_OUT_DIR"/ | grep -v "^total" | grep -v "^d" || true
    echo ""
}

# Package plugins function
package_plugins() {
    # Check if output directory exists
    if [ ! -d "$PLUGINS_OUT_DIR" ]; then
        echo -e "${YELLOW}Warning: Output directory does not exist, please build plugins first${NC}"
        return 1
    fi

    echo -e "${BLUE}>> Packaging plugins...${NC}"

    local packaged_count=0
    local skipped_count=0

    # Iterate through all files in output directory
    for plugin_path in "$PLUGINS_OUT_DIR"/*; do
        # Check if file is executable and not a zip file
        if [ -f "$plugin_path" ] && [ -x "$plugin_path" ] && [ "${plugin_path%.zip}" = "$plugin_path" ]; then
            plugin_name=$(basename "$plugin_path")
            plugin_base="$plugin_name"
            
            # Find corresponding source directory
            src_dir=$(find "$PLUGINS_SRC_DIR" -type d -name "$plugin_base" | head -1)
            
            # Check if manifest.json exists
            if [ -n "$src_dir" ] && [ -f "$src_dir/manifest.json" ]; then
                zip_file="${PLUGINS_OUT_DIR}/${plugin_base}.zip"
                echo -e "${GREEN}   -> Packaging $plugin_name to $zip_file${NC}"
                cd "$PROJECT_ROOT" && \
                zip -j "$zip_file" "$plugin_path" "$src_dir/manifest.json" > /dev/null 2>&1
                ((packaged_count++))
            else
                echo -e "${YELLOW}   !! manifest.json not found for $plugin_name, skipping${NC}"
                ((skipped_count++))
            fi
        fi
    done

    echo -e "${GREEN}>> Plugin packaging completed${NC}"
    echo -e "${BLUE}>> Packaged: ${packaged_count}, Skipped: ${skipped_count}${NC}"
    echo ""
}

# Generate plugin checksum function
generate_checksum() {
    local plugin_file="${2:-}"
    
    if [ -z "$plugin_file" ]; then
        echo -e "${RED}Error: Please provide plugin file path${NC}"
        echo ""
        echo "Usage:"
        echo "  $0 checksum <plugin_file>"
        echo "Example: $0 checksum plugins/stdout"
        exit 1
    fi

    # Check if file exists
    if [ ! -f "$plugin_file" ]; then
        echo -e "${RED}Error: File does not exist: $plugin_file${NC}"
        exit 1
    fi

    echo -e "${YELLOW}Calculating SHA256 checksum...${NC}"
    local checksum=$(sha256sum "$plugin_file" | awk '{print $1}')

    # Get file information
    local file_size=$(ls -lh "$plugin_file" | awk '{print $5}')
    local file_name=$(basename "$plugin_file")

    # Output results
    echo ""
    echo -e "${GREEN}========================================${NC}"
    echo -e "${GREEN}Plugin File Information${NC}"
    echo -e "${GREEN}========================================${NC}"
    echo "File Path: $plugin_file"
    echo "File Name: $file_name"
    echo "File Size: $file_size"
    echo ""
    echo -e "${YELLOW}SHA256 Checksum:${NC}"
    echo -e "${GREEN}$checksum${NC}"
    echo ""

    # Generate SQL update statement
    echo -e "${YELLOW}SQL Update Statement:${NC}"
    echo "UPDATE \`t_plugin\` SET \`checksum\` = '$checksum' WHERE \`entry_point\` = '$plugin_file' OR \`install_path\` = '$plugin_file';"
    echo ""

    # Generate verification command
    echo -e "${YELLOW}Verification Command:${NC}"
    echo "sha256sum -c <<< \"$checksum  $plugin_file\""
    echo ""

    # Save to file
    local checksum_file="${plugin_file}.sha256"
    echo "$checksum  $plugin_file" > "$checksum_file"
    echo -e "${GREEN}✓ Checksum saved to: $checksum_file${NC}"
    echo ""
}

# Test plugin auto-loading function
test_autoload() {
    echo "========================================"
    echo -e "${BLUE}[INFO]${NC} Arcentra Plugin Auto-loading Test Script"
    echo "========================================"
    echo ""

    # Create necessary directories
    echo -e "${BLUE}[INFO]${NC} Creating plugin directories..."
    mkdir -p plugins
    mkdir -p conf.d
    mkdir -p examples/plugin_autowatch
    echo -e "${GREEN}[SUCCESS]${NC} Directories created"
    echo ""

    # Check example plugin
    echo -e "${BLUE}[INFO]${NC} Checking example plugin..."
    if [ -f "plugins/stdout.so" ]; then
        echo -e "${GREEN}[SUCCESS]${NC} Found example plugin: plugins/stdout.so"
    else
        echo -e "${YELLOW}[WARN]${NC} Example plugin not found, you need to build plugins first"
        echo -e "${BLUE}[INFO]${NC} Build command example:"
        echo "  cd pkg/plugins/notify/stdout"
        echo "  go build -buildmode=plugin -o ../../../../plugins/stdout.so stdout.go"
    fi
    echo ""

    # Check configuration file
    echo -e "${BLUE}[INFO]${NC} Checking configuration file..."
    if [ -f "conf.d/plugins.yaml" ]; then
        echo -e "${GREEN}[SUCCESS]${NC} Found configuration file: conf.d/plugins.yaml"
    else
        echo -e "${YELLOW}[WARN]${NC} Configuration file not found, creating example config..."
        cat > conf.d/plugins.yaml << 'EOF'
# Plugin configuration file
# Supports auto-monitoring and hot-reload

plugins:
  - path: ./plugins/stdout.so
    name: stdout
    type: notify
    version: "1.0.0"
    config:
      prefix: "[Notification]"
EOF
        echo -e "${GREEN}[SUCCESS]${NC} Configuration file created: conf.d/plugins.yaml"
    fi
    echo ""

    # Check demo program
    echo -e "${BLUE}[INFO]${NC} Checking demo program..."
    if [ -f "examples/plugin_autowatch/main.go" ]; then
        echo -e "${GREEN}[SUCCESS]${NC} Found demo program"
    else
        echo -e "${RED}[ERROR]${NC} Demo program not found: examples/plugin_autowatch/main.go"
        exit 1
    fi
    echo ""

    # Display usage instructions
    echo "========================================"
    echo -e "${BLUE}[INFO]${NC} Usage Instructions"
    echo "========================================"
    echo ""
    echo "1. Start the demo program:"
    echo -e "   ${GREEN}go run examples/plugin_autowatch/main.go${NC}"
    echo ""
    echo "2. In another terminal window, you can perform the following operations:"
    echo ""
    echo "   a) Add a new plugin:"
    echo -e "      ${GREEN}cp /path/to/new-plugin.so plugins/${NC}"
    echo "      The system will automatically detect and load the new plugin"
    echo ""
    echo "   b) Remove a plugin:"
    echo -e "      ${GREEN}rm plugins/some-plugin.so${NC}"
    echo "      The system will automatically unload the plugin"
    echo ""
    echo "   c) Modify configuration:"
    echo -e "      ${GREEN}vim conf.d/plugins.yaml${NC}"
    echo "      The system will automatically reload configuration after saving"
    echo ""
    echo "3. Check log output to observe plugin loading and unloading process"
    echo ""
    echo "4. Press Ctrl+C to stop the demo program"
    echo ""

    echo "========================================"
    echo -e "${BLUE}[INFO]${NC} Preparation"
    echo "========================================"
    echo ""

    # Ask if compile example plugin
    if [ ! -f "plugins/stdout.so" ]; then
        read -p "Compile example plugin? (y/n) " -n 1 -r
        echo
        if [[ $REPLY =~ ^[Yy]$ ]]; then
            echo -e "${BLUE}[INFO]${NC} Compiling example plugin..."
            if [ -f "pkg/plugins/notify/stdout/stdout.go" ]; then
                cd pkg/plugins/notify/stdout
                go build -buildmode=plugin -o ../../../../plugins/stdout.so stdout.go
                cd - > /dev/null
                echo -e "${GREEN}[SUCCESS]${NC} Plugin compiled: plugins/stdout.so"
            else
                echo -e "${YELLOW}[WARN]${NC} Plugin source code not found: pkg/plugins/notify/stdout/stdout.go"
            fi
        fi
        echo ""
    fi

    # Ask if start demo
    echo "========================================"
    read -p "Start demo program now? (y/n) " -n 1 -r
    echo
    echo ""

    if [[ $REPLY =~ ^[Yy]$ ]]; then
        echo "========================================"
        echo -e "${BLUE}[INFO]${NC} Starting demo program..."
        echo "========================================"
        echo ""
        sleep 1
        go run examples/plugin_autowatch/main.go
    else
        echo -e "${BLUE}[INFO]${NC} You can run it manually later:"
        echo -e "  ${GREEN}go run examples/plugin_autowatch/main.go${NC}"
        echo ""
    fi

    echo -e "${GREEN}[SUCCESS]${NC} Test script execution completed"
}

# Main logic
case "$ACTION" in
    build)
        build_plugins
        ;;
    package)
        package_plugins
        ;;
    all)
        build_plugins
        package_plugins
        ;;
    checksum)
        generate_checksum "$@"
        ;;
    test|autoload)
        test_autoload
        ;;
    *)
        echo -e "${RED}Error: Unknown action '$ACTION'${NC}"
        echo ""
        echo "Usage:"
        echo "  $0 build [JOBS]             # Build plugins only"
        echo "  $0 package                  # Package plugins only"
        echo "  $0 all [JOBS]               # Build and package (default)"
        echo "  $0 checksum <plugin_file>   # Generate plugin checksum"
        echo "  $0 test                     # Test plugin auto-loading"
        echo "  $0 [JOBS]                   # Specify parallel jobs count, execute all operations"
        exit 1
        ;;
esac

if [ "$ACTION" != "test" ] && [ "$ACTION" != "autoload" ]; then
    echo -e "${GREEN}✓ Operation completed${NC}"
fi
