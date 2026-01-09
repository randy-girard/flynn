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

make

./script/start-all

./script/flynn-builder build --version=dev --tuf-db=/etc/flynn/tuf.db --verbose

./script/export-components --host host0 /root/go/src/github.com/flynn/flynn/go-tuf/repo

./script/stop-all

cd /root/go/src/github.com/flynn/flynn-discovery
docker compose down
cd /root/go/src/github.com/flynn/flynn

scp -o StrictHostKeyChecking=no -r /root/go/src/github.com/flynn/flynn/go-tuf/repo/repository/ root@10.0.0.211:/root/go-tuf/repo/

cp ./script/install-flynn /usr/bin/install-flynn

scp -o StrictHostKeyChecking=no /usr/bin/install-flynn root@10.0.0.211:/root/go-tuf/repo/install-flynn

# sudo bash -s -- --version dev < <(curl -fsSL  https://dl.flynn.cloud.randygirard.com/install-flynn)
# /usr/bin/install-flynn -r https://dl.flynn.cloud.randygirard.com --version dev
# sudo flynn-host init --discovery https://discovery.flynn.io/clusters/53e8402e-030f-4861-95ba-d5b5a91b5902
# sudo systemctl start flynn-host
# sudo systemctl status flynn-host
# Setup dns
# sudo \
#   CLUSTER_DOMAIN=demo.localflynn.com \
#   flynn-host bootstrap \
#   --min-hosts 3 \
#    --discovery https://discovery.flynn.io/clusters/53e8402e-030f-4861-95ba-d5b5a91b5902
