#!/bin/bash

set -e

echo "Installing Verilog (iverilog)..."

if ! command -v /usr/bin/iverilog &> /dev/null; then
    apt-get install -y --no-install-recommends \
        iverilog=11.0-1.1+b1
    echo "iverilog installed successfully."
else
    echo "iverilog is already installed."
fi

echo "Verifying Verilog installation..."
cat <<EOF > test.v
module main;
    initial begin
        \$display("Verilog is installed and working!");
    end
endmodule
EOF

/usr/bin/iverilog -o test test.v
vvp test

if [ $? -eq 0 ]; then
    echo "Verilog program ran successfully."
    rm -f test.v test
else
    echo "Verilog program failed to run."
    exit 1
fi