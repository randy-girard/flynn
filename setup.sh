sudo su -l

apt-get update
add-apt-repository ppa:longsleep/golang-backports -y
apt-get install ca-certificates curl gcc
install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
chmod a+r /etc/apt/keyrings/docker.asc

# Add the repository to Apt sources:
echo \
  "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \
  $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}") stable" | \
  tee /etc/apt/sources.list.d/docker.list > /dev/null
apt-get update
apt-get install -y \
  docker-ce \
  docker-ce-cli \
  containerd.io \
  docker-buildx-plugin \
  docker-compose-plugin \
  jq \
  net-tools \
  ifupdown \
  zfsutils-linux \
  debootstrap \
  squashfs-tools \
  ca-certificates \
  make \
  curl \
  gcc \
  gnupg \
  libdigest-sha-perl \
  linux-modules-extra-$(uname -r)

cd /usr/local
# adjust version as you like; 1.20+ is fine for Flynn
wget https://go.dev/dl/go1.21.13.linux-amd64.tar.gz
rm -rf go
tar -xzf go1.21.13.linux-amd64.tar.gz

export PATH=/usr/local/go/bin:$PATH
go version
go env CGO_ENABLED
CGO_ENABLED=1 go env CGO_ENABLED
