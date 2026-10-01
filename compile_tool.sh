#!/bin/bash

# Builds PortTool for the three platforms, plus an Apple Silicon build.

OUTPUT_DIR="./Output"

# Platforms to build: "GOOS:extension"
PLATFORMS=(
    "windows:.exe"
    "darwin:"
    "linux:"
)

for entry in "${PLATFORMS[@]}"; do
    PLATFORM="${entry%%:*}"
    EXTENSION="${entry#*:}"

    PLATFORM_DIR="$OUTPUT_DIR/$PLATFORM"
    OUTPUT_FILE="$PLATFORM_DIR/PortTool$EXTENSION"

    mkdir -p "$PLATFORM_DIR"

    echo "Building PortTool for the '$PLATFORM' platform..."
    GOOS=$PLATFORM GOARCH=amd64 go build -o "$OUTPUT_FILE" ./cmd/porttool

    if [ $? -eq 0 ]; then
        echo "Build succeeded! The executable is saved at: $OUTPUT_FILE"
    else
        echo "Build failed for PortTool on '$PLATFORM'. Please check the error messages."
        exit 1
    fi
done

# Apple Silicon Macs.
# See $PROD/maps/porttool-on-linux-and-macos/issues/XPT-02-whether-to-ship-arm64.md
ARM_MAC_DIR="$OUTPUT_DIR/darwin-arm64"
mkdir -p "$ARM_MAC_DIR"
echo "Building PortTool for the 'darwin-arm64' platform..."
if GOOS=darwin GOARCH=arm64 go build -o "$ARM_MAC_DIR/PortTool" ./cmd/porttool; then
    echo "Build succeeded! The executable is saved at: $ARM_MAC_DIR/PortTool"
else
    echo "Build failed for PortTool on 'darwin-arm64'. Please check the error messages."
    exit 1
fi

# PortTool's plan page reads plan files from a plans/ folder beside the
# executable. Existing files are overwritten and extra ones left alone: the
# shipped plans belong to this repository, but a plan somebody wrote on a line
# is theirs.
for PLATFORM_DIR in "$OUTPUT_DIR/windows" "$OUTPUT_DIR/darwin" "$OUTPUT_DIR/linux" "$ARM_MAC_DIR"; do
    mkdir -p "$PLATFORM_DIR/plans"
    cp ./TestCase/plans/*.json "$PLATFORM_DIR/plans/"
done
