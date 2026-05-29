#!/bin/bash

set -e 

echo "Installing Java..."

apt-get update && apt-get install -y --no-install-recommends \
    openjdk-17-jdk \
    && rm -rf /var/lib/apt/lists/*

ARCH=$(dpkg --print-architecture)
export JAVA_HOME="/usr/lib/jvm/java-17-openjdk-${ARCH}"
echo "export JAVA_HOME=/usr/lib/jvm/java-17-openjdk-${ARCH}" >> /etc/profile
echo "export PATH=\$JAVA_HOME/bin:\$PATH" >> /etc/profile

echo "public class Test { public static void main(String[] args) { System.out.println(\"Java is working!\"); } }" > Test.java

javac Test.java || { echo "Java compilation failed"; exit 1; }

java Test || { echo "Java execution failed"; exit 1; }

rm Test.java Test.class