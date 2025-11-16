#!/bin/bash
set -e

echo "Step 1: Creating flynn-host service..."
curl -v -X PUT "http://192.0.2.200:1111/services/flynn-host" \
  -H "Content-Type: application/json" \
  -d '{}'

echo ""
echo "Step 2: Calculating instance ID..."
INSTANCE_ID=$(echo -n "http-192.0.2.200:1113" | md5sum | awk '{print $1}')
echo "Instance ID: $INSTANCE_ID"

echo ""
echo "Step 3: Registering flynn-host instance..."
curl -v -X PUT "http://192.0.2.200:1111/services/flynn-host/instances/$INSTANCE_ID" \
  -H "Content-Type: application/json" \
  -d '{
    "id": "'$INSTANCE_ID'",
    "proto": "http",
    "addr": "192.0.2.200:1113",
    "meta": {
      "id": "host0",
      "tag.host_id": "host0"
    }
  }'

echo ""
echo "Step 4: Verifying registration..."
curl http://192.0.2.200:1111/services/flynn-host/instances

echo ""
echo "Step 5: Testing flynn-builder..."
export DISCOVERD=192.0.2.200:1111
export EXTERNAL_IP=192.0.2.200
cd /root/go/src/github.com/flynn/flynn
./script/flynn-builder build --version=dev --verbose 2>&1 &

tail -f /var/log/flynn/host-0/flynn-host.log /tmp/discoverd.log
