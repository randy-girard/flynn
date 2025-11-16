flanneld \
  -discoverd-url="http://${DISCOVERD}" \
  -iface="${EXTERNAL_IP}" \
  -http-port="5001" \
  -notify-url="http://${EXTERNAL_IP}:1113/host/network" \
  -logtostderr \
  > /tmp/flanneld.log 2>&1 &
