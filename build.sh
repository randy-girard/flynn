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

export SQUASHFS="/var/lib/flynn/base-layer.squashfs"
export JSON_FILE="/root/go/src/github.com/flynn/flynn/builder/manifest.json"

mkdir -p /var/lib/flynn/base-root
debootstrap \
  --variant=minbase \
  --include=squashfs-tools,curl,gnupg,ca-certificates,bash \
  focal \
  /var/lib/flynn/base-root \
  http://archive.ubuntu.com/ubuntu
mkdir -p /var/lib/flynn
mksquashfs /var/lib/flynn/base-root "$SQUASHFS" -noappend
export SIZE=$(stat -c%s "$SQUASHFS")
export HASH=$(openssl dgst -sha512-256 "$SQUASHFS" | awk '{print $2}')

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

mkdir -p /root/.ssh
cp /root/go/src/github.com/flynn/flynn/sshkeys/id_rsa /root/.ssh/id_rsa

cd /root/go/src/github.com/flynn/go-tuf/
docker compose down
rm -rf repo
docker compose up --build -d

# Whenever the keys expire, you have to run this
# script again, and then clean and build flynn
./update_keys_in_flynn.sh

scp -o StrictHostKeyChecking=no -r ./repo/* root@10.0.0.211:/root/go-tuf/repo/

cd /root/go/src/github.com/flynn/flynn-discovery
docker compose up --build -d

cd /root/go/src/github.com/flynn/flynn
mkdir -p /etc/flynn
mkdir -p /tmp/discoverd-data

#./script/clean-flynn
#./script/build-flynn
#./script/flynn-builder build --version=dev --verbose
#./build/bin/flynn-builder build

# Copy keys from go-tuf repo.

make clean
make

rm build/bin/flynn-builder
rm build/bin/flannel-wrapper

go build -o build/bin/flannel-wrapper ./flannel/wrapper

export DISCOVERY_URL=`./build/bin/flynn-host init --init-discovery`

./script/start-all

zfs set sync=disabled flynn-default
zfs set reservation=512M flynn-default
zfs set refreservation=512M flynn-default

./script/flynn-builder build --version=dev --tuf-db=/etc/flynn/tuf.db --verbose

./script/export-components --host host0 /root/go/src/github.com/flynn/flynn/go-tuf/repo

./script/stop-all

cd /root/go/src/github.com/flynn/flynn-discovery
docker compose down
cd /root/go/src/github.com/flynn/flynn

scp -o StrictHostKeyChecking=no -r /root/go/src/github.com/flynn/flynn/go-tuf/repo/repository/ root@10.0.0.211:/root/go-tuf/repo/

cp ./script/install-flynn /usr/bin/install-flynn

scp -o StrictHostKeyChecking=no /usr/bin/install-flynn root@10.0.0.211:/root/go-tuf/repo/install-flynn

#sudo bash < <(curl -fsSL  https://dl.flynn.cloud.randygirard.com/install-flynn)
#/usr/bin/install-flynn -r https://dl.flynn.cloud.randygirard.com --version dev
