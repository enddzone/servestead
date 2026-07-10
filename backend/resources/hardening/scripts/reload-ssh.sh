passwd -l root >/dev/null 2>&1 || true
install -d -m 0755 -o root -g root /run/sshd
/usr/sbin/sshd -t
systemctl reload-or-restart ssh || systemctl reload-or-restart sshd
