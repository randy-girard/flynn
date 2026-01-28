apt-get update
add-apt-repository ppa:longsleep/golang-backports -y
apt-get install -y ca-certificates curl gcc cloud-guest-utils lvm2 gh

growpart /dev/sda 3
pvresize /dev/sda3
lvextend -l +100%FREE -r /dev/ubuntu-vg/ubuntu-lv

rm -f /etc/apt/sources.list.d/docker.*

# Dynamic Ubuntu codename
export UBUNTU_CODENAME=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")

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


#cd /tmp
#wget https://launchpad.net/~canonical-kernel-team/+archive/ubuntu/ppa/+build/14960883/+files/linux-image-4.13.0-1019-gcp_4.13.0-1019.23_amd64.deb
#dpkg -i linux-image-4.13.0-1019-gcp_4.13.0-1019.23_amd64.deb

cd /usr/local
# adjust version as you like; 1.20+ is fine for Flynn
wget https://go.dev/dl/go1.24.12.linux-amd64.tar.gz
rm -rf go
tar -xzf go1.24.12.linux-amd64.tar.gz

export PATH=/usr/local/go/bin:$PATH
go version
go env CGO_ENABLED
CGO_ENABLED=1 go env CGO_ENABLED

mkdir -p /root/.ssh
cp /root/go/src/github.com/flynn/flynn/sshkeys/id_rsa /root/.ssh/id_rsa

cd /root/go/src/github.com/flynn/flynn
GOOS=linux GOARCH=amd64 go build -o sha512_256_binary sha512_256.go
