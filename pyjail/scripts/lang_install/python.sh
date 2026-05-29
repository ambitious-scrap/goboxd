#!/bin/bash

set -e 

if command -v /usr/bin/python >/dev/null 2>&1; then
    echo "Python is already installed. Skipping installation."
else
    wget https://www.python.org/ftp/python/2.7.18/Python-2.7.18.tgz
    tar xzf Python-2.7.18.tgz
    cd Python-2.7.18
    ./configure --enable-optimizations
    make altinstall
    ln -s "/usr/local/bin/python2.7" "/usr/bin/python"
    
fi

if /usr/bin/python --version >/dev/null 2>&1; then
    echo "Python installation verified successfully."
    /usr/bin/python -c "print('Python is working correctly!')"
else
    echo "Python installation verification failed."
    exit 1
fi