apt-get update
add-apt-repository ppa:longsleep/golang-backports -y
apt-get install -y ca-certificates curl gcc

rm -f /etc/apt/sources.list.d/docker.*

# Dynamic Ubuntu codename
export UBUNTU_CODENAME=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")

if [ "$UBUNTU_CODENAME" = "xenial" ]; then
  echo "Detected Xenial — using legacy Docker setup"

  # Keyring
  rm -rf /etc/apt/keyrings/docker.asc
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc

  # Docker repo
  echo "deb [arch=$(dpkg --print-architecture) trusted=yes] \
  https://download.docker.com/linux/ubuntu $UBUNTU_CODENAME stable" \
  | tee /etc/apt/sources.list.d/docker.list > /dev/null
  add-apt-repository -y "deb http://archive.ubuntu.com/ubuntu $UBUNTU_CODENAME-backports main universe"
  apt-get update
  apt-get install -y \
    docker-ce \
    docker-ce-cli \
    containerd.io \
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

  # Install Buildx manually
  mkdir -p ~/.docker/cli-plugins/
  #if [ "$ARCH" = "x86_64" ]; then
      BUILDX_URL="https://github.com/docker/buildx/releases/download/v0.23.0/buildx-v0.23.0.linux-amd64"
  #elif [ "$ARCH" = "aarch64" ]; then
  #    BUILDX_URL="https://github.com/docker/buildx/releases/download/v0.23.0/buildx-v0.23.0.linux-arm64"
  #else
  #    echo "Unsupported architecture: $ARCH"
  #    exit 1
  #fi
  curl -fL -o ~/.docker/cli-plugins/docker-buildx "$BUILDX_URL"
  chmod +x ~/.docker/cli-plugins/docker-buildx

  # Install docker-compose manually
  curl -SL https://github.com/docker/compose/releases/latest/download/docker-compose-linux-x86_64 \
      -o ~/.docker/cli-plugins/docker-compose
  chmod +x ~/.docker/cli-plugins/docker-compose
elif [ "$UBUNTU_CODENAME" = "noble" ]; then
  echo "Detected $UBUNTU_CODENAME — installing full modern Docker packages"

  # Add Docker's official GPG key:
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc

  # Add the repository to Apt sources:
  tee /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Signed-By: /etc/apt/keyrings/docker.asc
EOF

  apt update
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
else
  echo "Detected $UBUNTU_CODENAME — installing full modern Docker packages"

  # Keyring
  rm -rf /etc/apt/keyrings/docker.asc
  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg | gpg --dearmor -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc

  # Dynamic Ubuntu codename
  export UBUNTU_CODENAME=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")

  # Docker repo
  echo "deb [arch=$(dpkg --print-architecture) trusted=yes] \
  https://download.docker.com/linux/ubuntu $UBUNTU_CODENAME stable" \
  | tee /etc/apt/sources.list.d/docker.list > /dev/null

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
fi

#cd /tmp
#wget https://launchpad.net/~canonical-kernel-team/+archive/ubuntu/ppa/+build/14960883/+files/linux-image-4.13.0-1019-gcp_4.13.0-1019.23_amd64.deb
#dpkg -i linux-image-4.13.0-1019-gcp_4.13.0-1019.23_amd64.deb

cd /usr/local
# adjust version as you like; 1.20+ is fine for Flynn
wget https://go.dev/dl/go1.21.13.linux-amd64.tar.gz
rm -rf go
tar -xzf go1.21.13.linux-amd64.tar.gz

export PATH=/usr/local/go/bin:$PATH
go version
go env CGO_ENABLED
CGO_ENABLED=1 go env CGO_ENABLED

mkdir -p /root/.ssh
cp /root/go/src/github.com/flynn/flynn/sshkeys/id_rsa /root/.ssh/id_rsa

cd /root/go/src/github.com/flynn/flynn
GOOS=linux GOARCH=amd64 go build -o sha512_256_binary sha512_256.go
