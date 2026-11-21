import io
import json
from pathlib import Path
import sqlite3
import tarfile
import tempfile
import uuid


def seed_upgrade(adb, package, output):
    adb.run("shell", "am", "force-stop", package)
    root = "/data/user/0/" + package + "/no_backup/data"
    archive = adb.run("exec-out", "tar", "-C", root, "-cf", "-", ".", binary=True)
    marker = uuid.uuid4().hex
    draft_text, queued_text = "升级草稿 " + marker, "升级待发 " + marker
    with tempfile.TemporaryDirectory(prefix="moerise-upgrade-") as directory:
        folder = Path(directory)
        with tarfile.open(fileobj=io.BytesIO(archive)) as source:
            for name in ("moerise.sqlite3", "moerise.sqlite3-wal"):
                entries = [item for item in source.getmembers() if item.name in (name, "./" + name)]
                if entries:
                    assert len(entries) == 1 and entries[0].isfile()
                    (folder / name).write_bytes(source.extractfile(entries[0]).read())
        database = folder / "moerise.sqlite3"
        db = sqlite3.connect(database)
        try:
            profile = db.execute("SELECT accountKey FROM profiles").fetchone()
            assert profile, "0.2.0 account missing"
            account = profile[0]
            home = db.execute("SELECT id FROM drafts WHERE accountKey=? AND slot='home'", (account,)).fetchone()
            assert home, "0.2.0 home draft missing"
            db.execute("UPDATE drafts SET content=?,revision=revision+1 WHERE id=?", (draft_text, home[0]))
            draft_id, operation, key = (str(uuid.uuid4()) for _ in range(3))
            frozen = {"id": draft_id, "accountKey": account, "content": queued_text, "revision": 1,
                      "slot": "queued:" + draft_id, "createdAt": 1}
            encoded = json.dumps(frozen)
            db.execute("""INSERT INTO drafts(id,accountKey,content,revision,attachmentsJson,slot,createdAt,queuedRevisionJson)
                VALUES(?,?,?,1,'[]',?,1,?)""", (draft_id, account, queued_text, frozen["slot"], encoded))
            db.execute("""INSERT INTO pending_writes(id,draftId,accountKey,idempotencyKey,method,path,body,draftRevision,status,submittedDraftJson)
                VALUES(?,?,?,?,'POST','/notes',?,1,'prepared',?)""",
                       (operation, draft_id, account, key, json.dumps({"content": queued_text}), encoded))
            db.commit()
            assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
            db.execute("PRAGMA wal_checkpoint(TRUNCATE)")
        finally:
            db.close()
        temporary = "/data/local/tmp/moerise-upgrade-" + marker + ".sqlite3"
        adb.run("push", str(database), temporary)
        identity = adb.run("shell", "stat", "-c", "%u:%g", root + "/moerise.sqlite3").strip()
        assert identity.replace(":", "").isdigit()
        adb.run("shell", "cp", temporary, root + "/moerise.sqlite3")
        adb.run("shell", "chown", identity, root + "/moerise.sqlite3")
        adb.run("shell", "chmod", "600", root + "/moerise.sqlite3")
        adb.run("shell", "rm", "-f", root + "/moerise.sqlite3-wal", root + "/moerise.sqlite3-shm", temporary)
        adb.run("shell", "restorecon", root + "/moerise.sqlite3")
    result = {"draft": draft_text, "queued": queued_text, "account": account, "write_key": key}
    output.mkdir(parents=True, exist_ok=True)
    (output / "seed.json").write_text(json.dumps(result) + "\n")
    return result


def verify_upgrade(adb, package, expected):
    adb.run("shell", "am", "force-stop", package)
    root = "/data/user/0/" + package + "/no_backup/data"
    archive = adb.run("exec-out", "tar", "-C", root, "-cf", "-", ".", binary=True)
    with tempfile.TemporaryDirectory(prefix="moerise-upgrade-verify-") as directory:
        database = Path(directory) / "moerise.sqlite3"
        with tarfile.open(fileobj=io.BytesIO(archive)) as source:
            for name in (database.name, database.name + "-wal"):
                entries = [item for item in source.getmembers() if item.name in (name, "./" + name)]
                if entries:
                    assert len(entries) == 1 and entries[0].isfile()
                    (database.parent / name).write_bytes(source.extractfile(entries[0]).read())
        db = sqlite3.connect(database)
        try:
            assert db.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
            assert db.execute("PRAGMA user_version").fetchone()[0] == 2
            assert db.execute("SELECT accountKey FROM profiles").fetchall() == [(expected["account"],)]
            assert db.execute("SELECT content FROM drafts WHERE accountKey=? AND slot='home'",
                              (expected["account"],)).fetchone() == (expected["draft"],)
            assert db.execute("SELECT count(*) FROM pending_writes WHERE accountKey=?",
                              (expected["account"],)).fetchone()[0] == 0
        finally:
            db.close()
