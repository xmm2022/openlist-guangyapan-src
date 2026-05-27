#!/usr/bin/env python3
import base64
import importlib.util
import json
from pathlib import Path


ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("batch", ROOT / "single_share_to_cas_batch.py")
batch = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(batch)


def check(name, cond):
    if not cond:
        raise AssertionError(name)


check("tool kind", batch.TOOL_KIND == "single-share-multi-file")
check("parse gib", batch.parse_size("2GiB") == 2 * 1024**3)
check("parse gb", batch.parse_size("2GB") == 2 * 1000**3)
check("parse bytes", batch.parse_size("123") == 123)

files = [
    {"path": "a", "coSize": 100, "fullName": "a.mkv"},
    {"path": "b", "coSize": 200, "fullName": "b.mp4"},
    {"path": "c", "coSize": 300, "fullName": "c.iso"},
]
selected, selected_bytes = batch.select_batch(files, set(), 250, 0, False)
check("select stops at cap", [f["path"] for f in selected] == ["a"])
check("select bytes", selected_bytes == 100)
selected, _ = batch.select_batch(files, {"a"}, 250, 0, False)
check("select skips done", [f["path"] for f in selected] == ["b"])
selected, _ = batch.select_batch(files, set(), 50, 0, True)
check("single over cap", [f["path"] for f in selected] == ["a"])

mixed_files = [
    {"path": "nfo", "coSize": 1, "fullName": "show/history.E09.nfo"},
    {"path": "sub", "coSize": 1, "fullName": "show/history.E09.ass"},
    {"path": "video", "coSize": 100, "fullName": "show/history.E09.mkv"},
]
check("nfo is not video", not batch.is_video_resource(mixed_files[0]))
check("subtitle is not video", not batch.is_video_resource(mixed_files[1]))
check("mkv is video", batch.is_video_resource(mixed_files[2]))
selected, selected_bytes = batch.select_batch(mixed_files, set(), 200, 0, False)
check("select skips non-video by default", [f["path"] for f in selected] == ["video"])
check("select video bytes only", selected_bytes == 100)

sha256_item = {"contentHashAlgorithm": "SHA256", "contentHash": "A" * 64}
md5_item = {"contentHashAlgorithm": "md5", "contentHash": "b" * 32}
check("usable sha256 lowercases hash", batch.usable_sha256(sha256_item) == "a" * 64)
check("md5 is not usable sha256", batch.usable_sha256(md5_item) == "")

progress = {"done": {"old": {}}, "failed": {"md5": {}}, "skipped": {}, "runs": []}
bucket = batch.record_result(progress, "md5", {"status": "skipped_no_sha256", "fullName": "x.mkv"})
check("skipped result bucket", bucket == "skipped")
check("skipped result not failed", "md5" not in progress["failed"])
check("skipped result stored", progress["skipped"]["md5"]["status"] == "skipped_no_sha256")
check("processed paths include skipped", "md5" in batch.processed_paths(progress, retry_failed=False))

check("single worker uses legacy tmp", batch.worker_tmp_name("__tmp", 1, 1) == "__tmp")
check("multi worker uses isolated tmp", batch.worker_tmp_name("__tmp", 2, 3) == "__tmp_w2")
check("workers arg parses", batch.build_arg_parser().parse_args(["--share-id", "abc", "--files-json", "files.json", "--workers", "3"]).workers == 3)


class DelayedDeleteYun:
    def __init__(self):
        self.deleted = []
        self.list_calls = 0

    def delete_permanently(self, file_id):
        self.deleted.append(file_id)

    def list_folder(self, folder_id):
        self.list_calls += 1
        if self.list_calls == 1:
            return [{"type": "file", "name": "stale.mkv", "size": 123}]
        return []


delayed = DelayedDeleteYun()
check(
    "delete wait tolerates stale listing",
    batch.delete_temp_source_and_wait(delayed, "file-1", "tmp", "stale.mkv", 123, checks=2, delay=0),
)
check("delete called once", delayed.deleted == ["file-1"])

cas = batch.cas_common.encode_cas("x.txt", 12, "a" * 64)
decoded = json.loads(base64.b64decode(cas).decode("utf-8"))
check("cas name", decoded["name"] == "x.txt")
check("cas sha256", decoded["sha256"] == "a" * 64)

yun = batch.cas_common.Yun139("auth", "account", "/")
parts = yun.part_infos(31 * 1024**3)
check("large part size", parts[0]["partSize"] == 512 * 1024**2)
check("large first 100 cap source", len(parts) == 62)
check("large part offset", parts[1]["parallelHashCtx"]["partOffset"] == 512 * 1024**2)

print("self-test ok")
