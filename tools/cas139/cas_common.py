#!/usr/bin/env python3
import base64
import hashlib
import json
import os
import random
import sqlite3
import string
import time
import urllib.error
import urllib.parse
import urllib.request


DEFAULT_DB_PATH = "/opt/openlist/data/data.db"
DEFAULT_SHARE_HOST = "https://share-kd-njs.yun.139.com/yun-share"


def dumps(obj):
    return json.dumps(obj, ensure_ascii=False, separators=(",", ":"))


def md5_hex(s):
    return hashlib.md5(s.encode("utf-8")).hexdigest()


def cal_sign(body, ts, rand_str):
    encoded = urllib.parse.quote(body, safe="-_.!~*'()")
    sorted_body = "".join(sorted(encoded))
    b64 = base64.b64encode(sorted_body.encode("utf-8")).decode("ascii")
    return md5_hex(md5_hex(b64) + md5_hex(ts + ":" + rand_str)).upper()


def rand16():
    alphabet = string.ascii_letters + string.digits
    return "".join(random.choice(alphabet) for _ in range(16))


def http_json(url, body, auth, family=False, personal=False, timeout=40):
    body_s = dumps(body)
    body_b = body_s.encode("utf-8")
    ts = time.strftime("%Y-%m-%d %H:%M:%S", time.localtime())
    r = rand16()
    sign = cal_sign(body_s, ts, r)
    svc_type = "2" if family else "1"

    headers = {
        "Accept": "application/json, text/plain, */*",
        "Content-Type": "application/json;charset=UTF-8",
        "Authorization": "Basic " + auth,
        "CMS-DEVICE": "default",
        "Cms-Device": "default",
        "Origin": "https://yun.139.com",
        "Referer": "https://yun.139.com/",
        "User-Agent": "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0 Safari/537.36",
        "x-DeviceInfo": "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||",
        "x-huawei-channelSrc": "10000034",
        "x-inner-ntwk": "2",
        "x-m4c-caller": "PC",
        "x-m4c-src": "10002",
        "x-SvcType": svc_type,
        "Mcloud-Sign": f"{ts},{r},{sign}",
        "mcloud-sign": f"{ts},{r},{sign}",
        "Mcloud-Channel": "1000101",
        "mcloud-channel": "1000101",
        "Mcloud-Client": "10701",
        "mcloud-client": "10701",
        "Mcloud-Version": "7.14.0",
        "mcloud-version": "7.14.0",
        "Inner-Hcy-Router-Https": "1",
        "INNER-HCY-ROUTER-HTTPS": "1",
        "x-yun-api-version": "v1",
        "X-Yun-Api-Version": "v1",
    }
    if personal:
        headers.update({
            "Caller": "web",
            "Mcloud-Route": "001",
            "mcloud-route": "001",
            "X-Yun-App-Channel": "10000034",
            "X-Yun-Channel-Source": "10000034",
            "X-Yun-Client-Info": "||9|7.14.0|chrome|120.0.0.0|||windows 10||zh-CN|||dW5kZWZpbmVk||",
            "X-Yun-Module-Type": "100",
            "X-Yun-Svc-Type": "1",
            "x-yun-app-channel": "10000034",
            "x-yun-channel-source": "10000034",
            "x-yun-module-type": "100",
            "x-yun-svc-type": "1",
        })

    req = urllib.request.Request(url, data=body_b, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            text = resp.read().decode("utf-8", "replace")
            return json.loads(text), text
    except urllib.error.HTTPError as e:
        text = e.read().decode("utf-8", "replace")
        raise RuntimeError(f"HTTP {e.code} {url}: {text[:1000]}")
    except urllib.error.URLError as e:
        raise RuntimeError(f"URL error {url}: {e}")


def put_bytes(url, data):
    req = urllib.request.Request(url, data=data, method="PUT", headers={
        "Content-Type": "application/octet-stream",
        "Content-Length": str(len(data)),
        "Origin": "https://yun.139.com",
        "Referer": "https://yun.139.com/",
    })
    with urllib.request.urlopen(req, timeout=60) as resp:
        if resp.status != 200:
            raise RuntimeError(f"PUT failed HTTP {resp.status}")
        resp.read()


class Yun139:
    def __init__(self, auth, account, root_folder_id):
        self.auth = auth
        self.account = account
        self.root_folder_id = root_folder_id or "/"
        self.personal_host = ""

    def route(self):
        body = {
            "userInfo": {
                "userType": 1,
                "accountType": 1,
                "accountName": self.account,
            },
            "modAddrType": 1,
        }
        resp, _ = http_json("https://user-njs.yun.139.com/user/route/qryRoutePolicy", body, self.auth)
        if not resp.get("success"):
            raise RuntimeError(f"route failed: {safe_resp(resp)}")
        for item in resp.get("data", {}).get("routePolicyList", []):
            if item.get("modName") == "personal" and item.get("httpsUrl"):
                self.personal_host = item["httpsUrl"].rstrip("/")
                return self.personal_host
        raise RuntimeError("route failed: no personal host")

    def personal_post(self, path, body):
        if not self.personal_host:
            self.route()
        resp, _ = http_json(self.personal_host + path, body, self.auth, personal=True)
        if not resp.get("success"):
            raise RuntimeError(f"personal {path} failed: {safe_resp(resp)}")
        return resp

    def list_folder(self, parent_id):
        items = []
        cursor = ""
        while True:
            resp = self.personal_post("/file/list", {
                "imageThumbnailStyleList": ["Small", "Large"],
                "orderBy": "updated_at",
                "orderDirection": "DESC",
                "pageInfo": {"pageCursor": cursor, "pageSize": 100},
                "parentFileId": parent_id,
            })
            data = resp.get("data", {})
            items.extend(data.get("items") or [])
            cursor = data.get("nextPageCursor") or ""
            if not cursor:
                return items

    def find_child(self, parent_id, name, item_type=None):
        for item in self.list_folder(parent_id):
            if item.get("name") == name and (item_type is None or item.get("type") == item_type):
                return item
        return None

    def ensure_folder(self, parent_id, name):
        found = self.find_child(parent_id, name, "folder")
        if found:
            return found["fileId"]
        resp = self.personal_post("/file/create", {
            "parentFileId": parent_id,
            "name": name,
            "description": "",
            "type": "folder",
            "fileRenameMode": "force_rename",
        })
        file_id = resp.get("data", {}).get("fileId")
        if not file_id:
            raise RuntimeError(f"create folder {name} returned no fileId: {safe_resp(resp)}")
        return file_id

    def ensure_path(self, root_id, names):
        cur = root_id
        for name in names:
            cur = self.ensure_folder(cur, name)
        return cur

    def upload_small(self, parent_id, name, content):
        sha256 = hashlib.sha256(content).hexdigest()
        size = len(content)
        resp = self.personal_post("/file/create", {
            "contentHash": sha256,
            "contentHashAlgorithm": "SHA256",
            "contentType": "application/octet-stream",
            "parallelUpload": False,
            "partInfos": [{
                "partNumber": 1,
                "partSize": size,
                "parallelHashCtx": {"partOffset": 0},
            }],
            "size": size,
            "parentFileId": parent_id,
            "name": name,
            "type": "file",
            "fileRenameMode": "auto_rename",
        })
        data = resp.get("data", {})
        part_infos = data.get("partInfos") or []
        if part_infos:
            put_bytes(part_infos[0]["uploadUrl"], content)
            self.personal_post("/file/complete", {
                "contentHash": sha256,
                "contentHashAlgorithm": "SHA256",
                "fileId": data["fileId"],
                "uploadId": data["uploadId"],
            })
        return data.get("fileId"), sha256, data

    def part_infos(self, size):
        gb = 1024**3
        mb = 1024**2
        part_size = 512 * mb if size // gb > 30 else 100 * mb
        parts = []
        offset = 0
        part_number = 1
        while offset < size or (size == 0 and part_number == 1):
            part_len = min(part_size, max(0, size - offset))
            parts.append({
                "partNumber": part_number,
                "partSize": part_len,
                "parallelHashCtx": {"partOffset": offset},
            })
            offset += part_len
            part_number += 1
            if size == 0:
                break
        return parts

    def create_by_sha256(self, parent_id, name, size, sha256, rename="auto_rename"):
        part_infos = self.part_infos(size)
        resp = self.personal_post("/file/create", {
            "contentHash": sha256,
            "contentHashAlgorithm": "SHA256",
            "contentType": "application/octet-stream",
            "parallelUpload": False,
            "partInfos": part_infos[:100],
            "size": size,
            "parentFileId": parent_id,
            "name": name,
            "type": "file",
            "fileRenameMode": rename,
        })
        return resp.get("data", {})

    def delete_permanently(self, file_id):
        if not file_id:
            return
        self.personal_post("/recyclebin/batchTrash", {"fileIds": [file_id]})
        self.personal_post("/file/batchDelete", {"fileIds": [file_id]})


def safe_resp(resp):
    clone = json.loads(json.dumps(resp, ensure_ascii=False))
    return dumps(clone)[:1200]


def load_storage(db_path=DEFAULT_DB_PATH, storage_id=0, mount_path=""):
    con = sqlite3.connect(db_path)
    try:
        if storage_id:
            row = con.execute(
                "select id,mount_path,addition from x_storages where driver='139Yun' and id=?",
                (storage_id,),
            ).fetchone()
        elif mount_path:
            row = con.execute(
                "select id,mount_path,addition from x_storages where driver='139Yun' and mount_path=?",
                (mount_path,),
            ).fetchone()
        else:
            row = con.execute(
                "select id,mount_path,addition from x_storages where driver='139Yun' and disabled=0 and status='work' order by id"
            ).fetchone()
    finally:
        con.close()
    if not row:
        raise RuntimeError("no matching 139Yun storage found")
    sid, storage_mount_path, addition_s = row
    addition = json.loads(addition_s)
    auth = addition.get("authorization") or ""
    if not auth:
        raise RuntimeError("139Yun storage has no authorization")
    decoded = base64.b64decode(auth).decode("utf-8", "replace")
    parts = decoded.split(":")
    if len(parts) < 3:
        raise RuntimeError("authorization format invalid")
    account = parts[1]
    return {
        "id": sid,
        "mount_path": storage_mount_path,
        "addition": addition,
        "auth": auth,
        "account": account,
        "root_folder_id": addition.get("root_folder_id") or "/",
    }


def create_share_transfer(yun, share_id, file_item, tmp_folder_id, tmp_folder_name, owner_account="", share_host=DEFAULT_SHARE_HOST):
    req = {
        "createOuterLinkBatchOprTaskReq": {
            "msisdn": yun.account,
            "ownerAccount": owner_account or "",
            "taskType": 1,
            "taskInfo": {
                "linkID": share_id,
                "contentInfoList": [file_item["path"]],
                "catalogInfoList": [],
                "newCatalogID": tmp_folder_id,
                "newCatalogName": tmp_folder_name,
                "needPassword": False,
            },
            "linkID": share_id,
            "needPassword": False,
        }
    }
    url = share_host.rstrip("/") + "/richlifeApp/devapp/IBatchOprTask/createOuterLinkBatchOprTask"
    resp, _ = http_json(url, req, yun.auth, personal=True)
    if not resp.get("success"):
        raise RuntimeError(f"share transfer create failed: {safe_resp(resp)}")
    data = resp.get("data") or {}
    task_id = data.get("taskID") or data.get("taskId")
    if isinstance(data.get("createBatchOprTaskRes"), dict):
        nested = data["createBatchOprTaskRes"]
        task_id = task_id or nested.get("taskID") or nested.get("taskId")
    if not task_id:
        raise RuntimeError(f"share transfer create returned no task id: {safe_resp(resp)}")
    return task_id, resp


def query_share_transfer(yun, task_id, share_host=DEFAULT_SHARE_HOST):
    req = {"queryBatchOprTaskDetailReq": {"taskID": task_id, "msisdn": yun.account}}
    url = share_host.rstrip("/") + "/richlifeApp/devapp/IBatchOprTask/queryBatchOprTaskDetail"
    resp, _ = http_json(url, req, yun.auth, personal=True)
    return resp


def wait_for_file(yun, folder_id, name, size, timeout=120):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        items = yun.list_folder(folder_id)
        matches = [
            item for item in items
            if item.get("type") == "file" and item.get("name") == name and int(item.get("size") or 0) == int(size)
        ]
        if matches:
            return matches[0]
        last = [(item.get("name"), item.get("type"), item.get("size")) for item in items[:10]]
        time.sleep(3)
    raise RuntimeError(f"timed out waiting for transferred file {name}; last items={last}")


def encode_cas(name, size, sha256):
    payload = {
        "name": name,
        "size": int(size),
        "md5": "",
        "sliceMd5": "",
        "sha256": sha256,
        "create_time": str(int(time.time())),
    }
    return base64.b64encode(dumps(payload).encode("utf-8"))


def load_owner_account(path):
    if not path:
        return ""
    try:
        with open(path, "r", encoding="utf-8") as f:
            data = json.load(f)
        generals = data.get("data", {}).get("getOutLinkGeneralResp", {}).get("outLinkGeneral") or []
        if generals:
            return generals[0].get("ownerUserId") or ""
    except Exception:
        return ""
    return ""
