#!/bin/bash

set -e 

if command -v /usr/bin/psql >/dev/null 2>&1; then
    echo "PostgreSQL is already installed. Skipping installation."
else
    echo "Installing PostgreSQL and postgresql-common..."
    apt-get install -y postgresql postgresql-common

    echo "Starting PostgreSQL service..."
    service postgresql start
fi

if /usr/bin/psql --version >/dev/null 2>&1; then
    echo "PostgreSQL installation verified successfully."
else
    echo "PostgreSQL installation verification failed."
    exit 1
fi

echo "Setting up directories..."
mkdir -p /db_backups
chown root /db_backups

echo "Extracting database backups..."
if [[ -f "/app/db_backups.tar.gz" ]]; then
    tar xzf /app/db_backups.tar.gz -C /db_backups/
    echo "Database backups extracted successfully."
elif [[ -d "/app/db_backups" ]]; then
    cp -r /app/db_backups/* /db_backups/
    echo "Database backups copied successfully."
else
    echo "Warning: No database backups found. Skipping."
fi

