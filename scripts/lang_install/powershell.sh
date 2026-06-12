#!/usr/bin/env bash
set -euo pipefail
# Microsoft's apt repo ships the `powershell` package for x64 only; it has no
# arm64 build. Install from the GitHub release tarball so this works on both
# amd64 and arm64 hosts (e.g. Apple-silicon Colima).
PS_VERSION=7.4.6
case "$(dpkg --print-architecture)" in
  amd64) PS_ARCH=x64 ;;
  arm64) PS_ARCH=arm64 ;;
  *) echo "unsupported arch for powershell: $(dpkg --print-architecture)" >&2; exit 1 ;;
esac
# pwsh runtime deps.
apt-get install -y --no-install-recommends wget ca-certificates libicu70 libssl3
wget -q "https://github.com/PowerShell/PowerShell/releases/download/v${PS_VERSION}/powershell-${PS_VERSION}-linux-${PS_ARCH}.tar.gz" -O /tmp/powershell.tar.gz
mkdir -p /opt/microsoft/powershell/7
tar -xzf /tmp/powershell.tar.gz -C /opt/microsoft/powershell/7
rm -f /tmp/powershell.tar.gz
chmod +x /opt/microsoft/powershell/7/pwsh
ln -sf /opt/microsoft/powershell/7/pwsh /usr/bin/pwsh
pwsh --version
