mem_kib="$(awk '/^MemTotal:/ { print $2 }' /proc/meminfo)"
test -n "$mem_kib"
ram_gib="$(( (mem_kib + 1048575) / 1048576 ))"
if [ "$ram_gib" -lt 2 ]; then swap_gib="$((ram_gib * 2))"; elif [ "$ram_gib" -le 8 ]; then swap_gib="$ram_gib"; else swap_gib=4; fi
desired_bytes="$((swap_gib * 1073741824))"
current_bytes=0
if [ -f /swapfile ]; then current_bytes="$(stat -c %s /swapfile)"; fi
if [ "$current_bytes" -ne "$desired_bytes" ]; then
  available_bytes="$(df --output=avail -B1 / | tail -n 1 | tr -d ' ')"
  additional_bytes="$((desired_bytes - current_bytes))"
  if [ "$additional_bytes" -gt 0 ] && [ "$available_bytes" -lt "$additional_bytes" ]; then echo "insufficient disk space for ${swap_gib} GiB swap file" >&2; exit 1; fi
  if swapon --show=NAME --noheadings --raw | grep -Fxq /swapfile; then swapoff /swapfile; fi
  rm -f /swapfile
  if ! fallocate -l "${swap_gib}G" /swapfile; then dd if=/dev/zero of=/swapfile bs=1M count="$((swap_gib * 1024))" status=progress; fi
fi
chmod 600 /swapfile
if [ "$(blkid -p -s TYPE -o value /swapfile 2>/dev/null || true)" != "swap" ]; then mkswap /swapfile >/dev/null; fi
if ! swapon --show=NAME --noheadings --raw | grep -Fxq /swapfile; then swapon /swapfile; fi
if ! grep -Eq '^[[:space:]]*/swapfile[[:space:]]+none[[:space:]]+swap[[:space:]]' /etc/fstab; then printf '%s\n' '/swapfile none swap sw 0 0' >> /etc/fstab; fi
echo "configured ${swap_gib} GiB swap for ${ram_gib} GiB RAM"
