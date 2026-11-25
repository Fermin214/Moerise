#!/usr/bin/env python3
import sys

HOST_ONLY = {
    b"scripts/android/phone-session.py",
    b"scripts/android/phone-session.test.py",
    b"scripts/android/verify-phone.ps1",
    b"scripts/android/phone-checks.json",
    b"scripts/android/ci-scope.test.py",
    b"scripts/android/device-recovery.test.py",
}
ANDROID_FILES = {
    b"main.go", b"go.mod", b"go.sum", b"LICENSE",
    b"docs/android/THIRD_PARTY_NOTICES.md",
}
ANDROID_DIRS = (
    b"mobile/", b"scripts/android/", b"internal/", b"scripts/testweb/",
    b"cmd/moerise-telemetry/", b"scripts/release/", b"docs/android/licenses/",
    b".github/workflows/",
)


def classify(paths):
    host = False
    for path in paths:
        if path in HOST_ONLY:
            host = True
        elif (path in ANDROID_FILES or path.startswith(ANDROID_DIRS)
              or (path.startswith(b"web/") and path.count(b"/") == 1 and path.endswith(b".go"))):
            return "full"
    return "host" if host else "none"


if __name__ == "__main__":
    print(classify(sys.stdin.buffer.read().split(b"\0")))
