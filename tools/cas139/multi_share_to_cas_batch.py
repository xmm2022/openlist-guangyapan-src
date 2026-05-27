#!/usr/bin/env python3
import argparse
import json
import re
import sys
from pathlib import Path


CURRENT_DIR = Path(__file__).resolve().parent
if str(CURRENT_DIR) not in sys.path:
    sys.path.insert(0, str(CURRENT_DIR))

import single_share_to_cas_batch  # noqa: E402


TOOL_KIND = "multi-share"


def load_share_manifest(path):
    path = Path(path)
    if path.suffix.lower() == ".jsonl":
        shares = []
        with path.open("r", encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if line:
                    shares.append(json.loads(line))
    else:
        with path.open("r", encoding="utf-8") as f:
            data = json.load(f)
        if isinstance(data, list):
            shares = data
        else:
            shares = data.get("shares") or []
    if not isinstance(shares, list):
        raise ValueError("manifest must be a JSON list, a {\"shares\": [...]} object, or JSONL records")
    out = []
    for index, item in enumerate(shares, 1):
        if not isinstance(item, dict):
            raise ValueError(f"share #{index} must be an object")
        share_id = item.get("share_id") or item.get("shareId") or item.get("linkID") or item.get("link_id")
        files_json = item.get("files_json") or item.get("filesJson")
        if not share_id:
            raise ValueError(f"share #{index} missing share_id")
        if not files_json:
            raise ValueError(f"share #{index} missing files_json")
        normalized = dict(item)
        normalized["share_id"] = str(share_id)
        normalized["files_json"] = str(files_json)
        out.append(normalized)
    return out


def safe_name(value):
    value = re.sub(r"[^A-Za-z0-9_.-]+", "_", str(value).strip())
    return value.strip("._-") or "share"


def add_arg(args, name, value):
    if value is None or value == "":
        return
    args.extend([name, str(value)])


def add_bool(args, name, enabled):
    if enabled:
        args.append(name)


def build_single_args(global_args, share):
    share_id = share["share_id"]
    safe_share = safe_name(share_id)
    args = ["--share-id", share_id, "--files-json", share["files_json"]]

    add_arg(args, "--owner-general-json", share.get("owner_general_json") or share.get("ownerGeneralJson") or global_args.owner_general_json)
    add_arg(args, "--db-path", global_args.db_path)
    add_arg(args, "--storage-id", share.get("storage_id") or share.get("storageId") or global_args.storage_id)
    add_arg(args, "--mount-path", share.get("mount_path") or share.get("mountPath") or global_args.mount_path)
    add_arg(args, "--share-host", share.get("share_host") or share.get("shareHost") or global_args.share_host)
    add_arg(args, "--tmp-dir-name", share.get("tmp_dir_name") or share.get("tmpDirName") or f"{global_args.tmp_dir_prefix}_{safe_share}")
    add_arg(args, "--out-dir-name", share.get("out_dir_name") or share.get("outDirName") or global_args.out_dir_name)
    add_arg(args, "--max-batch-size", share.get("max_batch_size") or share.get("maxBatchSize") or global_args.max_batch_size)
    add_arg(args, "--max-file-size", share.get("max_file_size") or share.get("maxFileSize") or global_args.max_file_size)
    add_arg(args, "--limit", share.get("limit") if share.get("limit") is not None else global_args.limit)
    add_arg(args, "--order", share.get("order") or global_args.order)
    add_arg(args, "--progress", share.get("progress") or f"{global_args.progress_dir.rstrip('/')}/cas139_{safe_share}_progress.json")
    add_arg(args, "--workers", share.get("workers") or global_args.workers)
    add_arg(args, "--verify-restore-max-size", share.get("verify_restore_max_size") or share.get("verifyRestoreMaxSize") or global_args.verify_restore_max_size)
    add_arg(args, "--task-timeout", share.get("task_timeout") or share.get("taskTimeout") or global_args.task_timeout)
    add_arg(args, "--file-timeout", share.get("file_timeout") or share.get("fileTimeout") or global_args.file_timeout)

    add_bool(args, "--allow-single-over-cap", bool(share.get("allow_single_over_cap") or share.get("allowSingleOverCap") or global_args.allow_single_over_cap))
    add_bool(args, "--include-non-video", bool(share.get("include_non_video") or share.get("includeNonVideo") or global_args.include_non_video))
    add_bool(args, "--retry-failed", bool(share.get("retry_failed") or share.get("retryFailed") or global_args.retry_failed))
    add_bool(args, "--verify-restore", bool(share.get("verify_restore") or share.get("verifyRestore") or global_args.verify_restore))
    add_bool(args, "--dry-run", global_args.dry_run)
    return args


def build_arg_parser():
    p = argparse.ArgumentParser(description="Multiple 139 share links: run single-share CAS conversion for each share manifest entry.")
    p.add_argument("--manifest", required=True, help="JSON/JSONL manifest containing share_id and files_json for each share.")
    p.add_argument("--stop-on-error", action="store_true", help="Stop at first failed share. Default continues.")
    p.add_argument("--owner-general-json", default="")
    p.add_argument("--db-path", default=single_share_to_cas_batch.cas_common.DEFAULT_DB_PATH)
    p.add_argument("--storage-id", type=int, default=0)
    p.add_argument("--mount-path", default="")
    p.add_argument("--share-host", default=single_share_to_cas_batch.cas_common.DEFAULT_SHARE_HOST)
    p.add_argument("--tmp-dir-prefix", default="__openlist_cas_import_tmp")
    p.add_argument("--out-dir-name", default=single_share_to_cas_batch.DEFAULT_OUT_DIR_NAME)
    p.add_argument("--max-batch-size", default="500GiB")
    p.add_argument("--max-file-size", default="500GB")
    p.add_argument("--limit", type=int, default=0)
    p.add_argument("--order", choices=["input", "smallest", "largest"], default="input")
    p.add_argument("--progress-dir", default=".")
    p.add_argument("--allow-single-over-cap", action="store_true")
    p.add_argument("--include-non-video", action="store_true")
    p.add_argument("--retry-failed", action="store_true")
    p.add_argument("--workers", type=int, default=1)
    p.add_argument("--verify-restore", action="store_true")
    p.add_argument("--verify-restore-max-size", default="50GB")
    p.add_argument("--task-timeout", type=int, default=900)
    p.add_argument("--file-timeout", type=int, default=180)
    p.add_argument("--dry-run", action="store_true")
    return p


def main(argv=None):
    args = build_arg_parser().parse_args(argv)
    shares = load_share_manifest(args.manifest)
    print(f"tool={TOOL_KIND} shares={len(shares)} dryRun={args.dry_run}", flush=True)
    failures = 0
    for index, share in enumerate(shares, 1):
        single_args = build_single_args(args, share)
        print(f"[{index}/{len(shares)}] share={share['share_id']} files={share['files_json']}", flush=True)
        rc = single_share_to_cas_batch.main(single_args)
        if rc != 0:
            failures += 1
            print(f"[{index}/{len(shares)}] ERROR share={share['share_id']} rc={rc}", file=sys.stderr, flush=True)
            if args.stop_on_error:
                return rc
    return 1 if failures else 0


if __name__ == "__main__":
    raise SystemExit(main())
