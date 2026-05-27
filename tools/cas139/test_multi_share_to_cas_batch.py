#!/usr/bin/env python3
import importlib.util
import json
import tempfile
from pathlib import Path


ROOT = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location("multi", ROOT / "multi_share_to_cas_batch.py")
multi = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(multi)


def check(name, cond):
    if not cond:
        raise AssertionError(name)


check("tool kind", multi.TOOL_KIND == "multi-share")

with tempfile.TemporaryDirectory() as td:
    tmp = Path(td)
    files_a = tmp / "a_files.json"
    files_b = tmp / "b_files.json"
    files_a.write_text(json.dumps({"files": []}), encoding="utf-8")
    files_b.write_text(json.dumps({"files": []}), encoding="utf-8")

    manifest_json = tmp / "shares.json"
    manifest_json.write_text(json.dumps({
        "shares": [
            {"share_id": "share-a", "files_json": str(files_a), "progress": str(tmp / "a_progress.json")},
            {"share_id": "share-b", "files_json": str(files_b), "progress": str(tmp / "b_progress.json")},
        ]
    }), encoding="utf-8")

    shares = multi.load_share_manifest(manifest_json)
    check("loads json shares", [s["share_id"] for s in shares] == ["share-a", "share-b"])

    manifest_jsonl = tmp / "shares.jsonl"
    manifest_jsonl.write_text(
        json.dumps({"share_id": "share-a", "files_json": str(files_a)}) + "\n"
        + json.dumps({"share_id": "share-b", "files_json": str(files_b)}) + "\n",
        encoding="utf-8",
    )
    shares = multi.load_share_manifest(manifest_jsonl)
    check("loads jsonl shares", [s["share_id"] for s in shares] == ["share-a", "share-b"])

    args = multi.build_single_args(
        multi.build_arg_parser().parse_args(["--manifest", str(manifest_json), "--dry-run", "--workers", "2"]),
        shares[0],
    )
    check("single args include share id", args[:4] == ["--share-id", "share-a", "--files-json", str(files_a)])
    check("single args include dry run", "--dry-run" in args)
    check("single args include workers", args[args.index("--workers") + 1] == "2")

    rc = multi.main(["--manifest", str(manifest_json), "--dry-run"])
    check("dry-run exits zero", rc == 0)

print("self-test ok")
