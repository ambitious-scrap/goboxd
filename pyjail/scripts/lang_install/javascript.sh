#!/bin/bash

set -e 

if command -v /usr/bin/node >/dev/null 2>&1; then
    echo "Node.js is already installed. Skipping installation."
else
    echo "Installing Node.js and npm..."
    curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
    apt-get install -y nodejs
fi

echo "console.log('Node.js is working!');" > test.js
/usr/bin/node test.js
if [ $? -eq 0 ]; then
    echo "Node.js installation verified successfully."
else
    echo "Node.js installation verification failed."
    exit 1
fi

rm test.js
