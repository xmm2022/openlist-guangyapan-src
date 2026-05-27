#!/usr/bin/env python3
import argparse
import concurrent.futures
import json
import os
import queue
import sys
import threading
import time
from pathlib import Path


CURRENT_DIR = Path(__file__).resolve().parent
if str(CURRENT_DIR) not in sys.path:
    sys.path.insert(0, str(CURRENT_DIR))

import cas_common  # noqa: E402


TOOL_KIND = "single-share-multi-file"
DEFAULT_PROGRESS_PATH = "cas139_single_share_progress.json"
DEFAULT_TMP_DIR_NAME = "__openlist_cas_import_tmp"
DEFAULT_OUT_DIR_NAME = "__openlist_cas_import_out"
VIDEO_EXTENSIONS = {
    ".mkv",
    ".mp4",
    ".iso",
    ".ts",
    ".m2ts",
    ".avi",
    ".mpeg",
    ".mpg",
    ".flv",
    ".rmvb",
    ".mov",
    ".wmv",
    ".webm",
    ".m4v",
    ".vob",
}


def parse_size(value):
    raw = str(value).strip()
    if not raw:
        raise ValueError("empty size")
    units = [
        ("tib", 1024**4), ("tb", 1000**4),
        ("gib", 1024**3), ("gb", 1000**3),
        ("mib", 1024**2), ("mb", 1000**2),
        ("kib", 1024), ("kb", 1000),
        ("b", 1),
    ]
    lower = raw.lower().replace(" ", "")
    for suffix, factor in units:
        if lower.endswith(suffix):
            return int(float(lower[:-len(suffix)]) * factor)
    return int(float(lower))


def fmt_size(n):
    n = int(n or 0)
    for unit, factor in [("TiB", 1024**4), ("GiB", 1024**3), ("MiB", 1024**2)]:
        if abs(n) >= factor:
            return f"{n / factor:.2f} {unit}"
    return f"{n} B"


def is_video_resource(item):
    full_name = item.get("_fullName") or item.get("fullName") or item.get("coName") or ""
    return os.path.splitext(full_name.lower())[1] in VIDEO_EXTENSIONS


def load_files(files_json):
    with open(files_json, "r", encoding="utf-8") as f:
        data = json.load(f)
    files = []
    for index, item in enumerate(data.get("files") or []):
        try:
            size = int(item.get("coSize") or 0)
        except Exception:
            size = 0
        if size <= 0:
            continue
        full_name = item.get("fullName") or item.get("coName") or item.get("path") or f"file-{index}"
        clone = dict(item)
        clone["_index"] = index
        clone["_size"] = size
        clone["_fullName"] = full_name
        files.append(clone)
    return files


def load_progress(path=DEFAULT_PROGRESS_PATH):
    if not os.path.exists(path):
        return {"done": {}, "failed": {}, "skipped": {}, "runs": []}
    with open(path, "r", encoding="utf-8") as f:
        data = json.load(f)
    data.setdefault("done", {})
    data.setdefault("failed", {})
    data.setdefault("skipped", {})
    data.setdefault("runs", [])
    return data


def save_progress(progress, path=DEFAULT_PROGRESS_PATH):
    tmp = path + ".tmp"
    with open(tmp, "w", encoding="utf-8") as f:
        json.dump(progress, f, ensure_ascii=False, indent=2)
    os.replace(tmp, path)


def processed_paths(progress, retry_failed=False):
    paths = set(progress.get("done", {}).keys())
    paths.update(progress.get("skipped", {}).keys())
    if not retry_failed:
        paths.update(progress.get("failed", {}).keys())
    return paths


def record_result(progress, key, result):
    status = result.get("status") or ""
    if status.startswith("skipped"):
        progress.setdefault("skipped", {})[key] = result
        progress.setdefault("done", {}).pop(key, None)
        progress.setdefault("failed", {}).pop(key, None)
        return "skipped"
    progress.setdefault("done", {})[key] = result
    progress.setdefault("failed", {}).pop(key, None)
    progress.setdefault("skipped", {}).pop(key, None)
    return "done"


def select_batch(
    files,
    done_paths,
    max_batch_bytes,
    limit,
    allow_single_over_cap,
    max_file_bytes=0,
    include_non_video=False,
):
    selected = []
    total = 0
    for item in files:
        key = item["path"]
        if key in done_paths:
            continue
        if not include_non_video and not is_video_resource(item):
            continue
        size = int(item.get("_size", item.get("coSize") or 0))
        if max_file_bytes and size > max_file_bytes:
            continue
        if selected and total + size > max_batch_bytes:
            break
        if not selected and size > max_batch_bytes and not allow_single_over_cap:
            continue
        selected.append(item)
        total += size
        if limit and len(selected) >= limit:
            break
    return selected, total


def order_files(files, order):
    if order == "smallest":
        return sorted(files, key=lambda x: (int(x["_size"]), int(x["_index"])))
    if order == "largest":
        return sorted(files, key=lambda x: (-int(x["_size"]), int(x["_index"])))
    return sorted(files, key=lambda x: int(x["_index"]))


def usable_sha256(item):
    sha256 = (item.get("contentHash") or "").lower()
    algo = item.get("contentHashAlgorithm") or ""
    if algo.lower() == "sha256" and len(sha256) == 64:
        return sha256
    return ""


def worker_tmp_name(base_name, worker_id, workers):
    if workers <= 1:
        return base_name
    return f"{base_name}_w{worker_id}"


def clean_tmp_folder(yun, tmp_id):
    items = yun.list_folder(tmp_id)
    files = [item for item in items if item.get("type") == "file"]
    for item in files:
        yun.delete_permanently(item.get("fileId"))
    return len(files)


def clean_all_tmp_folders(yun, workers, tmp_dir_name):
    total = 0
    for worker_id in range(1, max(1, workers) + 1):
        tmp = yun.find_child(yun.root_folder_id, worker_tmp_name(tmp_dir_name, worker_id, workers), "folder")
        if tmp:
            total += clean_tmp_folder(yun, tmp["fileId"])
    return total


def temp_source_left(yun, tmp_id, name, size):
    return [
        x for x in yun.list_folder(tmp_id)
        if x.get("type") == "file" and x.get("name") == name and int(x.get("size") or 0) == size
    ]


def delete_temp_source_and_wait(yun, file_id, tmp_id, name, size, checks=6, delay=2):
    yun.delete_permanently(file_id)
    left = []
    for attempt in range(max(1, checks)):
        left = temp_source_left(yun, tmp_id, name, size)
        if not left:
            return True
        if attempt + 1 < checks and delay:
            time.sleep(delay)
    return False


def wait_task_done(yun, task_id, timeout, share_host):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            q = cas_common.query_share_transfer(yun, task_id, share_host)
            detail = q.get("data", {}).get("queryBatchOprTaskDetailRes") or q.get("data", {})
            task = detail.get("batchOprTask") if isinstance(detail, dict) else None
            if task:
                last = task
                status = str(task.get("taskStatus"))
                progress = str(task.get("progress") or "")
                result = str(task.get("taskResultCode") or "")
                if status in {"2", "3", "4"} or progress == "100" or result:
                    return task
        except Exception as e:
            last = {"query_error": str(e)}
        time.sleep(2)
    raise RuntimeError(f"task timeout: task_id={task_id} last={last}")


def already_has_cas(yun, parent_id, cas_name):
    return yun.find_child(parent_id, cas_name, "file") is not None


def process_one(yun, item, tmp_id, tmp_name, out_root_id, owner_account, args):
    size = int(item["_size"])
    full_name = item["_fullName"]
    name = item["coName"]
    cas_name = name + ".cas"
    parent_parts = [p for p in full_name.split("/")[:-1] if p]
    output_lock = getattr(args, "output_lock", None)
    if output_lock:
        with output_lock:
            out_parent_id = yun.ensure_path(out_root_id, parent_parts)
            cas_exists = already_has_cas(yun, out_parent_id, cas_name)
    else:
        out_parent_id = yun.ensure_path(out_root_id, parent_parts)
        cas_exists = already_has_cas(yun, out_parent_id, cas_name)

    if cas_exists:
        removed = clean_tmp_folder(yun, tmp_id)
        if removed:
            print(f"  cleaned tmp leftovers: {removed}", flush=True)
        return {
            "status": "already_exists",
            "fullName": full_name,
            "size": size,
            "casPath": args.out_dir_name + "/" + "/".join(parent_parts + [cas_name]),
        }

    removed = clean_tmp_folder(yun, tmp_id)
    if removed:
        print(f"  cleaned tmp leftovers: {removed}", flush=True)

    task_id, _ = cas_common.create_share_transfer(yun, args.share_id, item, tmp_id, tmp_name, owner_account, args.share_host)
    task = wait_task_done(yun, task_id, args.task_timeout, args.share_host)
    print(f"  task={task_id} status={task.get('taskStatus')} progress={task.get('progress')}", flush=True)

    transferred = cas_common.wait_for_file(yun, tmp_id, name, size, timeout=args.file_timeout)
    sha256 = usable_sha256(transferred)
    algo = transferred.get("contentHashAlgorithm") or ""
    content_hash = (transferred.get("contentHash") or "").lower()
    if not sha256:
        if not delete_temp_source_and_wait(yun, transferred.get("fileId"), tmp_id, name, size):
            left = temp_source_left(yun, tmp_id, name, size)
            raise RuntimeError(f"temp source still exists after md5-only skip delete: {len(left)}")
        return {
            "status": "skipped_no_sha256",
            "fullName": full_name,
            "size": size,
            "contentHashAlgorithm": algo,
            "contentHash": content_hash,
            "skipReason": "transfer returned no usable SHA256",
        }

    cas_content = cas_common.encode_cas(name, size, sha256)
    cas_file_id, cas_sha256, cas_upload = yun.upload_small(out_parent_id, cas_name, cas_content)
    if not cas_file_id:
        found = yun.find_child(out_parent_id, cas_name, "file")
        cas_file_id = found.get("fileId") if found else ""
    if args.verify_restore and size <= args.verify_restore_max_bytes:
        restore_name = f"__cas_restore_probe_{int(time.time())}_{name}"
        restored = yun.create_by_sha256(tmp_id, restore_name, size, sha256, "auto_rename")
        if restored.get("partInfos"):
            raise RuntimeError("restore probe did not rapid-upload; refusing to delete source")
        yun.delete_permanently(restored.get("fileId"))
    elif args.verify_restore:
        print(f"  skip restore probe above {fmt_size(args.verify_restore_max_bytes)}", flush=True)

    if not delete_temp_source_and_wait(yun, transferred.get("fileId"), tmp_id, name, size):
        left = temp_source_left(yun, tmp_id, name, size)
        raise RuntimeError(f"temp source still exists after delete: {len(left)}")

    return {
        "status": "done",
        "fullName": full_name,
        "size": size,
        "sha256": sha256,
        "casSha256": cas_sha256,
        "casFileId": cas_file_id,
        "casPath": args.out_dir_name + "/" + "/".join(parent_parts + [cas_name]),
        "taskID": task_id,
        "casUpload": bool(cas_upload),
    }


def build_arg_parser():
    p = argparse.ArgumentParser(
        description="Single 139 share link, many files: convert share files to OpenList .cas via temporary transfer."
    )
    p.add_argument("--share-id", required=True, help="139 share ID/linkID. This tool handles one share link per run.")
    p.add_argument("--files-json", required=True, help="JSON captured from the share listing; must contain a top-level files array.")
    p.add_argument("--owner-general-json", default="", help="Optional getOutLinkGeneral JSON used to extract ownerUserId.")
    p.add_argument("--db-path", default=cas_common.DEFAULT_DB_PATH, help="OpenList data.db path.")
    p.add_argument("--storage-id", type=int, default=0, help="Optional OpenList 139Yun storage id.")
    p.add_argument("--mount-path", default="", help="Optional OpenList 139Yun mount_path selector.")
    p.add_argument("--share-host", default=cas_common.DEFAULT_SHARE_HOST)
    p.add_argument("--tmp-dir-name", default=DEFAULT_TMP_DIR_NAME)
    p.add_argument("--out-dir-name", default=DEFAULT_OUT_DIR_NAME)
    p.add_argument("--max-batch-size", default="500GiB", help="Strict total selected source size cap, e.g. 1GiB, 500GiB.")
    p.add_argument("--max-file-size", default="500GB", help="Skip files larger than this PC-client-compatible per-file cap.")
    p.add_argument("--limit", type=int, default=0, help="Max file count for this run; 0 means no count cap.")
    p.add_argument("--order", choices=["input", "smallest", "largest"], default="input")
    p.add_argument("--progress", default=DEFAULT_PROGRESS_PATH)
    p.add_argument("--allow-single-over-cap", action="store_true", help="Process one file even if it exceeds max batch size.")
    p.add_argument("--include-non-video", action="store_true", help="Also process metadata/subtitle/image files. Default is video containers only.")
    p.add_argument("--retry-failed", action="store_true", help="Retry entries already recorded in progress.failed. Default skips them.")
    p.add_argument("--workers", type=int, default=1, help="Number of parallel transfer workers. Each worker uses an isolated temp folder.")
    p.add_argument("--verify-restore", action="store_true", help="Probe SHA256 rapid restore before deleting each temp source.")
    p.add_argument("--verify-restore-max-size", default="50GB", help="Do not run restore probe above this non-PC endpoint cap.")
    p.add_argument("--task-timeout", type=int, default=900)
    p.add_argument("--file-timeout", type=int, default=180)
    p.add_argument("--dry-run", action="store_true")
    return p


def make_worker_context(worker_id, workers, storage, args):
    yun = cas_common.Yun139(storage["auth"], storage["account"], storage["root_folder_id"])
    yun.route()
    tmp_name = worker_tmp_name(args.tmp_dir_name, worker_id, workers)
    tmp_id = yun.ensure_folder(yun.root_folder_id, tmp_name)
    out_root_id = yun.ensure_folder(yun.root_folder_id, args.out_dir_name)
    clean_tmp_folder(yun, tmp_id)
    return yun, tmp_id, tmp_name, out_root_id


def run_worker(worker_id, workers, work_q, result_q, stop_event, storage, owner_account, args):
    try:
        yun, tmp_id, tmp_name, out_root_id = make_worker_context(worker_id, workers, storage, args)
        while not stop_event.is_set():
            try:
                idx, item = work_q.get_nowait()
            except queue.Empty:
                break
            try:
                print(f"[w{worker_id} {idx}/{args.selected_count}] {fmt_size(item['_size'])} {item['_fullName']}", flush=True)
                result = process_one(yun, item, tmp_id, tmp_name, out_root_id, owner_account, args)
                result_q.put(("result", worker_id, idx, item, result))
            except Exception as e:
                stop_event.set()
                result_q.put(("error", worker_id, idx, item, str(e)))
                break
            finally:
                work_q.task_done()
        clean_tmp_folder(yun, tmp_id)
    except Exception as e:
        stop_event.set()
        result_q.put(("worker_error", worker_id, 0, None, str(e)))


def print_plan(files, progress, selected, selected_bytes, args):
    total_bytes = sum(int(f["_size"]) for f in files)
    video_files = [f for f in files if is_video_resource(f)]
    video_bytes = sum(int(f["_size"]) for f in video_files)
    print(
        f"tool={TOOL_KIND} share={args.share_id} files={len(files)} total={fmt_size(total_bytes)} "
        f"videoFiles={len(video_files)} videoTotal={fmt_size(video_bytes)} done={len(progress['done'])} "
        f"failed={len(progress['failed'])} skipped={len(progress['skipped'])} "
        f"selected={len(selected)} selectedBytes={fmt_size(selected_bytes)} "
        f"includeNonVideo={args.include_non_video} retryFailed={args.retry_failed} "
        f"workers={args.workers} order={args.order}",
        flush=True,
    )
    for i, item in enumerate(selected[:10], 1):
        print(f"  plan {i}: {fmt_size(item['_size'])} {item['_fullName']}", flush=True)
    if len(selected) > 10:
        print(f"  ... {len(selected) - 10} more", flush=True)


def main(argv=None):
    args = build_arg_parser().parse_args(argv)
    if args.workers < 1:
        raise SystemExit("--workers must be >= 1")
    max_batch_bytes = parse_size(args.max_batch_size)
    max_file_bytes = parse_size(args.max_file_size) if args.max_file_size else 0
    args.verify_restore_max_bytes = parse_size(args.verify_restore_max_size)
    files = order_files(load_files(args.files_json), args.order)
    progress = load_progress(args.progress)
    done = processed_paths(progress, retry_failed=args.retry_failed)
    selected, selected_bytes = select_batch(
        files,
        done,
        max_batch_bytes,
        args.limit,
        args.allow_single_over_cap,
        max_file_bytes,
        include_non_video=args.include_non_video,
    )

    print_plan(files, progress, selected, selected_bytes, args)
    if not selected:
        print("nothing selected", flush=True)
        return 0
    if args.dry_run:
        return 0

    storage = cas_common.load_storage(args.db_path, args.storage_id, args.mount_path)
    owner_account = cas_common.load_owner_account(args.owner_general_json)
    yun = cas_common.Yun139(storage["auth"], storage["account"], storage["root_folder_id"])
    yun.route()
    tmp_id = yun.ensure_folder(yun.root_folder_id, worker_tmp_name(args.tmp_dir_name, 1, args.workers))
    out_root_id = yun.ensure_folder(yun.root_folder_id, args.out_dir_name)

    run = {
        "tool": TOOL_KIND,
        "shareID": args.share_id,
        "startedAt": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        "maxBatchSize": args.max_batch_size,
        "order": args.order,
        "selected": len(selected),
        "selectedBytes": selected_bytes,
        "includeNonVideo": args.include_non_video,
        "retryFailed": args.retry_failed,
        "workers": args.workers,
    }
    progress["runs"].append(run)
    save_progress(progress, args.progress)

    completed = 0
    completed_bytes = 0
    skipped = 0
    skipped_bytes = 0
    args.selected_count = len(selected)
    args.output_lock = threading.Lock()

    if args.workers == 1:
        tmp_name = worker_tmp_name(args.tmp_dir_name, 1, args.workers)
        for idx, item in enumerate(selected, 1):
            key = item["path"]
            if key in processed_paths(progress, retry_failed=args.retry_failed):
                continue
            print(f"[{idx}/{len(selected)}] {fmt_size(item['_size'])} {item['_fullName']}", flush=True)
            try:
                result = process_one(yun, item, tmp_id, tmp_name, out_root_id, owner_account, args)
                result["completedAt"] = time.strftime("%Y-%m-%dT%H:%M:%S%z")
                result["index"] = item["_index"]
                bucket = record_result(progress, key, result)
                if bucket == "skipped":
                    skipped += 1
                    skipped_bytes += int(item["_size"])
                    print(f"  skipped: {result['status']} algo={result.get('contentHashAlgorithm')} hash={result.get('contentHash')}", flush=True)
                else:
                    completed += 1
                    completed_bytes += int(item["_size"])
                    print(f"  ok: {result['status']} -> {result['casPath']}", flush=True)
            except Exception as e:
                progress["failed"][key] = {
                    "fullName": item["_fullName"],
                    "size": int(item["_size"]),
                    "error": str(e),
                    "failedAt": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
                }
                save_progress(progress, args.progress)
                print(f"  ERROR: {e}", file=sys.stderr, flush=True)
                return 1
            save_progress(progress, args.progress)
        clean_tmp_folder(yun, tmp_id)
    else:
        work_q = queue.Queue()
        result_q = queue.Queue()
        stop_event = threading.Event()
        queued = 0
        for idx, item in enumerate(selected, 1):
            if item["path"] in processed_paths(progress, retry_failed=args.retry_failed):
                continue
            work_q.put((idx, item))
            queued += 1
        print(f"parallel start: workers={args.workers} queued={queued}", flush=True)
        threads = [
            threading.Thread(
                target=run_worker,
                args=(worker_id, args.workers, work_q, result_q, stop_event, storage, owner_account, args),
                daemon=False,
            )
            for worker_id in range(1, args.workers + 1)
        ]
        for t in threads:
            t.start()
        finished = 0
        expected = queued
        while finished < expected:
            try:
                kind, worker_id, idx, item, payload = result_q.get(timeout=2)
            except queue.Empty:
                if all(not t.is_alive() for t in threads):
                    break
                continue
            if kind == "result":
                key = item["path"]
                result = payload
                result["completedAt"] = time.strftime("%Y-%m-%dT%H:%M:%S%z")
                result["index"] = item["_index"]
                result["worker"] = worker_id
                bucket = record_result(progress, key, result)
                if bucket == "skipped":
                    skipped += 1
                    skipped_bytes += int(item["_size"])
                    print(f"[w{worker_id} {idx}/{len(selected)}] skipped: {result['status']} algo={result.get('contentHashAlgorithm')} hash={result.get('contentHash')}", flush=True)
                else:
                    completed += 1
                    completed_bytes += int(item["_size"])
                    print(f"[w{worker_id} {idx}/{len(selected)}] ok: {result['status']} -> {result['casPath']}", flush=True)
                save_progress(progress, args.progress)
                finished += 1
            else:
                if item is not None:
                    progress["failed"][item["path"]] = {
                        "fullName": item["_fullName"],
                        "size": int(item["_size"]),
                        "error": payload,
                        "failedAt": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
                        "worker": worker_id,
                    }
                save_progress(progress, args.progress)
                print(f"[w{worker_id} {idx}/{len(selected) if item is not None else '?'}] ERROR: {payload}", file=sys.stderr, flush=True)
                stop_event.set()
                break
        for t in threads:
            t.join()
        if stop_event.is_set():
            return 1

    clean_all_tmp_folders(yun, args.workers, args.tmp_dir_name)
    run["finishedAt"] = time.strftime("%Y-%m-%dT%H:%M:%S%z")
    run["completed"] = completed
    run["completedBytes"] = completed_bytes
    run["skipped"] = skipped
    run["skippedBytes"] = skipped_bytes
    save_progress(progress, args.progress)
    print(
        f"batch complete: files={completed} bytes={fmt_size(completed_bytes)} "
        f"skipped={skipped} skippedBytes={fmt_size(skipped_bytes)} progress={args.progress}",
        flush=True,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
