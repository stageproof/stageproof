#!/usr/bin/env python3
"""Create or update the Stageproof note on the current GitLab merge request.

Reads STAGEPROOF_GITLAB_TOKEN, CI_API_V4_URL, CI_PROJECT_ID, and
CI_MERGE_REQUEST_IID from the environment. The note body is the markdown
file passed as the first argument, prefixed with a stable HTML marker so a
rerun replaces the previous note instead of stacking comments.
"""

import json
import os
import sys
import urllib.request

MARKER = "<!-- stageproof-mr-gate -->"


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: post_gitlab_mr_note.py COMMENT.md", file=sys.stderr)
        return 2
    token = os.environ.get("STAGEPROOF_GITLAB_TOKEN", "")
    project = os.environ.get("CI_PROJECT_ID", "")
    mr_iid = os.environ.get("CI_MERGE_REQUEST_IID", "")
    if not token or not project or not mr_iid:
        print("stageproof: missing GitLab CI token or merge request id", file=sys.stderr)
        return 2

    body = MARKER + "\n" + open(sys.argv[1], encoding="utf-8").read()
    api = os.environ.get("CI_API_V4_URL", "https://gitlab.com/api/v4").rstrip("/")
    headers = {"PRIVATE-TOKEN": token, "Content-Type": "application/json"}
    notes_url = f"{api}/projects/{project}/merge_requests/{mr_iid}/notes?per_page=100"
    existing_id = None
    try:
        req = urllib.request.Request(notes_url, headers=headers)
        notes = json.loads(urllib.request.urlopen(req, timeout=20).read())
        for note in notes:
            if MARKER in (note.get("body") or ""):
                existing_id = note["id"]
                break
    except Exception as exc:
        print("warn listing MR notes:", exc)

    payload = json.dumps({"body": body}).encode("utf-8")
    if existing_id:
        url = f"{api}/projects/{project}/merge_requests/{mr_iid}/notes/{existing_id}"
        method = "PUT"
        action = "Updated"
    else:
        url = f"{api}/projects/{project}/merge_requests/{mr_iid}/notes"
        method = "POST"
        action = "Posted"
    req = urllib.request.Request(url, data=payload, headers=headers, method=method)
    urllib.request.urlopen(req, timeout=20)
    print(f"{action} Stageproof comment on MR !{mr_iid}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
