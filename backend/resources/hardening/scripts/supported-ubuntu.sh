. /etc/os-release
[[ "$ID" = "ubuntu" ]]
dpkg --compare-versions "$VERSION_ID" ge 22.04
kernel_version="$(uname -r | cut -d- -f1)"
dpkg --compare-versions "$kernel_version" ge 5.15
