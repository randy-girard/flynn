export PATH=/usr/local/go/bin:$PATH
export HOST_UBUNTU=$(lsb_release -cs)
export TUF_ROOT_PASSPHRASE="password"
export TUF_TARGETS_PASSPHRASE="password"
export TUF_SNAPSHOT_PASSPHRASE="password"
export TUF_TIMESTAMP_PASSPHRASE="password"
export PATH="/root/go/src/github.com/flynn/flynn/build/bin:/usr/local/go/bin:$PATH"
export CGO_ENABLED=1
export CLUSTER_DOMAIN=flynn.local
export DISCOVERD=192.0.2.200:1111
export DISCOVERY_SERVER=http://localhost:8180
export EXTERNAL_IP=192.0.2.200
export LISTEN_IP=192.0.2.200
export PORT_0=1111
export DISCOVERD_PEERS=192.0.2.200:1111
export TELEMETRY_URL=http://localhost:8080/measure/scheduler
export FLYNN_REPOSITORY=http://localhost:8080
export SQUASHFS="/var/lib/flynn/base-layer.squashfs"
export JSON_FILE="/root/go/src/github.com/flynn/flynn/builder/manifest.json"
export UBUNTU_CODENAME=$(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")

echo "GO VERSION"
echo "$(go version)"

./script/stop-all
./script/install-flynn --remove --clean --yes

echo 'Acquire::ForceIPv4 "true";' | sudo tee /etc/apt/apt.conf.d/99force-ipv4

ssh -o StrictHostKeyChecking=no root@10.0.0.211 "rm -rf /root/go-tuf/repo/*"

mkdir -p /var/lib/flynn/base-root
debootstrap \
  --variant=minbase \
  --include=squashfs-tools,curl,gnupg,ca-certificates,bash \
  $UBUNTU_CODENAME \
  /var/lib/flynn/base-root \
  http://mirror.math.princeton.edu/pub/ubuntu
mksquashfs /var/lib/flynn/base-root "$SQUASHFS" -noappend

export SIZE=$(stat -c%s "$SQUASHFS")
export HASH=$(./sha512_256_binary "$SQUASHFS")

echo "SIZE=$SIZE"
echo "HASH=$HASH"

# Update JSON file using jq
jq --arg url "file://$SQUASHFS" \
   --arg size "$SIZE" \
   --arg hash "$HASH" \
   '.base_layer.url = $url |
    .base_layer.size = ($size | tonumber) |
    .base_layer.hashes.sha512_256 = $hash' \
   "$JSON_FILE" > "${JSON_FILE}.tmp" && mv "${JSON_FILE}.tmp" "$JSON_FILE"

cd /root/go/src/github.com/flynn/go-tuf/ && \
docker compose down && \
rm -rf repo && \
docker compose up -d --build

# Whenever the keys expire, you have to run this
# script again, and then clean and build flynn
./update_keys_in_flynn.sh

scp -o StrictHostKeyChecking=no -r ./repo/* root@10.0.0.211:/root/go-tuf/repo/

cd /root/go/src/github.com/flynn/flynn-discovery && \
docker compose down && \
docker compose up -d --build

cd /root/go/src/github.com/flynn/flynn && \
mkdir -p /etc/flynn && \
mkdir -p /tmp/discoverd-data

#./script/clean-flynn
#./script/build-flynn
#./script/flynn-builder build --version=dev --verbose
#./build/bin/flynn-builder build

# Copy keys from go-tuf repo.

rm -rf /tmp/flynn-* && \
rm -rf /var/log/flynn/* && \
make clean && \
make && \
rm -f build/bin/flynn-builder && \
rm -f build/bin/flannel-wrapper && \
go build -o build/bin/flannel-wrapper ./flannel/wrapper && \
export DISCOVERY_URL=`./build/bin/flynn-host init --init-discovery` && \
./script/start-all && \
zfs set sync=disabled flynn-default && \
zfs set reservation=512M flynn-default && \
zfs set refreservation=512M flynn-default && \
rm -rf /etc/flynn/tuf.db && \
./script/flynn-builder build --version=dev --tuf-db=/etc/flynn/tuf.db --verbose && \
./script/export-components --host host0 /root/go/src/github.com/flynn/flynn/go-tuf/repo && \
  flynn-host ps -a && \
  cd /root/go/src/github.com/flynn/flynn && \
  scp -o StrictHostKeyChecking=no -r /root/go/src/github.com/flynn/flynn/go-tuf/repo/repository/ root@10.0.0.211:/root/go-tuf/repo/ && \
  cp ./script/install-flynn /usr/bin/install-flynn && \
  scp -o StrictHostKeyChecking=no /usr/bin/install-flynn root@10.0.0.211:/root/go-tuf/repo/install-flynn

# sudo bash -s -- --remove --yes < <(curl -fsSL https://dl.flynn.cloud.randygirard.com/install-flynn)
# sudo bash -s -- --version dev < <(curl -fsSL  https://dl.flynn.cloud.randygirard.com/install-flynn)
# /usr/bin/install-flynn -r https://dl.flynn.cloud.randygirard.com --version dev
# sudo flynn-host init --discovery https://discovery.flynn.io/clusters/53e8402e-030f-4861-95ba-d5b5a91b5902
# sudo systemctl start flynn-host
# sudo systemctl status flynn-host
# Setup dns
# sudo \
#   CLUSTER_DOMAIN=demo.flynn.cloud.randygirard.com \
#   flynn-host bootstrap \
#   --min-hosts 3 \
#    --discovery https://discovery.flynn.io/clusters/53e8402e-030f-4861-95ba-d5b5a91b5902
#   CLUSTER_DOMAIN=heztner.flynn.cloud.randygirard.com flynn-host bootstrap --min-hosts 1

#
# Notes:
# - Make sure firewall/ports are set up properly or not running
