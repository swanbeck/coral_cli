#!/bin/bash

set -euo pipefail

# check user is not root
if [ "$(id -u)" -eq 0 ]; then
    echo "Warn: This script should not be run as root." >&2
    # exit 1
fi

# check if the 'darwin' user exists on the system
if ! id -u darwin &>/dev/null; then
    echo "Error: The 'darwin' user does not exist on this system." >&2
    exit 1
fi

# check if rsync is installed
if ! command -v rsync &>/dev/null; then
    echo "Error: rsync is not installed. Please install rsync and try again." >&2
    exit 1
fi

# make sure LIB_PATH is set inside the container 
: "${LIB_PATH:?Environment variable LIB_PATH is not set}"
