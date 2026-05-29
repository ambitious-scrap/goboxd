#!/bin/bash

set -e 

echo "Installing system dependencies..."
apt-get update
apt-get install -y --no-install-recommends \
    curl \
    ca-certificates \
    gnupg \
    build-essential \
    wget \
    nginx \
    git \
    unzip \
    postgresql \
    postgresql-common \
    libpq-dev \
    protobuf-compiler \
    libprotobuf-dev \
    protobuf-c-compiler \
    libprotobuf-c-dev \
    flex \
    bison \
    pkg-config \
    libnl-3-dev \
    libnl-route-3-dev \
    libnl-genl-3-dev \
    libnl-nf-3-dev

echo "Building nsjail version 3.4 from official Google repository..."
git clone --branch 3.4 --depth 1 https://github.com/google/nsjail.git ~/nsjail
cd ~/nsjail
make

echo "Running language installation scripts..."
SCRIPT_DIR="$(dirname "$0")"
for script in "$SCRIPT_DIR/lang_install"/*.sh; do
    if [ -f "$script" ]; then
        echo "Executing $script..."
        bash "$script"
    fi
done

apt-get clean
rm -rf /var/lib/apt/lists/*