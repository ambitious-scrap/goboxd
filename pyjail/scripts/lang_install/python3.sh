#!/bin/bash

set -e 

apt-get install -y --no-install-recommends \
    python3 \
    python3-pip \
    python3-dev \
    python3-venv \
    python3-psycopg2

if python3 --version >/dev/null 2>&1; then
    echo "Python3 installation verified successfully."
    python3 -c "print('Python3 is working correctly!')"
    python3 -c "import psycopg2; print('psycopg2 is working correctly!')"
else
    echo "Python3 installation verification failed."
    exit 1
fi

mkdir -p /virtualenvs
chown root /virtualenvs