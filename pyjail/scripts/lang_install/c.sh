#!/bin/bash

set -e 

if ! command -v /usr/bin/gcc &> /dev/null; then
    echo "GCC is not installed. Installing GCC..."
    sudo apt-get install -y gcc
fi

TEMP_C_FILE=$(mktemp /tmp/test_c_program.XXXX.c)
cat <<EOF > "$TEMP_C_FILE"
#include <stdio.h>

int main() {
    printf("C compiler is working correctly\\n");
    return 0;
}
EOF

TEMP_EXEC_FILE=$(mktemp /tmp/test_c_program.XXXX)
/usr/bin/gcc "$TEMP_C_FILE" -o "$TEMP_EXEC_FILE"

"$TEMP_EXEC_FILE"
if [ $? -eq 0 ]; then
    echo "C compiler is installed and working correctly."
else
    echo "C compiler failed to run."
    exit 1
fi

rm -f "$TEMP_C_FILE" "$TEMP_EXEC_FILE"