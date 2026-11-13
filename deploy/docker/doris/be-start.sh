#!/bin/bash
set -euo pipefail
conf=/opt/apache-doris/be/conf/be.conf
sed -i '/^mem_limit[[:space:]]*=/d' "$conf"
# Keep 512 MiB below the 3 GiB container limit for process overhead.
printf '\nmem_limit = 2560m\n' >> "$conf"
# Idle RPC threads otherwise retain over 1 GiB in their 32 KiB allocation caches.
sed -i '/^JEMALLOC_CONF=/s/,tcache_max:[0-9]*//g; /^JEMALLOC_CONF=/s/"$/,tcache_max:4096"/' "$conf"
exec bash /usr/local/bin/init_be.sh
