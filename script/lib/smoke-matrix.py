#!/usr/bin/env python3
"""Load a Flynn Vagrant smoke matrix document.

The matrix is a small YAML subset (comments, maps, lists, scalars). Env vars
named in FIELD_TO_ENV override file values when they were already set in the
caller's environment (see snapshot-env / SMOKE_MATRIX_EXPLICIT).
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shlex
import sys
from typing import Any

VERSION = 1

# Whole-run keys: applied once (builder, build, host units).
RUN_FIELDS = {
    "skip_unit_tests": "SKIP_UNIT_TESTS",
    "skip_builder_unit_tests": "SKIP_BUILDER_UNIT_TESTS",
    "skip_vagrant_up": "SKIP_VAGRANT_UP",
    "skip_build": "SKIP_BUILD",
    "keep_vms": "KEEP_VMS",
    "keep_builder": "KEEP_BUILDER",
    "keep_vms_on_fail": "KEEP_VMS_ON_FAIL",
    "keep_logs": "KEEP_LOGS",
    "skip_teardown": "SKIP_TEARDOWN",
    "smoke_detail": "SMOKE_DETAIL",
    "build_version": "BUILD_VERSION",
    "build_phase": "BUILD_PHASE",
    "cluster_domain": "CLUSTER_DOMAIN",
    "vagrant_memory": "VAGRANT_MEMORY",
    "vagrant_cpus": "VAGRANT_CPUS",
    "builder_memory": "BUILDER_MEMORY",
    "builder_cpus": "BUILDER_CPUS",
    "plugin_repo_root": "PLUGIN_REPO_ROOT",
    "plugin_build_concurrency": "PLUGIN_BUILD_CONCURRENCY",
    "flynn_build_attempts": "FLYNN_BUILD_ATTEMPTS",
    "smoke_max_nodes": "SMOKE_MAX_NODES",
    "smoke_firewall_port": "SMOKE_FIREWALL_PORT",
}

# Per-item keys: applied before that item's topology loop.
ITEM_FIELDS = {
    "topologies": "SMOKE_TOPOLOGIES",
    "cluster_size": "CLUSTER_SIZE",
    "skip_install": "SKIP_INSTALL",
    "skip_deploy": "SKIP_DEPLOY",
    "skip_verify_before": "SKIP_VERIFY_BEFORE",
    "skip_upgrade": "SKIP_UPGRADE",
    "skip_backup": "SKIP_BACKUP",
    "skip_cli": "SKIP_CLI",
    "skip_volume": "SKIP_VOLUME",
    "skip_plugin_install": "SKIP_PLUGIN_INSTALL",
    "skip_buildpack": "SKIP_BUILDPACK",
    "resume_at": "RESUME_AT",
    "upgrade_passes": "UPGRADE_PASSES",
    "seed_rows": "SMOKE_SEED_ROWS",
    "blob_count": "SMOKE_BLOB_COUNT",
    "plugins": "PLUGIN_SMOKE_APPS",
    "datastores": "SMOKE_DATASTORES",
    "blobstore_backend": "SMOKE_BLOBSTORE_BACKEND",
}

FIELD_TO_ENV = {**RUN_FIELDS, **ITEM_FIELDS}
ENV_TO_FIELD = {v: k for k, v in FIELD_TO_ENV.items()}
BOOL_FIELDS = {
    k
    for k, env in FIELD_TO_ENV.items()
    if env.startswith("SKIP_") or env.startswith("KEEP_") or env == "SMOKE_DETAIL"
}

LOCAL_NAME = "smoke-matrix.yaml"
EXAMPLE_NAME = "smoke-matrix.example.yaml"


class MatrixError(Exception):
    pass


def parse_simple_yaml(text: str) -> Any:
    """Parse a restricted YAML subset used by smoke matrix documents."""
    lines: list[tuple[int, str]] = []
    for raw in text.splitlines():
        stripped = _strip_comment(raw)
        if not stripped.strip():
            continue
        indent = len(stripped) - len(stripped.lstrip(" "))
        if indent % 2 != 0:
            raise MatrixError(f"indent must be multiples of 2: {raw!r}")
        lines.append((indent, stripped.strip()))
    if not lines:
        return {}
    value, _ = _parse_block(lines, 0, 0)
    return value


def _strip_comment(raw: str) -> str:
    out = []
    quote = None
    i = 0
    while i < len(raw):
        ch = raw[i]
        if quote:
            out.append(ch)
            if ch == quote and (i == 0 or raw[i - 1] != "\\"):
                quote = None
            i += 1
            continue
        if ch in ("'", '"'):
            quote = ch
            out.append(ch)
            i += 1
            continue
        if ch == "#":
            break
        out.append(ch)
        i += 1
    return "".join(out).rstrip()


def _parse_block(lines: list[tuple[int, str]], idx: int, indent: int) -> tuple[Any, int]:
    if idx >= len(lines):
        return {}, idx
    cur_indent, content = lines[idx]
    if cur_indent < indent:
        return {}, idx
    if content.startswith("- "):
        return _parse_list(lines, idx, cur_indent)
    return _parse_map(lines, idx, cur_indent)


def _parse_map(lines: list[tuple[int, str]], idx: int, indent: int) -> tuple[dict[str, Any], int]:
    out: dict[str, Any] = {}
    while idx < len(lines):
        cur_indent, content = lines[idx]
        if cur_indent < indent:
            break
        if cur_indent > indent:
            raise MatrixError(f"unexpected indent at {content!r}")
        if content.startswith("- "):
            raise MatrixError(f"list item where a map key was expected: {content!r}")
        key, sep, rest = content.partition(":")
        if not sep:
            raise MatrixError(f"expected 'key:' at {content!r}")
        key = key.strip()
        rest = rest.strip()
        idx += 1
        if rest == "":
            if idx < len(lines) and lines[idx][0] > indent:
                child, idx = _parse_block(lines, idx, lines[idx][0])
                out[key] = child
            else:
                out[key] = None
            continue
        out[key] = _parse_scalar(rest)
    return out, idx


def _parse_list(lines: list[tuple[int, str]], idx: int, indent: int) -> tuple[list[Any], int]:
    out: list[Any] = []
    while idx < len(lines):
        cur_indent, content = lines[idx]
        if cur_indent < indent:
            break
        if cur_indent > indent:
            raise MatrixError(f"unexpected indent at {content!r}")
        if not content.startswith("- "):
            break
        rest = content[2:].strip()
        idx += 1
        if rest == "":
            if idx < len(lines) and lines[idx][0] > indent:
                child, idx = _parse_block(lines, idx, lines[idx][0])
                out.append(child)
            else:
                out.append(None)
            continue
        if ":" in rest and not rest.startswith(("'", '"')):
            key, sep, val = rest.partition(":")
            item: dict[str, Any] = {key.strip(): _parse_scalar(val.strip()) if val.strip() else None}
            if idx < len(lines) and lines[idx][0] > indent:
                nested, idx = _parse_map(lines, idx, lines[idx][0])
                item.update(nested)
            out.append(item)
            continue
        out.append(_parse_scalar(rest))
    return out, idx


def _parse_scalar(s: str) -> Any:
    if s == "[]":
        return []
    if s == "" or s in ("null", "~"):
        return None
    if s in ("true", "True", "yes", "YES"):
        return True
    if s in ("false", "False", "no", "NO"):
        return False
    if (s.startswith('"') and s.endswith('"')) or (s.startswith("'") and s.endswith("'")):
        return s[1:-1]
    if re.fullmatch(r"-?\d+", s):
        return int(s)
    return s


def load_matrix(path: str) -> dict[str, Any]:
    try:
        with open(path, encoding="utf-8") as f:
            raw = f.read()
    except OSError as e:
        raise MatrixError(f"cannot read {path}: {e}") from e
    data = parse_simple_yaml(raw)
    if data is None:
        data = {}
    if not isinstance(data, dict):
        raise MatrixError(f"{path}: document must be a map")
    ver = data.get("version", VERSION)
    if ver != VERSION:
        raise MatrixError(f"{path}: unsupported version {ver} (want {VERSION})")
    items = data.get("items")
    if items is None:
        items = []
    if not isinstance(items, list):
        raise MatrixError(f"{path}: items must be a list")
    defaults = data.get("defaults") or {}
    if defaults is None:
        defaults = {}
    if not isinstance(defaults, dict):
        raise MatrixError(f"{path}: defaults must be a map")
    seen: set[str] = set()
    normalized = []
    for i, item in enumerate(items):
        if not isinstance(item, dict):
            raise MatrixError(f"{path}: items[{i}] must be a map")
        ident = str(item.get("id") or "").strip()
        if not ident:
            raise MatrixError(f"{path}: items[{i}] is missing id")
        if ident in seen:
            raise MatrixError(f"{path}: duplicate item id {ident!r}")
        seen.add(ident)
        normalized.append(item)
    data["defaults"] = defaults
    data["items"] = normalized
    data["_path"] = path
    return data


def resolve_matrix_path(root: str, cli_path: str | None = None) -> str:
    if cli_path:
        path = cli_path if os.path.isabs(cli_path) else os.path.join(root, cli_path)
        if not os.path.isfile(path):
            raise MatrixError(f"matrix file not found: {path}")
        return os.path.abspath(path)
    env_path = os.environ.get("SMOKE_MATRIX", "").strip()
    if env_path:
        path = env_path if os.path.isabs(env_path) else os.path.join(root, env_path)
        if not os.path.isfile(path):
            raise MatrixError(f"SMOKE_MATRIX not found: {path}")
        return os.path.abspath(path)
    local = os.path.join(root, LOCAL_NAME)
    if os.path.isfile(local):
        return os.path.abspath(local)
    example = os.path.join(root, EXAMPLE_NAME)
    if os.path.isfile(example):
        return os.path.abspath(example)
    raise MatrixError(f"no matrix file (looked for {LOCAL_NAME} then {EXAMPLE_NAME} under {root})")


def explicit_env() -> dict[str, str]:
    raw = os.environ.get("SMOKE_MATRIX_EXPLICIT", "")
    if raw:
        try:
            data = json.loads(raw)
        except json.JSONDecodeError as e:
            raise MatrixError(f"SMOKE_MATRIX_EXPLICIT is not JSON: {e}") from e
        if not isinstance(data, dict):
            raise MatrixError("SMOKE_MATRIX_EXPLICIT must be a JSON object")
        return {str(k): str(v) for k, v in data.items()}
    return {k: os.environ[k] for k in ENV_TO_FIELD if k in os.environ}


def snapshot_env() -> dict[str, str]:
    return {k: os.environ[k] for k in ENV_TO_FIELD if k in os.environ}


def _as_bool(value: Any) -> bool:
    if isinstance(value, bool):
        return value
    if isinstance(value, int):
        return value != 0
    s = str(value).strip().lower()
    return s in ("1", "true", "yes", "on")


def _env_value(field: str, value: Any) -> str:
    if field == "topologies":
        if isinstance(value, list):
            return ",".join(str(x) for x in value)
        return str(value).replace(" ", "")
    if field in ("plugins", "datastores"):
        if isinstance(value, list):
            return " ".join(str(x) for x in value)
        return str(value).strip()
    if field in BOOL_FIELDS:
        return "1" if _as_bool(value) else "0"
    if value is None:
        return ""
    return str(value)


def _item_enabled(item: dict[str, Any]) -> bool:
    if "enabled" not in item or item["enabled"] is None:
        return True
    return _as_bool(item["enabled"])


def select_items(matrix: dict[str, Any], wanted: list[str]) -> list[dict[str, Any]]:
    items: list[dict[str, Any]] = matrix["items"]
    by_id = {str(it["id"]): it for it in items}
    if wanted:
        out = []
        missing = []
        for ident in wanted:
            ident = ident.strip()
            if not ident:
                continue
            if ident not in by_id:
                missing.append(ident)
                continue
            out.append(by_id[ident])
        if missing:
            known = ", ".join(by_id) or "(none)"
            raise MatrixError(f"unknown matrix item(s): {', '.join(missing)} (known: {known})")
        if not out:
            raise MatrixError("no matrix items selected")
        return out
    enabled = [it for it in items if _item_enabled(it)]
    if not enabled:
        raise MatrixError(f"{matrix.get('_path')}: no enabled items (enable one, or pass --item)")
    return enabled


def parse_item_ids(raw: str | None) -> list[str]:
    if not raw:
        return []
    parts = []
    for chunk in raw.split(","):
        chunk = chunk.strip()
        if chunk:
            parts.append(chunk)
    return parts


def merge_item(matrix: dict[str, Any], item: dict[str, Any]) -> dict[str, Any]:
    merged = dict(matrix.get("defaults") or {})
    for key, value in item.items():
        if key in ("id", "description", "enabled"):
            continue
        merged[key] = value
    return merged


def topology_specs(item: dict[str, Any]) -> list[str]:
    raw = item.get("topologies")
    if raw is None and item.get("cluster_size") is not None:
        raw = item.get("cluster_size")
    if raw is None:
        return []
    if isinstance(raw, list):
        parts = [str(x).strip() for x in raw if str(x).strip()]
    else:
        parts = [p.strip() for p in str(raw).replace(" ", "").split(",") if p.strip()]
    aliases = {
        "add": "add",
        "add-node": "add",
        "3+1": "add",
        "remove": "remove",
        "remove-node": "remove",
        "3-1": "remove",
        "discovery": "discovery",
        "discovery-join": "discovery",
        "1+2": "discovery",
        "both": "1,3",
    }
    out: list[str] = []
    for p in parts:
        mapped = aliases.get(p, p)
        if mapped == "1,3":
            for n in ("1", "3"):
                if n not in out:
                    out.append(n)
            continue
        if mapped not in out:
            out.append(mapped)
    return out


def inventory_size(spec: str) -> int:
    spec = {
        "add": "add",
        "add-node": "add",
        "3+1": "add",
        "remove": "remove",
        "remove-node": "remove",
        "3-1": "remove",
        "discovery": "discovery",
        "discovery-join": "discovery",
        "1+2": "discovery",
    }.get(spec, spec)
    if spec == "add":
        return 4
    if spec in ("remove", "discovery"):
        return 3
    return int(spec)


def assignments(fields: dict[str, str], values: dict[str, Any], expl: dict[str, str]) -> list[str]:
    lines = []
    for field, env in fields.items():
        if field not in values or values[field] is None:
            continue
        if env in expl:
            continue
        lines.append(f"{env}={shlex.quote(_env_value(field, values[field]))}")
    return lines


def cmd_snapshot_env(_: argparse.Namespace) -> int:
    sys.stdout.write(json.dumps(snapshot_env(), sort_keys=True))
    return 0


def cmd_resolve(args: argparse.Namespace) -> int:
    sys.stdout.write(resolve_matrix_path(args.root, args.matrix) + "\n")
    return 0


def cmd_list(args: argparse.Namespace) -> int:
    path = resolve_matrix_path(args.root, args.matrix)
    matrix = load_matrix(path)
    sys.stdout.write(f"matrix: {path}\n")
    width = max((len(str(it["id"])) for it in matrix["items"]), default=2)
    for it in matrix["items"]:
        flag = "enabled" if _item_enabled(it) else "disabled"
        desc = str(it.get("description") or "").strip()
        specs = ",".join(topology_specs(merge_item(matrix, it))) or "-"
        extra = f"  {desc}" if desc else ""
        sys.stdout.write(f"  {str(it['id']):<{width}}  {flag:<8}  {specs}{extra}\n")
    return 0


def _wanted(args: argparse.Namespace) -> list[str]:
    wanted = list(args.item or [])
    if not wanted:
        wanted = parse_item_ids(os.environ.get("SMOKE_MATRIX_ITEM", ""))
    return wanted


def cmd_select(args: argparse.Namespace) -> int:
    path = resolve_matrix_path(args.root, args.matrix)
    matrix = load_matrix(path)
    for it in select_items(matrix, _wanted(args)):
        sys.stdout.write(str(it["id"]) + "\n")
    return 0


def plugin_names(value: Any) -> list[str]:
    if value is None:
        return []
    if isinstance(value, list):
        return [str(x) for x in value if str(x).strip()]
    return [part for part in str(value).split() if part]


def union_plugins(matrix: dict[str, Any], items: list[dict[str, Any]]) -> list[str]:
    seen: list[str] = []
    for it in items:
        for name in plugin_names(merge_item(matrix, it).get("plugins")):
            if name not in seen:
                seen.append(name)
    return seen


def cmd_apply_run(args: argparse.Namespace) -> int:
    path = resolve_matrix_path(args.root, args.matrix)
    matrix = load_matrix(path)
    expl = explicit_env()
    defaults = matrix.get("defaults") or {}
    run_values = dict(defaults)
    fields = dict(RUN_FIELDS)
    items = select_items(matrix, _wanted(args))
    # Plugin images are built once before the item loop. If every selected
    # row skips install, skip that whole-run rebuild too (quick smoke).
    if items and all(
        _as_bool(merge_item(matrix, it).get("skip_plugin_install", False))
        for it in items
    ):
        run_values["skip_plugin_install"] = True
        fields["skip_plugin_install"] = "SKIP_PLUGIN_INSTALL"
    lines = assignments(fields, run_values, expl)
    # Images are built once, before any item applies its own plugin list.
    if "PLUGIN_SMOKE_APPS" not in expl:
        names = union_plugins(matrix, items)
        if names:
            lines.append(f"PLUGIN_SMOKE_APPS={shlex.quote(' '.join(names))}")
    lines.append(f"SMOKE_MATRIX_FILE={shlex.quote(path)}")
    sys.stdout.write("\n".join(lines) + ("\n" if lines else ""))
    return 0


def cmd_apply_item(args: argparse.Namespace) -> int:
    path = resolve_matrix_path(args.root, args.matrix)
    matrix = load_matrix(path)
    wanted = _wanted(args)
    if len(wanted) != 1:
        raise MatrixError("apply-item requires exactly one --item")
    ident = wanted[0]
    items = select_items(matrix, [ident])
    item = items[0]
    merged = merge_item(matrix, item)
    expl = explicit_env()
    lines = assignments(ITEM_FIELDS, merged, expl)
    lines.append(f"SMOKE_MATRIX_ITEM={shlex.quote(str(item['id']))}")
    if "PLUGIN_SMOKE_APPS" not in expl and "plugins" in merged and merged["plugins"] is not None:
        plugins = _env_value("plugins", merged["plugins"])
        lines.append(f"PLUGIN_SMOKE_APPS_REQUESTED={shlex.quote(plugins)}")
    sys.stdout.write("\n".join(lines) + ("\n" if lines else ""))
    return 0


def cmd_max_inventory(args: argparse.Namespace) -> int:
    path = resolve_matrix_path(args.root, args.matrix)
    matrix = load_matrix(path)
    need = 1
    for it in select_items(matrix, _wanted(args)):
        merged = merge_item(matrix, it)
        for spec in topology_specs(merged):
            n = inventory_size(spec)
            if n > need:
                need = n
    sys.stdout.write(str(need) + "\n")
    return 0


def cmd_all_topologies(args: argparse.Namespace) -> int:
    path = resolve_matrix_path(args.root, args.matrix)
    matrix = load_matrix(path)
    specs: list[str] = []
    for it in select_items(matrix, _wanted(args)):
        for spec in topology_specs(merge_item(matrix, it)):
            if spec not in specs:
                specs.append(spec)
    sys.stdout.write(" ".join(specs) + "\n")
    return 0


def cmd_use_items(args: argparse.Namespace) -> int:
    if _wanted(args):
        sys.stdout.write("1\n")
        return 0
    expl = explicit_env()
    if "SMOKE_TOPOLOGIES" in expl or "CLUSTER_SIZE" in expl:
        sys.stdout.write("0\n")
        return 0
    sys.stdout.write("1\n")
    return 0


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument("--root", default=os.environ.get("FLYNN_ROOT", os.getcwd()))
    p.add_argument("--matrix", default=None, help="matrix file (overrides discovery)")
    p.add_argument(
        "--item",
        action="append",
        default=[],
        help="item id (repeat or comma-separated); before the subcommand is fine",
    )
    sub = p.add_subparsers(dest="cmd", required=True)

    sub.add_parser("snapshot-env", help="JSON of matrix-related env vars that are set")
    sub.add_parser("resolve", help="print the matrix file path")
    sub.add_parser("list", help="print items in the matrix")
    sub.add_parser("select", help="print selected item ids")
    sub.add_parser("apply-run", help="shell assignments for whole-run defaults")
    sub.add_parser("apply-item", help="shell assignments for one item")
    sub.add_parser("max-inventory", help="max Vagrant nodeN count for selected items")
    sub.add_parser("all-topologies", help="unique topology specs for selected items")
    sub.add_parser(
        "use-items",
        help="print 1 if the run should loop matrix items, 0 for env-driven topologies",
    )
    return p


def _flatten_items(args: argparse.Namespace) -> None:
    if getattr(args, "item", None):
        flat: list[str] = []
        for raw in args.item:
            flat.extend(parse_item_ids(raw))
        args.item = flat


def main(argv: list[str] | None = None) -> int:
    parser = build_parser()
    args = parser.parse_args(argv)
    _flatten_items(args)
    commands = {
        "snapshot-env": cmd_snapshot_env,
        "resolve": cmd_resolve,
        "list": cmd_list,
        "select": cmd_select,
        "apply-run": cmd_apply_run,
        "apply-item": cmd_apply_item,
        "max-inventory": cmd_max_inventory,
        "all-topologies": cmd_all_topologies,
        "use-items": cmd_use_items,
    }
    try:
        return commands[args.cmd](args)
    except MatrixError as e:
        sys.stderr.write(f"smoke-matrix: {e}\n")
        return 1


if __name__ == "__main__":
    sys.exit(main())
