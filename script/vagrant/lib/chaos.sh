# shellcheck shell=bash
# monitor and chaos for script/vagrant.sh (laptop loop). Health samples run
# guest/check-health.sh on a running cluster node; faults run
# guest/chaos-fault.sh (job kills) or act on a node (flynn-host restart, hard
# power-off and boot). Needs run_as_root_on and start_one_cluster_node from
# vagrant.sh.

CHAOS_ALL_FAULTS="system-job plugin-job datastore-primary datastore-replica tenant-job host-restart node-reboot"
CHAOS_HEALTH_OUT=""

# chaos_check_node prints a cluster node other than $1 to sample from.
# vagrant status takes a global lock, so during halt/up every machine looks
# unknown and the running list is empty. Fall back to the pinned node list
# so the sample still runs on a host that is not the one being rebooted.
chaos_check_node() {
  local avoid="${1:-}" name
  while IFS= read -r name; do
    if [[ -n "${name}" && "${name}" != "${avoid}" ]]; then
      echo "${name}"
      return 0
    fi
  done < <(flynn_vagrant_running_cluster_nodes)
  while IFS= read -r name; do
    if [[ -n "${name}" && "${name}" != "${avoid}" ]]; then
      echo "${name}"
      return 0
    fi
  done < <(flynn_vagrant_cluster_nodes)
  return 1
}

# chaos_exec_on runs a root command on a node. Direct SSH uses the host-only
# address and the Vagrant private key so a health sample is not blocked by
# the vagrant lock held by halt or up of another node.
chaos_exec_on() {
  local vm="$1" cmd="$2"
  local key="" ip
  if [[ -n "${VAGRANT_DOTFILE_PATH:-}" ]]; then
    key="${VAGRANT_DOTFILE_PATH}/machines/${vm}/virtualbox/private_key"
  fi
  if [[ -n "${key}" && -f "${key}" ]]; then
    ip="$(flynn_vagrant_node_ip "${vm}")"
    ssh -i "${key}" \
      -o StrictHostKeyChecking=no \
      -o UserKnownHostsFile=/dev/null \
      -o ConnectTimeout=8 \
      -o BatchMode=yes \
      "vagrant@${ip}" "sudo -n bash -lc $(printf '%q' "${cmd}")"
    return
  fi
  run_as_root_on "${vm}" "${cmd}"
}

# chaos_health takes one health sample into CHAOS_HEALTH_OUT.
# The tenants section gets ROUTER_IPS, the running nodes other than $1: an
# app with no route on every one of them (deleted after the tenant list was
# taken) is dropped in that sample instead of failing it (EDGE-99).
chaos_health() {
  local node ip st=0 name routers=""
  if declare -F flynn_vagrant_pin_nodes >/dev/null; then
    flynn_vagrant_pin_nodes
  fi
  if ! node="$(chaos_check_node "${1:-}")"; then
    CHAOS_HEALTH_OUT="FAIL nodes: no running cluster node to check from"
    return 1
  fi
  ip="$(flynn_vagrant_node_ip "${node}")"
  while IFS= read -r name; do
    if [[ -n "${name}" && "${name}" != "${1:-}" ]]; then
      routers+="${routers:+ }$(flynn_vagrant_node_ip "${name}")"
    fi
  done < <(flynn_vagrant_running_cluster_nodes)
  set +e
  CHAOS_HEALTH_OUT="$(chaos_exec_on "${node}" "cd ${SRC} && CLUSTER_DOMAIN=$(flynn_vagrant_cluster_domain) CLUSTER_IP=${ip} MIN_HOSTS=$(flynn_vagrant_get_nodes) BUILDER_IP=${FLYNN_DEV_IP:-192.168.57.10} TENANT_APPS=$(printf '%q' "${CHAOS_TENANT_APPS:-}") ROUTER_IPS=$(printf '%q' "${routers}") EXPECT_RESOURCES=$(printf '%q' "${CHAOS_EXPECT_RESOURCES:-}") script/vagrant/guest/check-health.sh" 2>&1 | grep -v '^Connection to .* closed' | grep -v '^Warning: Permanently added')"
  st=$?
  set -e
  if [[ "${st}" -ne 0 ]]; then
    if ! grep -q '^FAIL ' <<<"${CHAOS_HEALTH_OUT}"; then
      CHAOS_HEALTH_OUT="FAIL monitor: health check on ${node} exited ${st} without a result: $(printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep -v '^[[:space:]]*$' | tail -2 | paste -sd ';' - | cut -c1-200 || true)"
    fi
  fi
  if ! chaos_bridge_check "${1:-}"; then
    st=1
  fi
  [[ "${st}" -eq 0 ]]
}

# chaos_bridge_check runs guest/bridge-ips.py on every running cluster node
# except $1 and appends "ok bridge <node>: ..." or "FAIL bridge <node>: ..."
# to CHAOS_HEALTH_OUT. It fails when an address on a node's flynnbr0 answers
# ARP from two MACs (an IP reused while the old job's veth is still on the
# bridge). An unreachable node is skipped: the cluster section reports
# missing hosts. CHAOS_BRIDGE_CHECK=0 turns the check off.
chaos_bridge_check() {
  local avoid="${1:-}" name out st failed=0
  if [[ "${CHAOS_BRIDGE_CHECK:-1}" == 0 ]]; then
    return 0
  fi
  while IFS= read -r name; do
    if [[ -z "${name}" || "${name}" == "${avoid}" ]]; then
      continue
    fi
    set +e
    out="$(chaos_exec_on "${name}" "timeout 20 python3 ${SRC}/script/vagrant/guest/bridge-ips.py" < /dev/null 2>&1 | grep -v '^Connection to .* closed' | grep -v '^Warning: Permanently added')"
    st=$?
    set -e
    if grep -q '^duplicate ' <<<"${out}"; then
      CHAOS_HEALTH_OUT+=$'\n'"FAIL bridge ${name}: $(grep '^duplicate ' <<<"${out}" | paste -sd ';' - | cut -c1-300)"
      failed=1
    elif [[ "${st}" -eq 0 ]]; then
      CHAOS_HEALTH_OUT+=$'\n'"ok bridge ${name}: $(printf '%s\n' "${out}" | grep -v '^[[:space:]]*$' | tail -1)"
    else
      CHAOS_HEALTH_OUT+=$'\n'"ok bridge ${name}: skipped (exit ${st}: $(printf '%s\n' "${out}" | grep -v '^[[:space:]]*$' | tail -1 | cut -c1-120))"
    fi
  done < <(flynn_vagrant_running_cluster_nodes)
  [[ "${failed}" -eq 0 ]]
}

chaos_now() { date -u +%H:%M:%S; }

# monitor_cluster prints one health sample every MONITOR_INTERVAL seconds
# (default 30) until Ctrl-C, or MONITOR_COUNT samples. Exit status is the
# last sample's.
monitor_cluster() {
  require_cluster_nodes
  local interval="${MONITOR_INTERVAL:-30}" count="${MONITOR_COUNT:-0}" i=0 st=0
  CHAOS_TENANT_APPS="$(list_tenant_apps 2>/dev/null | tr '\n' ' ' | sed 's/[[:space:]]*$//' || true)"
  echo "monitoring every ${interval}s (tenant apps: ${CHAOS_TENANT_APPS:-none}); Ctrl-C to stop"
  while :; do
    i=$((i + 1))
    st=0
    chaos_health || st=$?
    if [[ "${st}" -eq 0 ]]; then
      echo "$(chaos_now) healthy | $(printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep '^ok ' | sed 's/^ok //' | paste -sd '|' -)"
    else
      echo "$(chaos_now) UNHEALTHY"
      printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep -E '^(FAIL|ok) ' | sed 's/^/  /'
    fi
    if [[ "${count}" -gt 0 && "${i}" -ge "${count}" ]]; then
      return "${st}"
    fi
    sleep "${interval}"
  done
}

# chaos_wait_healthy samples every 10s until healthy or $2 seconds pass.
chaos_wait_healthy() {
  local label="$1" timeout="$2" avoid="${3:-}" start=${SECONDS}
  while :; do
    if chaos_health "${avoid}"; then
      return 0
    fi
    if (( SECONDS - start >= timeout )); then
      echo "$(chaos_now) ${label}: not healthy after ${timeout}s" >&2
      printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep -E '^(FAIL|ok) ' | sed 's/^/  /' >&2
      return 1
    fi
    echo "$(chaos_now) ${label}: waiting ($((SECONDS - start))s) $(printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep '^FAIL ' | head -1 | cut -c1-160)"
    sleep 10
  done
}

# chaos_inject applies one fault and sets CHAOS_TARGET. Exit 3 = nothing to hit.
# A host-restart or node-reboot writes the node to $CHAOS_RUN_DIR/avoid so the
# background monitor samples from another node until the round ends.
chaos_inject() {
  local kind="$1" seed="$2" nodes=() name node out st=0 attempt
  while IFS= read -r name; do
    [[ -n "${name}" ]] && nodes+=("${name}")
  done < <(flynn_vagrant_running_cluster_nodes)
  node="${nodes[$((RANDOM % ${#nodes[@]}))]}"
  case "${kind}" in
    host-restart|node-reboot)
      if [[ -n "${CHAOS_RUN_DIR:-}" ]]; then
        echo "${node}" > "${CHAOS_RUN_DIR}/avoid"
      fi
      ;;
  esac
  case "${kind}" in
    host-restart)
      CHAOS_TARGET="node=${node} systemctl restart flynn-host"
      run_as_root_on "${node}" "systemctl restart flynn-host.service"
      ;;
    node-reboot)
      CHAOS_TARGET="node=${node} power off ${CHAOS_NODE_DOWN:-60}s, boot, start flynn-host"
      echo "$(chaos_now) powering off ${node}"
      vagrant halt --force "${node}"
      sleep "${CHAOS_NODE_DOWN:-60}"
      flynn_vagrant_up_one "${node}"
      start_one_cluster_node "${node}" || true
      ;;
    *)
      # A vagrant ssh that fails before the guest script prints a target
      # (for example right after a node boots) is retried; the fault itself
      # has not run yet.
      for attempt in 1 2 3; do
        set +e
        out="$(run_as_root_on "${nodes[0]}" "cd ${SRC} && FAULT=${kind} SEED=${seed} APP_MATCH='${CHAOS_APP:-}' script/vagrant/guest/chaos-fault.sh" 2>&1)"
        st=$?
        set -e
        CHAOS_TARGET="$(printf '%s\n' "${out}" | sed -n 's/^target //p' | tail -1)"
        if [[ "${st}" -eq 0 || "${st}" -eq 3 || -n "${CHAOS_TARGET}" ]]; then
          break
        fi
        sleep 10
      done
      if [[ "${st}" -ne 0 ]]; then
        if [[ -n "${CHAOS_RUN_DIR:-}" ]]; then
          printf '%s\n' "${out}" > "${CHAOS_RUN_DIR}/inject-${kind}.log"
        fi
        [[ -n "${CHAOS_TARGET}" ]] || CHAOS_TARGET="$(printf '%s\n' "${out}" | grep -v '^[[:space:]]*$' | tail -3 | paste -sd ';' -)"
        return "${st}"
      fi
      ;;
  esac
}

# chaos_monitor_loop runs in the background for the whole chaos run. Every
# CHAOS_MONITOR_INTERVAL seconds (default 15) it appends one health sample to
# $1/samples.tsv: epoch, UTC time, phase (from $1/phase), healthy|UNHEALTHY,
# failing sections, and the FAIL lines joined by " ;; ". It does not sample
# from the node named in $1/avoid, whose local discoverd and router are down.
chaos_monitor_loop() {
  local dir="$1" interval="${CHAOS_MONITOR_INTERVAL:-15}" phase status sections fails
  while [[ -f "${dir}/phase" ]]; do
    phase="$(cat "${dir}/phase" 2>/dev/null || true)"
    if chaos_health "$(cat "${dir}/avoid" 2>/dev/null || true)"; then status=healthy; else status=UNHEALTHY; fi
    sections="$(printf '%s\n' "${CHAOS_HEALTH_OUT}" | sed -n 's/^FAIL \([^:]*\):.*/\1/p' | paste -sd ',' - || true)"
    fails="$(printf '%s\n' "${CHAOS_HEALTH_OUT}" | sed -n 's/^FAIL //p' | tr '\t' ' ' | awk 'NR > 1 { printf " ;; " } { printf "%s", $0 }' || true)"
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$(date -u +%s)" "$(chaos_now)" "${phase}" "${status}" "${sections:--}" "${fails:--}" >> "${dir}/samples.tsv"
    sleep "${interval}"
  done
}

chaos_stop_monitor() {
  if [[ -n "${CHAOS_MONITOR_PID:-}" ]]; then
    rm -f "${CHAOS_RUN_DIR}/phase"
    kill "${CHAOS_MONITOR_PID}" 2>/dev/null || true
    wait "${CHAOS_MONITOR_PID}" 2>/dev/null || true
    CHAOS_MONITOR_PID=""
  fi
  chaos_stop_ingress
}

CHAOS_LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# chaos_start_ingress starts host/ingress-probe.sh in the background for the
# run in CHAOS_RUN_DIR: every second, one request per target to every node
# IP (CHAOS_INGRESS_NODES, default every pinned cluster node, the same list
# the health check uses), CHAOS_INGRESS_TIMEOUT seconds each (default 1.5),
# appended to ingress.tsv. The targets are the router itself, the
# controller, and each tenant app in CHAOS_INGRESS_APPS (chaos_cluster sets
# it to the apps that answered at baseline). CHAOS_INGRESS=0 turns it off.
chaos_start_ingress() {
  local dir="${CHAOS_RUN_DIR}" name ip pairs=() targets="router controller" app
  CHAOS_INGRESS_PID=""
  CHAOS_INGRESS_NODE_LIST=""
  if [[ "${CHAOS_INGRESS:-1}" == 0 ]]; then
    echo "ingress probe: off (CHAOS_INGRESS=0)"
    return 0
  fi
  if ! declare -F flynn_vagrant_cluster_nodes >/dev/null; then
    echo "ingress probe: off (no cluster node list)"
    return 0
  fi
  while IFS= read -r name; do
    [[ -n "${name}" ]] || continue
    ip="$(flynn_vagrant_node_ip "${name}")" || continue
    pairs+=("${name}=${ip}")
    CHAOS_INGRESS_NODE_LIST+="${CHAOS_INGRESS_NODE_LIST:+ }${name}"
  done < <(if [[ -n "${CHAOS_INGRESS_NODES:-}" ]]; then printf '%s\n' ${CHAOS_INGRESS_NODES}; else flynn_vagrant_cluster_nodes; fi)
  if [[ "${#pairs[@]}" -eq 0 ]]; then
    echo "ingress probe: off (no nodes)"
    return 0
  fi
  for app in ${CHAOS_INGRESS_APPS:-}; do
    targets+=" app:${app}"
  done
  CHAOS_INGRESS_TARGETS="${targets}"
  touch "${dir}/ingress.tsv" "${dir}/ingress.on"
  INGRESS_TARGETS="${targets}" INGRESS_TIMEOUT="${CHAOS_INGRESS_TIMEOUT:-1.5}" \
    bash "${CHAOS_LIB_DIR}/../host/ingress-probe.sh" "${dir}" "$(flynn_vagrant_cluster_domain)" "${pairs[@]}" \
    < /dev/null >> "${dir}/ingress.log" 2>&1 &
  CHAOS_INGRESS_PID=$!
  echo "ingress probe: every 1s, ${targets} on ${CHAOS_INGRESS_NODE_LIST} (${dir}/ingress.tsv)"
}

# chaos_stop_ingress removes the flag file and waits for the prober to
# finish the requests in flight (a few seconds at most), then kills it if it
# is still there, so no prober or curl outlives the run.
chaos_stop_ingress() {
  local pid="${CHAOS_INGRESS_PID:-}" i
  [[ -n "${pid}" ]] || return 0
  rm -f "${CHAOS_RUN_DIR}/ingress.on"
  for ((i = 0; i < 50; i++)); do
    kill -0 "${pid}" 2>/dev/null || break
    /bin/sleep 0.1
  done
  if kill -0 "${pid}" 2>/dev/null; then
    kill -TERM "${pid}" 2>/dev/null || true
  fi
  wait "${pid}" 2>/dev/null || true
  CHAOS_INGRESS_PID=""
}

# chaos_write_html renders $1/report.html, a self-contained page with the
# same data as report.txt plus a per-sample health timeline.
chaos_write_html() {
  local dir="$1" seed="$2" rounds="$3" order="$4" started="$5" verdict="$6" finished="$7"
  {
    cat <<'EOF'
<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Flynn chaos report</title>
<style>
:root{--ok:#1f9d55;--okbg:#e3f6ea;--bad:#d64545;--badbg:#fde8e8;--warn:#b7791f;--warnbg:#fdf3dc;--mute:#6b7280;--line:#e5e7eb;--ink:#111827}
*{box-sizing:border-box}
body{margin:0;font:14px/1.5 -apple-system,BlinkMacSystemFont,"Segoe UI",Helvetica,Arial,sans-serif;color:var(--ink);background:#f6f7f9}
main{max-width:1100px;margin:0 auto;padding:32px 24px 48px}
h1{font-size:24px;margin:0 0 4px}
h2{font-size:16px;margin:32px 0 12px}
.meta{color:var(--mute);margin:0 0 20px}
.meta code{color:var(--ink)}
code{font:12px/1.4 ui-monospace,SFMono-Regular,Menlo,monospace;background:#eef0f3;padding:1px 5px;border-radius:4px}
.verdict{display:inline-block;font-weight:600;padding:6px 14px;border-radius:999px;margin:8px 0 4px}
.verdict.pass{background:var(--okbg);color:var(--ok)}
.verdict.fail{background:var(--badbg);color:var(--bad)}
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:12px}
.card{background:#fff;border:1px solid var(--line);border-radius:10px;padding:14px 16px}
.card .k{color:var(--mute);font-size:12px;text-transform:uppercase;letter-spacing:.04em}
.card .v{font-size:22px;font-weight:600;margin-top:2px}
.card .s{color:var(--mute);font-size:12px}
table{width:100%;border-collapse:collapse;background:#fff;border:1px solid var(--line);border-radius:10px;overflow:hidden}
th,td{text-align:left;padding:8px 12px;border-bottom:1px solid var(--line);vertical-align:top}
th{background:#f9fafb;font-size:12px;color:var(--mute);text-transform:uppercase;letter-spacing:.04em}
tr:last-child td{border-bottom:0}
td.num{text-align:right;font-variant-numeric:tabular-nums;white-space:nowrap}
.badge{display:inline-block;padding:1px 9px;border-radius:999px;font-size:12px;font-weight:600}
.badge.ok{background:var(--okbg);color:var(--ok)}
.badge.bad{background:var(--badbg);color:var(--bad)}
.badge.skip{background:var(--warnbg);color:var(--warn)}
.tag{display:inline-block;background:#eef0f3;border-radius:4px;padding:0 6px;margin:0 4px 2px 0;font-size:12px}
.timeline{background:#fff;border:1px solid var(--line);border-radius:10px;padding:14px 16px;overflow-x:auto}
.tl-row{display:flex;align-items:center;gap:0;margin:2px 0}
.tl-label{flex:0 0 90px;font-size:12px;color:var(--mute);position:sticky;left:0;background:#fff}
.tl-cells{display:flex;gap:2px}
.c{width:12px;height:18px;border-radius:2px;background:var(--okbg);border:1px solid #c6ecd4}
.c.f{background:var(--bad);border-color:var(--bad)}
.c.p0{background:#eef0f3;border-color:#eef0f3}
.c.p1{background:#c7d2fe;border-color:#c7d2fe}
.c.p2{background:#a5b4fc;border-color:#a5b4fc}
.legend{color:var(--mute);font-size:12px;margin-top:8px}
.legend span{display:inline-block;width:10px;height:10px;border-radius:2px;margin:0 4px 0 12px;vertical-align:-1px}
.msg{font:12px/1.4 ui-monospace,SFMono-Regular,Menlo,monospace;word-break:break-word}
footer{color:var(--mute);font-size:12px;margin-top:32px}
</style></head><body><main>
EOF
    awk -F'\t' -v seed="${seed}" -v rounds="${rounds}" -v order="${order}" -v started="${started}" \
      -v finished="${finished}" -v verdict="${verdict}" -v nodes="$(flynn_vagrant_get_nodes)" \
      -v tenants="${CHAOS_TENANT_APPS:-none}" -v interval="${CHAOS_MONITOR_INTERVAL:-15}" \
      -v settle="${CHAOS_SETTLE_SUMMARY:-not run}" '
      function esc(s) { gsub(/&/, "\\&amp;", s); gsub(/</, "\\&lt;", s); gsub(/>/, "\\&gt;", s); gsub(/"/, "\\&quot;", s); return s }
      FILENAME == ARGV[1] {
        total++; t[total] = $2; ph[total] = $3; st[total] = $4; fl[total] = $6
        if ($4 == "healthy") ok++
        n = split($5, s, ","); delete now
        for (k = 1; k <= n; k++) if (s[k] != "-") {
          now[s[k]] = 1; bad[total, s[k]] = 1
          if (!(s[k] in fails)) { corder[++m] = s[k]; first[s[k]] = $2 }
          fails[s[k]]++; if (!run[s[k]]) outages[s[k]]++; run[s[k]]++
          if (run[s[k]] > longest[s[k]]) longest[s[k]] = run[s[k]]
        }
        for (c in run) if (!(c in now)) run[c] = 0
        if ($3 ~ /^round /) { split($3, p, " "); r = p[2]; for (k = 1; k <= n; k++) if (s[k] != "-" && !rs[r, s[k]]++) secs[r] = secs[r] "<span class=\"tag\">" esc(s[k]) "</span>" }
        if ($6 != "-") { c2 = split($6, f, " ;; "); for (k = 1; k <= c2; k++) { msg = substr(f[k], 1, 300); if (!(msg in msgs)) morder[++mm] = msg; msgs[msg]++ } }
        next
      }
      {
        nr++; rr[nr] = $1; rk[nr] = $2; rt[nr] = $3; rres[nr] = $6; rrec[nr] = $7; rset[nr] = ($8 != "" ? $8 : "-")
        if ($6 == "recovered") { rec++; v = $7; sub(/s$/, "", v); if (v + 0 > maxrec) { maxrec = v + 0; maxround = $1 } }
        else if ($6 == "skipped") skp++
        else failed++
      }
      END {
        pass = (verdict ~ /^PASSED/)
        print "<h1>Flynn chaos report</h1>"
        printf "<div class=\"verdict %s\">%s</div>\n", (pass ? "pass" : "fail"), esc(verdict)
        printf "<p class=\"meta\">final full-strength gate: %s</p>\n", esc(settle)
        printf "<p class=\"meta\">%s &rarr; %s &middot; seed <code>%s</code> (<code>CHAOS_SEED=%s</code> repeats this run) &middot; %s nodes &middot; tenant apps: %s<br>fault order: %s</p>\n", esc(started), esc(finished), esc(seed), esc(seed), esc(nodes), esc(tenants), esc(order)
        print "<div class=\"cards\">"
        printf "<div class=\"card\"><div class=\"k\">Rounds</div><div class=\"v\">%d / %d</div><div class=\"s\">recovered of %s planned</div></div>\n", rec, nr, esc(rounds)
        printf "<div class=\"card\"><div class=\"k\">Skipped / failed</div><div class=\"v\">%d / %d</div><div class=\"s\">rounds</div></div>\n", skp, failed
        printf "<div class=\"card\"><div class=\"k\">Slowest recovery</div><div class=\"v\">%s</div><div class=\"s\">%s</div></div>\n", (rec ? maxrec "s" : "-"), (rec ? "round " maxround : "no recovered rounds")
        printf "<div class=\"card\"><div class=\"k\">Healthy samples</div><div class=\"v\">%d%%</div><div class=\"s\">%d of %d, every %ss</div></div>\n", (total ? int(ok * 100 / total) : 0), ok, total, esc(interval)
        print "</div>"

        print "<h2>Rounds</h2><table><tr><th>Round</th><th>Fault</th><th>Result</th><th>Recovery</th><th>Settle</th><th>Checks failed</th><th>Target</th></tr>"
        for (i = 1; i <= nr; i++) {
          cls = (rres[i] == "recovered" ? "ok" : (rres[i] == "skipped" ? "skip" : "bad"))
          printf "<tr><td class=\"num\">%s</td><td>%s</td><td><span class=\"badge %s\">%s</span></td><td class=\"num\">%s</td><td class=\"num\">%s</td><td>%s</td><td class=\"msg\">%s</td></tr>\n", esc(rr[i]), esc(rk[i]), cls, esc(rres[i]), esc(rrec[i]), esc(rset[i]), (secs[rr[i]] ? secs[rr[i]] : "-"), esc(rt[i])
        }
        print "</table>"

        print "<h2>Health timeline</h2><div class=\"timeline\">"
        printf "<div class=\"tl-row\"><div class=\"tl-label\">phase</div><div class=\"tl-cells\">"
        for (i = 1; i <= total; i++) {
          pc = "p0"; if (ph[i] ~ /^round /) { split(ph[i], p, " "); pc = (p[2] % 2 ? "p1" : "p2") }
          printf "<div class=\"c %s\" title=\"%s %s\"></div>", pc, esc(t[i]), esc(ph[i])
        }
        print "</div></div>"
        printf "<div class=\"tl-row\"><div class=\"tl-label\">overall</div><div class=\"tl-cells\">"
        for (i = 1; i <= total; i++) printf "<div class=\"c%s\" title=\"%s %s: %s\"></div>", (st[i] == "healthy" ? "" : " f"), esc(t[i]), esc(ph[i]), esc(st[i])
        print "</div></div>"
        for (k = 1; k <= m; k++) {
          c = corder[k]
          printf "<div class=\"tl-row\"><div class=\"tl-label\">%s</div><div class=\"tl-cells\">", esc(c)
          for (i = 1; i <= total; i++) {
            tip = t[i] " " ph[i] ": " ((i, c) in bad ? "FAIL" : "ok")
            if ((i, c) in bad) { c2 = split(fl[i], f, " ;; "); for (j = 1; j <= c2; j++) if (index(f[j], c ":") == 1) tip = tip "\n" substr(f[j], 1, 300) }
            printf "<div class=\"c%s\" title=\"%s\"></div>", ((i, c) in bad ? " f" : ""), esc(tip)
          }
          print "</div></div>"
        }
        print "<div class=\"legend\">one cell per sample; hover for time and failures<span style=\"background:var(--okbg);border:1px solid #c6ecd4\"></span>passing<span style=\"background:var(--bad)\"></span>failing<span style=\"background:#c7d2fe\"></span>fault round<span style=\"background:#eef0f3\"></span>baseline / between rounds</div></div>"

        print "<h2>Checks</h2>"
        if (m == 0) print "<p>Every check passed in every sample.</p>"
        else {
          print "<table><tr><th>Check</th><th>Failed samples</th><th>Outages</th><th>Longest outage</th><th>First failed</th></tr>"
          for (k = 1; k <= m; k++) { c = corder[k]; printf "<tr><td>%s</td><td class=\"num\">%d / %d</td><td class=\"num\">%d</td><td class=\"num\">~%ds</td><td>%s</td></tr>\n", esc(c), fails[c], total, outages[c], longest[c] * interval, esc(first[c]) }
          print "</table>"
          print "<h2>Failure messages</h2><table><tr><th>Samples</th><th>Message</th></tr>"
          for (k = 1; k <= mm; k++) printf "<tr><td class=\"num\">%d</td><td class=\"msg\">%s</td></tr>\n", msgs[morder[k]], esc(morder[k])
          print "</table>"
        }
      }' "${dir}/samples.tsv" "${dir}/rounds.tsv"
    chaos_ingress_rounds "${dir}" | awk -F'\t' -v timeout="${CHAOS_INGRESS_TIMEOUT:-1.5}" '
      function esc(s) { gsub(/&/, "\\&amp;", s); gsub(/</, "\\&lt;", s); gsub(/>/, "\\&gt;", s); gsub(/"/, "\\&quot;", s); return s }
      function nodefails(s,   n, i, a, out, q) { n = split(s, a, ","); out = ""; for (i = 1; i <= n; i++) { split(a[i], q, "="); if (q[2] !~ /^0\//) out = out "<span class=\"tag\">" esc(a[i]) "</span>" }; return (out != "" ? out : "-") }
      $1 == "all" { all = $0; ap = $3; af = $4; al = $5; alt = $6; an = $8; next }
      { r[++nr] = $0 }
      END {
        print "<h2>Ingress</h2>"
        if (all == "") { print "<p>Not probed (<code>CHAOS_INGRESS=0</code> or no data in <code>ingress.tsv</code>).</p>"; exit }
        gsub(/=/, " ", an); gsub(/,/, ", ", an)
        printf "<div class=\"cards\"><div class=\"card\"><div class=\"k\">Probes</div><div class=\"v\">%d</div><div class=\"s\">every 1s, timeout %ss</div></div>", ap, esc(timeout)
        printf "<div class=\"card\"><div class=\"k\">Cluster-level failures</div><div class=\"v\">%d</div><div class=\"s\">seconds x targets no node served</div></div>", af
        printf "<div class=\"card\"><div class=\"k\">Longest outage</div><div class=\"v\">%ds</div><div class=\"s\">%s</div></div></div>\n", al, esc(alt)
        printf "<p class=\"meta\">A second fails only when no node answers the target as expected (router 404 for an unknown host, controller 200 on <code>/ping</code>, tenant apps 200-499). Per node (failed probes, not part of the verdict): %s</p>\n", esc(an)
        print "<table><tr><th>Round</th><th>Fault</th><th>Probes</th><th>Failed seconds</th><th>Longest</th><th>Failed targets</th><th>Node failures</th></tr>"
        for (i = 1; i <= nr; i++) {
          split(r[i], f, "\t")
          printf "<tr><td class=\"num\">%s</td><td>%s</td><td class=\"num\">%d</td><td class=\"num\"><span class=\"badge %s\">%d</span></td><td class=\"num\">%ds</td><td class=\"msg\">%s</td><td>%s</td></tr>\n", esc(f[1]), esc(f[2]), f[3], (f[4] + 0 ? "bad" : "ok"), f[4], f[5], esc(f[7]), nodefails(f[8])
        }
        print "</table>"
      }'
    echo "<footer>Raw data: <a href=\"samples.tsv\">samples.tsv</a> (one line per health sample), <a href=\"rounds.tsv\">rounds.tsv</a> (one line per fault), <a href=\"ingress.tsv\">ingress.tsv</a> (one line per ingress probe), <a href=\"report.txt\">report.txt</a></footer>"
    echo "</main></body></html>"
  } > "${dir}/report.html"
}

# chaos_write_report renders $1/report.txt and $1/report.html from
# rounds.tsv and samples.tsv.
chaos_write_report() {
  local dir="$1" seed="$2" rounds="$3" order="$4" started="$5" verdict="$6" finished
  finished="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  chaos_write_html "${dir}" "${seed}" "${rounds}" "${order}" "${started}" "${verdict}" "${finished}"
  {
    echo "Flynn chaos report"
    echo "  started   ${started}"
    echo "  finished  ${finished}"
    echo "  result    ${verdict}"
    echo "  settle    ${CHAOS_SETTLE_SUMMARY:-not run}"
    echo "  seed      ${seed} (CHAOS_SEED=${seed} repeats this run)"
    echo "  rounds    ${rounds}, order: ${order}"
    echo "  nodes     $(flynn_vagrant_get_nodes), tenant apps: ${CHAOS_TENANT_APPS:-none}"
    echo
    echo "Rounds"
    printf '  %-5s %-18s %-14s %-9s %-7s %-24s %s\n' ROUND FAULT RESULT RECOVERY SETTLE "CHECKS FAILED" TARGET
    awk -F'\t' '
      FILENAME == ARGV[1] { if ($3 ~ /^round /) { split($3, p, " "); n = p[2]; ns = split($5, s, ","); for (k = 1; k <= ns; k++) if (s[k] != "-" && !seen[n, s[k]]++) secs[n] = secs[n] (secs[n] ? "," : "") s[k] }; next }
      { printf "  %-5s %-18s %-14s %-9s %-7s %-24s %s\n", $1, $2, $6, $7, ($8 != "" ? $8 : "-"), (secs[$1] ? secs[$1] : "-"), $3 }
    ' "${dir}/samples.tsv" "${dir}/rounds.tsv"
    echo
    awk -F'\t' -v interval="${CHAOS_MONITOR_INTERVAL:-15}" '
      { total++; if ($4 == "healthy") ok++
        n = split($5, s, ","); delete now
        for (k = 1; k <= n; k++) if (s[k] != "-") { now[s[k]] = 1; if (!(s[k] in fails)) { order[++m] = s[k]; first[s[k]] = $2 }; fails[s[k]]++; if (!run[s[k]]) outages[s[k]]++; run[s[k]]++; if (run[s[k]] > longest[s[k]]) longest[s[k]] = run[s[k]] }
        for (c in run) if (!(c in now)) run[c] = 0
        if ($6 != "-") { c2 = split($6, f, " ;; "); for (k = 1; k <= c2; k++) { msg = substr(f[k], 1, 150); if (!(msg in msgs)) morder[++mm] = msg; msgs[msg]++ } } }
      END {
        printf "Monitor: %d samples every %ss, %d healthy (%d%%)\n", total, interval, ok, (total ? ok * 100 / total : 0)
        if (m == 0) { print "  every check passed in every sample"; exit }
        printf "  %-10s %-14s %-8s %-18s %s\n", "CHECK", "FAILED", "OUTAGES", "LONGEST", "FIRST FAILED"
        for (k = 1; k <= m; k++) { c = order[k]; printf "  %-10s %-14s %-8d ~%-17s %s\n", c, fails[c] "/" total, outages[c], longest[c] * interval "s", first[c] }
        print ""; print "Failure messages (samples seen)"
        for (k = 1; k <= mm; k++) printf "  %4d  %s\n", msgs[morder[k]], morder[k]
      }' "${dir}/samples.tsv"
    echo
    chaos_ingress_rounds "${dir}" | awk -F'\t' -v timeout="${CHAOS_INGRESS_TIMEOUT:-1.5}" '
      function nodefails(s,   n, i, a, out, q) { n = split(s, a, ","); out = ""; for (i = 1; i <= n; i++) { split(a[i], q, "="); if (q[2] !~ /^0\//) out = out (out != "" ? "," : "") a[i] }; return (out != "" ? out : "-") }
      $1 == "all" { all = $0; ap = $3; af = $4; al = $5; alt = $6; an = $8; next }
      { r[++nr] = $0 }
      END {
        if (all == "") { print "Ingress: not probed (CHAOS_INGRESS=0 or no data in ingress.tsv)"; exit }
        gsub(/=/, " ", an); gsub(/,/, ", ", an)
        printf "Ingress: %d probes, every 1s, timeout %ss; a second fails only when no node answers the target\n", ap, timeout
        printf "  cluster level (no node answered): %d failed probe seconds, longest outage %ds%s\n", af, al, (al ? " (" alt ")" : "")
        printf "  per node (failed probes, not part of the verdict): %s\n", an
        printf "  %-5s %-18s %-7s %-7s %-8s %-24s %s\n", "ROUND", "FAULT", "PROBES", "FAILED", "LONGEST", "FAILED TARGETS", "NODE FAILURES"
        for (i = 1; i <= nr; i++) { split(r[i], f, "\t"); printf "  %-5s %-18s %-7d %-7d %-8s %-24s %s\n", f[1], f[2], f[3], f[4], f[5] "s", f[7], nodefails(f[8]) }
      }'
    echo
    echo "Raw data: ${dir}/samples.tsv, ${dir}/rounds.tsv, ${dir}/ingress.tsv"
    echo "HTML report: ${dir}/report.html"
  } > "${dir}/report.txt"
}

# CHAOS_DEFAULT_BUDGET: killing one tenant job or a datastore replica must not
# cost a single failed tenant ingress sample, and no fault may cost a second
# in which no node serves a probed target (ingress.tsv, cluster level), except
# node-reboot: a powered-off node does not reset its connections, so a router
# holding a pooled connection to a backend there waits up to its 5 s
# dead-peer bound (router/proxy/transport.go) before it retries another
# backend. Measured on the 3-node dev cluster: 0 and 1 failed seconds per
# node-reboot round, 0 for every other fault.
CHAOS_DEFAULT_BUDGET="tenant-job:tenants=0 datastore-replica:tenants=0 system-job:ingress=0 plugin-job:ingress=0 datastore-primary:ingress=0 datastore-replica:ingress=0 tenant-job:ingress=0 host-restart:ingress=0 node-reboot:ingress=10 node-reboot:ingress-longest=5"

# chaos_check_budget prints a FAILED line and returns 1 when CHAOS_BUDGET or
# CHAOS_MAX_SKIPPED cannot be parsed, so a typo fails before the first fault.
chaos_check_budget() {
  local entry
  for entry in ${CHAOS_BUDGET-${CHAOS_DEFAULT_BUDGET}}; do
    if [[ ! "${entry}" =~ ^[a-z-]+:[a-z]+(-[a-z]+)*=[0-9]+$ ]]; then
      echo "FAILED: bad CHAOS_BUDGET entry ${entry} (want <fault>:<check>=<max>)"
      return 1
    fi
  done
  if [[ -n "${CHAOS_MAX_SKIPPED:-}" && ! "${CHAOS_MAX_SKIPPED}" =~ ^[0-9]+$ ]]; then
    echo "FAILED: bad CHAOS_MAX_SKIPPED ${CHAOS_MAX_SKIPPED} (want a number)"
    return 1
  fi
  if [[ ! "${CHAOS_SETTLE_TRIES:-120}" =~ ^[1-9][0-9]*$ ]]; then
    echo "FAILED: bad CHAOS_SETTLE_TRIES ${CHAOS_SETTLE_TRIES} (want a number of tries, at least 1)"
    return 1
  fi
}

# chaos_ingress_rounds summarizes $1/ingress.tsv per round of $1/rounds.tsv
# (a probe belongs to a round when its epoch is in [inject, end]) and once
# for the whole run ("all"). Prints nothing when there is no probe data.
# A probe is ok when the router answers 404 (unknown host), the controller
# 200, a tenant app 200-499. A target fails a second only when no node
# answered it as expected that second (cluster-level availability); probes
# that failed on one node while another answered are counted per node only.
# Columns: round, fault, probes, failed probe seconds (seconds x targets),
# longest run of failed seconds of one target, that target, failed seconds
# per target (t=n,...), failed/all probes per node (node=f/n,...), seconds
# with data.
chaos_ingress_rounds() {
  local dir="$1"
  [[ -s "${dir}/ingress.tsv" ]] || return 0
  awk -F'\t' '
    function iok(t, c) { c += 0; if (t == "router") return c == 404; if (t == "controller") return c == 200; return c >= 200 && c < 500 }
    function stats(r, k, a, b,   e, i, j, t, run, tf, fail, longest, lt, probes, ftl, nodes, c, tot, secs) {
      fail = 0; longest = 0; lt = "-"; probes = 0; ftl = ""; nodes = ""; secs = 0
      for (e = a; e <= b; e++) if (e in n) { probes += n[e]; secs++ }
      for (i = 1; i <= nt; i++) {
        t = tord[i]; run = 0; tf = 0
        for (e = a; e <= b; e++) {
          if (!((e, t) in seen)) continue
          if ((e, t) in good) { run = 0; continue }
          tf++; run++
          if (run > longest) { longest = run; lt = t }
        }
        if (tf) { fail += tf; ftl = ftl (ftl != "" ? "," : "") t "=" tf }
      }
      for (j = 1; j <= nn; j++) {
        c = 0; tot = 0
        for (e = a; e <= b; e++) { c += nf[e, nord[j]]; tot += np[e, nord[j]] }
        nodes = nodes (nodes != "" ? "," : "") nord[j] "=" c "/" tot
      }
      printf "%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%d\n", r, k, probes, fail, longest, lt, (ftl != "" ? ftl : "-"), (nodes != "" ? nodes : "-"), secs
    }
    FILENAME == ARGV[1] {
      if (NF < 5 || $1 !~ /^[0-9]+$/) next
      e = $1 + 0; t = $3
      if (!(t in tseen)) { tord[++nt] = t; tseen[t] = 1 }
      if (!($2 in nseen)) { nord[++nn] = $2; nseen[$2] = 1 }
      n[e]++; np[e, $2]++; seen[e, t] = 1
      if (iok(t, $4)) good[e, t] = 1; else nf[e, $2]++
      if (emin == "" || e < emin) emin = e
      if (e > emax) emax = e
      next
    }
    function isort(a, n,   i, j, v) { for (i = 2; i <= n; i++) { v = a[i]; for (j = i - 1; j >= 1 && a[j] > v; j--) a[j + 1] = a[j]; a[j + 1] = v } }
    nt && !sorted { isort(nord, nn); isort(tord, nt); sorted = 1 }
    nt { stats($1, $2, $4 + 0, $5 + 0) }
    END { if (nt && !sorted) { isort(nord, nn); isort(tord, nt) }; if (nt) stats("all", "-", emin, emax) }
  ' "${dir}/ingress.tsv" "${dir}/rounds.tsv"
}

# chaos_full_strength runs the gate reload and update use,
# wait_cluster_stable <label> CHAOS_SETTLE_TRIES 1 (hosts, controller,
# status, postgres peers healthy, every service at its formation count;
# default 120 tries five seconds apart). A round counts as recovered once the
# health sample passes, but a postgres peer can still be resyncing then.
# Sets CHAOS_SETTLE_SECS to the wait and, when the cluster is not back,
# CHAOS_SETTLE_REASON to the last line the probe printed. The gate's output
# is appended to $CHAOS_RUN_DIR/settle.log.
chaos_full_strength() {
  local label="$1" tries="${CHAOS_SETTLE_TRIES:-120}" start=${SECONDS} out st=0
  out="${CHAOS_RUN_DIR}/settle.out"
  CHAOS_SETTLE_REASON=""
  echo "$(chaos_now) waiting for full strength after ${label} (up to ${tries} tries)"
  { wait_cluster_stable "${label}" "${tries}" 1 < /dev/null 2>&1; } | tee "${out}" || st=$?
  CHAOS_SETTLE_SECS=$((SECONDS - start))
  { echo "== $(chaos_now) ${label}: exit ${st} after ${CHAOS_SETTLE_SECS}s"; cat "${out}"; } >> "${CHAOS_RUN_DIR}/settle.log"
  if [[ "${st}" -ne 0 ]]; then
    # The probe's own failure is the last line of the last attempt that is not
    # wait_cluster_stable's retry or give-up message.
    CHAOS_SETTLE_REASON="$(awk '
      /^probing live cluster/ { last = ""; next }
      /^waiting for Flynn to settle after / || /^cluster did not return to a stable state after / { next }
      /^Connection to .* closed/ || /^Warning: Permanently added/ || /^[[:space:]]*$/ { next }
      { last = $0 }
      END { print last }' "${out}" | cut -c1-300)"
    [[ -n "${CHAOS_SETTLE_REASON}" ]] || CHAOS_SETTLE_REASON="the probe did not pass in ${tries} tries"
  fi
  rm -f "${out}"
  return "${st}"
}

# chaos_verdict prints the verdict of the run in $1 (rounds.tsv, samples.tsv,
# ingress.tsv) and returns 0 for PASSED, 1 for FAILED and 2 for a bad
# CHAOS_BUDGET. It fails when no round injected a fault, when more than
# CHAOS_MAX_SKIPPED (unset: no limit) rounds were skipped, when a round took
# longer than $2 (CHAOS_TIMEOUT) seconds to recover, or when a round breaks a
# budget. CHAOS_BUDGET (default CHAOS_DEFAULT_BUDGET, empty for none) is a
# list of <fault>:<check>=<max>: <check> is a health section (cluster,
# tenants, plugins, resources, orphans, bridge, monitor, nodes) and <max> the
# failed samples of it allowed while that round runs, "unhealthy" counts
# every failed sample, "recovery" is the most seconds the round may take,
# "ingress" the failed probe seconds (a second and target no node served,
# see chaos_ingress_rounds) and "ingress-longest" the longest run of failed
# seconds of one target. A round with no probe data is not checked for the
# ingress budgets. When a tenant-job round that stopped the only web job of
# its app breaks a budget, the message says so.
chaos_verdict() {
  local dir="$1" timeout="$2" budget="${CHAOS_BUDGET-${CHAOS_DEFAULT_BUDGET}}"
  chaos_check_budget || return 2
  awk -F'\t' -v timeout="${timeout}" -v budget="${budget}" -v maxskip="${CHAOS_MAX_SKIPPED:-}" '
    BEGIN {
      nb = split(budget, b, " ")
      for (k = 1; k <= nb; k++) { split(b[k], p, /[:=]/); lim[p[1], p[2]] = p[3] + 0; checks[p[1]] = checks[p[1]] " " p[2] }
    }
    FILENAME == ARGV[1] {
      if ($3 !~ /^round [0-9]+ /) next
      split($3, p, " ")
      if ($4 != "healthy") bad[p[2], "unhealthy"]++
      ns = split($5, s, ",")
      for (k = 1; k <= ns; k++) if (s[k] != "-") bad[p[2], s[k]]++
      next
    }
    FILENAME == ARGV[2] {
      ig[$1] = 1; igf[$1] = $4 + 0; igl[$1] = $5 + 0; iglt[$1] = $6; igt[$1] = $7
      next
    }
    {
      nr++
      if ($6 == "skipped") { skp++; next }
      if ($6 != "recovered") next
      rec++
      secs = $7; sub(/s$/, "", secs); secs += 0
      if (secs > timeout + 0) out = out "; round " $1 " " $2 " recovered in " secs "s, more than CHAOS_TIMEOUT=" timeout "s"
      hint = ""
      if ($2 == "tenant-job" && $3 ~ / type=web / && $3 ~ / running=1$/) {
        app = $3; sub(/.* app=/, "", app); sub(/ .*/, "", app)
        hint = ": tenant app " app " runs a single web job; scale web=2 or set CHAOS_BUDGET"
      }
      nc = split(checks[$2], c, " ")
      for (k = 1; k <= nc; k++) {
        max = lim[$2, c[k]]
        if (c[k] == "recovery") { if (secs > max) out = out "; round " $1 " " $2 " recovered in " secs "s, budget " max "s"; continue }
        if (c[k] == "ingress") { if (($1 in ig) && igf[$1] > max) out = out "; round " $1 " " $2 " had " igf[$1] " failed ingress probe seconds (" igt[$1] "), budget " max hint; continue }
        if (c[k] == "ingress-longest") { if (($1 in ig) && igl[$1] > max) out = out "; round " $1 " " $2 " had a " igl[$1] "s ingress outage (" iglt[$1] "), budget " max "s" hint; continue }
        got = bad[$1, c[k]] + 0
        if (got > max) out = out "; round " $1 " " $2 " had " got " " (c[k] == "unhealthy" ? "UNHEALTHY" : "failed " c[k]) " samples, budget " max hint
      }
    }
    END {
      if (!rec) out = "; no fault was injected (" skp + 0 " skipped)" out
      if (maxskip != "" && skp + 0 > maxskip + 0) out = out "; " skp " skipped rounds, more than CHAOS_MAX_SKIPPED=" maxskip
      if (out != "") { print "FAILED: " substr(out, 3); exit 1 }
      printf "PASSED: %d recovered, %d skipped\n", rec, skp
    }' "${dir}/samples.tsv" <(chaos_ingress_rounds "${dir}") "${dir}/rounds.tsv"
}

# chaos_cluster runs CHAOS_ROUNDS faults (default 10) cycling through a
# shuffled CHAOS_FAULTS list. After each fault it waits CHAOS_SETTLE seconds,
# then up to CHAOS_TIMEOUT seconds for every health section to pass, then for
# full strength (chaos_full_strength) before the next fault. Stops at the
# first round that does not recover or does not reach full strength. After
# the last round the full-strength gate runs once more, and the run fails
# when the cluster is not back by then, so the next command (update, reload,
# an e2e) does not start on a degraded cluster. CHAOS_SEED repeats a run. A
# background monitor samples health for the whole run, and a report is
# written to CHAOS_REPORT_DIR (default ./flynn-logs/chaos/<UTC time>). The
# verdict is chaos_verdict's: all rounds skipped, a slow recovery or a broken
# CHAOS_BUDGET fails the run too. From the baseline to the end a per-second
# ingress prober (chaos_start_ingress) writes ingress.tsv for the ingress
# budgets and the report's Ingress section.
chaos_cluster() {
  require_cluster_nodes
  local rounds="${CHAOS_ROUNDS:-10}" timeout="${CHAOS_TIMEOUT:-900}" settle="${CHAOS_SETTLE:-20}"
  local seed="${CHAOS_SEED:-$(date +%s)}" kinds=() i j tmp kind start st inject_at
  local started verdict recovered recovered_at settled settle_line="" final_fail=""
  started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  chaos_check_budget >&2 || return 1
  CHAOS_RUN_DIR="${CHAOS_REPORT_DIR:-${ROOT}/flynn-logs/chaos/$(date -u +%Y%m%dT%H%M%SZ)}"
  mkdir -p "${CHAOS_RUN_DIR}"
  : > "${CHAOS_RUN_DIR}/samples.tsv"
  : > "${CHAOS_RUN_DIR}/rounds.tsv"
  read -r -a kinds <<< "${CHAOS_FAULTS:-${CHAOS_ALL_FAULTS}}"
  RANDOM="${seed}"
  for ((i = ${#kinds[@]} - 1; i > 0; i--)); do
    j=$((RANDOM % (i + 1)))
    tmp="${kinds[i]}"; kinds[i]="${kinds[j]}"; kinds[j]="${tmp}"
  done
  CHAOS_TENANT_APPS="$(list_tenant_apps 2>/dev/null | tr '\n' ' ' | sed 's/[[:space:]]*$//' || true)"
  echo "chaos: rounds=${rounds} seed=${seed} timeout=${timeout}s order=${kinds[*]}"
  echo "tenant apps: ${CHAOS_TENANT_APPS:-none}"
  echo "report: ${CHAOS_RUN_DIR}/report.txt"
  if ! chaos_wait_healthy "baseline" 300; then
    echo "cluster is not healthy before chaos; fix that first" >&2
    return 1
  fi
  echo "$(chaos_now) baseline healthy"
  # Every resource (id:app:nodes) seen at baseline must still exist at the
  # same size in every later sample; the health check's resources section
  # enforces that.
  CHAOS_EXPECT_RESOURCES="$(printf '%s\n' "${CHAOS_HEALTH_OUT}" | sed -n 's/^ok resources:.*inventory=//p' | head -1)"
  echo "resources at baseline: ${CHAOS_EXPECT_RESOURCES:-none}"
  echo "baseline" > "${CHAOS_RUN_DIR}/phase"
  chaos_monitor_loop "${CHAOS_RUN_DIR}" < /dev/null &
  CHAOS_MONITOR_PID=$!
  trap 'chaos_stop_monitor' EXIT
  trap 'chaos_stop_monitor; exit 130' INT TERM
  # Probe the tenant apps that answered at baseline ("ok tenants: tenant
  # HTTP ok: app=200 ..."); one that was already down or deleted would fail
  # every second.
  if [[ -z "${CHAOS_INGRESS_APPS+x}" ]]; then
    CHAOS_INGRESS_APPS="$(printf '%s\n' "${CHAOS_HEALTH_OUT}" | sed -n 's/^ok tenants: tenant HTTP ok: //p' | head -1 | tr ' ' '\n' | sed -n 's/=.*//p' | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
  fi
  chaos_start_ingress
  verdict=""
  CHAOS_SETTLE_SUMMARY=""
  for ((i = 1; i <= rounds; i++)); do
    kind="${kinds[$(((i - 1) % ${#kinds[@]}))]}"
    CHAOS_TARGET=""
    st=0
    rm -f "${CHAOS_RUN_DIR}/avoid"
    echo "round ${i} ${kind}" > "${CHAOS_RUN_DIR}/phase"
    inject_at="$(date -u +%s)"
    chaos_inject "${kind}" "${seed}-${i}" || st=$?
    if [[ "${st}" -eq 3 ]]; then
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: skipped (${CHAOS_TARGET})"
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" skipped - >> "${CHAOS_RUN_DIR}/rounds.tsv"
      continue
    elif [[ "${st}" -ne 0 ]]; then
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: fault failed: ${CHAOS_TARGET}" >&2
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" inject-failed - >> "${CHAOS_RUN_DIR}/rounds.tsv"
      verdict="FAILED: round ${i} ${kind} could not inject the fault"
      settle_line="not run: the run stopped at round ${i}"
      break
    fi
    echo "$(chaos_now) round ${i}/${rounds} ${kind}: ${CHAOS_TARGET}"
    start=${SECONDS}
    sleep "${settle}"
    avoid_node=""
    if [[ -f "${CHAOS_RUN_DIR}/avoid" ]]; then
      avoid_node="$(cat "${CHAOS_RUN_DIR}/avoid")"
    fi
    if chaos_wait_healthy "round ${i}" "${timeout}" "${avoid_node}"; then
      rm -f "${CHAOS_RUN_DIR}/avoid"
      recovered="$((SECONDS - start))s"
      recovered_at="$(date -u +%s)"
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: recovered in ${recovered}"
      # The last round is followed by the final gate below. The gate is not
      # part of the round's phase, so it is not charged to the round budget.
      settled=final
      if (( i < rounds )); then
        echo "settle after round ${i} ${kind}" > "${CHAOS_RUN_DIR}/phase"
        if ! chaos_full_strength "round ${i} ${kind}"; then
          printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "${recovered_at}" recovered "${recovered}" ">${CHAOS_SETTLE_SECS}s" >> "${CHAOS_RUN_DIR}/rounds.tsv"
          verdict="FAILED: cluster did not return to full strength after round ${i} ${kind}: ${CHAOS_SETTLE_REASON}"
          echo "$(chaos_now) ${verdict}" >&2
          settle_line="not run: the run stopped at round ${i}"
          break
        fi
        settled="${CHAOS_SETTLE_SECS}s"
      fi
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "${recovered_at}" recovered "${recovered}" "${settled}" >> "${CHAOS_RUN_DIR}/rounds.tsv"
    else
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" FAILED ">${timeout}s" >> "${CHAOS_RUN_DIR}/rounds.tsv"
      verdict="FAILED: round ${i} ${kind} did not recover within ${timeout}s"
      settle_line="not run: the run stopped at round ${i}"
      break
    fi
    echo "between rounds" > "${CHAOS_RUN_DIR}/phase"
  done
  # Final gate: the run is only over when the cluster is back at full
  # strength. Skipped when a round already stopped the run (it failed).
  if [[ -z "${settle_line}" ]]; then
    echo "settle" > "${CHAOS_RUN_DIR}/phase"
    if chaos_full_strength "chaos"; then
      settle_line="full strength after ${CHAOS_SETTLE_SECS}s (CHAOS_SETTLE_TRIES=${CHAOS_SETTLE_TRIES:-120})"
    else
      settle_line="not at full strength after ${CHAOS_SETTLE_SECS}s (CHAOS_SETTLE_TRIES=${CHAOS_SETTLE_TRIES:-120}): ${CHAOS_SETTLE_REASON}"
      final_fail="cluster did not return to full strength after chaos: ${CHAOS_SETTLE_REASON}"
      echo "$(chaos_now) FAILED: ${final_fail}" >&2
    fi
  fi
  CHAOS_SETTLE_SUMMARY="${settle_line}"
  chaos_stop_monitor
  trap - EXIT INT TERM
  st=0
  if [[ -n "${verdict}" ]]; then
    st=1
  else
    verdict="$(chaos_verdict "${CHAOS_RUN_DIR}" "${timeout}")" || st=1
  fi
  if [[ -n "${final_fail}" ]]; then
    st=1
    case "${verdict}" in
      FAILED:*) verdict="FAILED: ${final_fail}; ${verdict#FAILED: }" ;;
      *) verdict="FAILED: ${final_fail}" ;;
    esac
  fi
  chaos_write_report "${CHAOS_RUN_DIR}" "${seed}" "${rounds}" "${kinds[*]}" "${started}" "${verdict}"
  echo
  cat "${CHAOS_RUN_DIR}/report.txt"
  return "${st}"
}
