# shellcheck shell=bash
# monitor and chaos for script/vagrant.sh (laptop loop). Health samples run
# guest/check-health.sh on a running cluster node; faults run
# guest/chaos-fault.sh (job kills) or act on a node (flynn-host restart, hard
# power-off and boot). Needs run_as_root_on and start_one_cluster_node from
# vagrant.sh.

CHAOS_ALL_FAULTS="system-job plugin-job datastore-primary datastore-replica tenant-job host-restart node-reboot"
CHAOS_HEALTH_OUT=""

# chaos_check_node prints the first running cluster node other than $1.
chaos_check_node() {
  local avoid="${1:-}" name
  while IFS= read -r name; do
    if [[ -n "${name}" && "${name}" != "${avoid}" ]]; then
      echo "${name}"
      return 0
    fi
  done < <(flynn_vagrant_running_cluster_nodes)
  return 1
}

# chaos_health takes one health sample into CHAOS_HEALTH_OUT.
chaos_health() {
  local node ip st=0
  if ! node="$(chaos_check_node "${1:-}")"; then
    CHAOS_HEALTH_OUT="FAIL nodes: no running cluster node to check from"
    return 1
  fi
  ip="$(flynn_vagrant_node_ip "${node}")"
  set +e
  CHAOS_HEALTH_OUT="$(run_as_root_on "${node}" "cd ${SRC} && CLUSTER_DOMAIN=$(flynn_vagrant_cluster_domain) CLUSTER_IP=${ip} MIN_HOSTS=$(flynn_vagrant_get_nodes) BUILDER_IP=${FLYNN_DEV_IP:-192.168.57.10} TENANT_APPS=$(printf '%q' "${CHAOS_TENANT_APPS:-}") script/vagrant/guest/check-health.sh" 2>&1 | grep -v '^Connection to .* closed')"
  st=$?
  set -e
  if [[ "${st}" -ne 0 ]]; then
    if ! grep -q '^FAIL ' <<<"${CHAOS_HEALTH_OUT}"; then
      CHAOS_HEALTH_OUT="FAIL monitor: health check on ${node} exited ${st} without a result: $(printf '%s\n' "${CHAOS_HEALTH_OUT}" | grep -v '^[[:space:]]*$' | tail -2 | paste -sd ';' - | cut -c1-200 || true)"
    fi
    return 1
  fi
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
        out="$(run_as_root_on "${nodes[0]}" "cd ${SRC} && FAULT=${kind} SEED=${seed} script/vagrant/guest/chaos-fault.sh" 2>&1)"
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
      -v tenants="${CHAOS_TENANT_APPS:-none}" -v interval="${CHAOS_MONITOR_INTERVAL:-15}" '
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
        nr++; rr[nr] = $1; rk[nr] = $2; rt[nr] = $3; rres[nr] = $6; rrec[nr] = $7
        if ($6 == "recovered") { rec++; v = $7; sub(/s$/, "", v); if (v + 0 > maxrec) { maxrec = v + 0; maxround = $1 } }
        else if ($6 == "skipped") skp++
        else failed++
      }
      END {
        pass = (verdict ~ /^PASSED/)
        print "<h1>Flynn chaos report</h1>"
        printf "<div class=\"verdict %s\">%s</div>\n", (pass ? "pass" : "fail"), esc(verdict)
        printf "<p class=\"meta\">%s &rarr; %s &middot; seed <code>%s</code> (<code>CHAOS_SEED=%s</code> repeats this run) &middot; %s nodes &middot; tenant apps: %s<br>fault order: %s</p>\n", esc(started), esc(finished), esc(seed), esc(seed), esc(nodes), esc(tenants), esc(order)
        print "<div class=\"cards\">"
        printf "<div class=\"card\"><div class=\"k\">Rounds</div><div class=\"v\">%d / %d</div><div class=\"s\">recovered of %s planned</div></div>\n", rec, nr, esc(rounds)
        printf "<div class=\"card\"><div class=\"k\">Skipped / failed</div><div class=\"v\">%d / %d</div><div class=\"s\">rounds</div></div>\n", skp, failed
        printf "<div class=\"card\"><div class=\"k\">Slowest recovery</div><div class=\"v\">%s</div><div class=\"s\">%s</div></div>\n", (rec ? maxrec "s" : "-"), (rec ? "round " maxround : "no recovered rounds")
        printf "<div class=\"card\"><div class=\"k\">Healthy samples</div><div class=\"v\">%d%%</div><div class=\"s\">%d of %d, every %ss</div></div>\n", (total ? int(ok * 100 / total) : 0), ok, total, esc(interval)
        print "</div>"

        print "<h2>Rounds</h2><table><tr><th>Round</th><th>Fault</th><th>Result</th><th>Recovery</th><th>Checks failed</th><th>Target</th></tr>"
        for (i = 1; i <= nr; i++) {
          cls = (rres[i] == "recovered" ? "ok" : (rres[i] == "skipped" ? "skip" : "bad"))
          printf "<tr><td class=\"num\">%s</td><td>%s</td><td><span class=\"badge %s\">%s</span></td><td class=\"num\">%s</td><td>%s</td><td class=\"msg\">%s</td></tr>\n", esc(rr[i]), esc(rk[i]), cls, esc(rres[i]), esc(rrec[i]), (secs[rr[i]] ? secs[rr[i]] : "-"), esc(rt[i])
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
        print "<footer>Raw data: <a href=\"samples.tsv\">samples.tsv</a> (one line per health sample), <a href=\"rounds.tsv\">rounds.tsv</a> (one line per fault), <a href=\"report.txt\">report.txt</a></footer>"
      }' "${dir}/samples.tsv" "${dir}/rounds.tsv"
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
    echo "  seed      ${seed} (CHAOS_SEED=${seed} repeats this run)"
    echo "  rounds    ${rounds}, order: ${order}"
    echo "  nodes     $(flynn_vagrant_get_nodes), tenant apps: ${CHAOS_TENANT_APPS:-none}"
    echo
    echo "Rounds"
    printf '  %-5s %-18s %-14s %-9s %-24s %s\n' ROUND FAULT RESULT RECOVERY "CHECKS FAILED" TARGET
    awk -F'\t' '
      FILENAME == ARGV[1] { if ($3 ~ /^round /) { split($3, p, " "); n = p[2]; ns = split($5, s, ","); for (k = 1; k <= ns; k++) if (s[k] != "-" && !seen[n, s[k]]++) secs[n] = secs[n] (secs[n] ? "," : "") s[k] }; next }
      { printf "  %-5s %-18s %-14s %-9s %-24s %s\n", $1, $2, $6, $7, (secs[$1] ? secs[$1] : "-"), $3 }
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
    echo "Raw data: ${dir}/samples.tsv, ${dir}/rounds.tsv"
    echo "HTML report: ${dir}/report.html"
  } > "${dir}/report.txt"
}

# chaos_cluster runs CHAOS_ROUNDS faults (default 10) cycling through a
# shuffled CHAOS_FAULTS list. After each fault it waits CHAOS_SETTLE seconds,
# then up to CHAOS_TIMEOUT seconds for every health section to pass. Stops at
# the first round that does not recover. CHAOS_SEED repeats a run. A
# background monitor samples health for the whole run, and a report is
# written to CHAOS_REPORT_DIR (default ./flynn-logs/chaos/<UTC time>).
chaos_cluster() {
  require_cluster_nodes
  local rounds="${CHAOS_ROUNDS:-10}" timeout="${CHAOS_TIMEOUT:-900}" settle="${CHAOS_SETTLE:-20}"
  local seed="${CHAOS_SEED:-$(date +%s)}" kinds=() i j tmp kind start st inject_at
  local started verdict
  started="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
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
  echo "baseline" > "${CHAOS_RUN_DIR}/phase"
  chaos_monitor_loop "${CHAOS_RUN_DIR}" < /dev/null &
  CHAOS_MONITOR_PID=$!
  trap 'chaos_stop_monitor' EXIT
  trap 'chaos_stop_monitor; exit 130' INT TERM
  verdict=""
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
      break
    fi
    echo "$(chaos_now) round ${i}/${rounds} ${kind}: ${CHAOS_TARGET}"
    start=${SECONDS}
    sleep "${settle}"
    if chaos_wait_healthy "round ${i}" "${timeout}"; then
      rm -f "${CHAOS_RUN_DIR}/avoid"
      echo "$(chaos_now) round ${i}/${rounds} ${kind}: recovered in $((SECONDS - start))s"
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" recovered "$((SECONDS - start))s" >> "${CHAOS_RUN_DIR}/rounds.tsv"
    else
      printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "${i}" "${kind}" "${CHAOS_TARGET}" "${inject_at}" "$(date -u +%s)" FAILED ">${timeout}s" >> "${CHAOS_RUN_DIR}/rounds.tsv"
      verdict="FAILED: round ${i} ${kind} did not recover within ${timeout}s"
      break
    fi
    echo "between rounds" > "${CHAOS_RUN_DIR}/phase"
  done
  chaos_stop_monitor
  trap - EXIT INT TERM
  st=0
  if [[ -n "${verdict}" ]]; then
    st=1
  else
    verdict="PASSED: $(awk -F'\t' '$6 == "recovered"' "${CHAOS_RUN_DIR}/rounds.tsv" | wc -l | tr -d ' ') recovered, $(awk -F'\t' '$6 == "skipped"' "${CHAOS_RUN_DIR}/rounds.tsv" | wc -l | tr -d ' ') skipped"
  fi
  chaos_write_report "${CHAOS_RUN_DIR}" "${seed}" "${rounds}" "${kinds[*]}" "${started}" "${verdict}"
  echo
  cat "${CHAOS_RUN_DIR}/report.txt"
  return "${st}"
}
