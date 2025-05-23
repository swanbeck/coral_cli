#!/bin/bash

set -euo pipefail

# check if rsync is installed
if ! command -v rsync &>/dev/null; then
    echo "Error: rsync is not installed. Please install rsync and try again." >&2
    exit 1
fi

# make sure LIB_PATH is set inside the container 
: "${LIB_PATH:?Environment variable LIB_PATH is not set}"

QUIET=${QUIET:-true}
EXPORT_PATH=${EXPORT_PATH:-/export}

# create log file name and directory
LOG_FILE=${EXPORT_PATH}/logs/${IMAGE_ID}.log
mkdir -p ${EXPORT_PATH}/logs

if [ "$QUIET" = true ]; then
    rsync -au --out-format="%n" "$LIB_PATH/" "$EXPORT_PATH/" | tee "$LOG_FILE" > /dev/null
    if [ -f "$EXPORT_PATH/docker.yaml" ]; then
        DOCKER_FILE=${EXPORT_PATH}/docker/${IMAGE_ID}.yaml
        mkdir -p "$EXPORT_PATH/docker" > /dev/null 2>&1
        cp "$EXPORT_PATH/docker.yaml" "$DOCKER_FILE" > /dev/null 2>&1
        rm "$EXPORT_PATH/docker.yaml" > /dev/null 2>&1
    fi
else
    rsync -au --out-format="%n" "$LIB_PATH/" "$EXPORT_PATH/" | tee "$LOG_FILE"
    if [ -f "$EXPORT_PATH/docker.yaml" ]; then
        DOCKER_FILE=${EXPORT_PATH}/docker/${IMAGE_ID}.yaml
        mkdir -p "$EXPORT_PATH/docker"
        cp "$EXPORT_PATH/docker.yaml" "$DOCKER_FILE"
        rm "$EXPORT_PATH/docker.yaml"
    fi
fi

# set permissions for the export path
chown -R 1000:1000 "$EXPORT_PATH" 2>/dev/null || true
