#!/usr/bin/env python3
import sys

ANDROID_FILES = {
    b"main.go", b"go.mod", b"go.sum", b"config.example.json", b"LICENSE",
    b"scripts/lib/process.mjs",
    b"docs/android/THIRD_PARTY_NOTICES.md",
    b".github/workflows/checks.yml", b".github/workflows/android-checks.yml",
}
ANDROID_DIRS = (
    b"mobile/", b"scripts/android/", b"internal/", b"scripts/testweb/",
    b"docs/android/licenses/",
)


def classify(paths):
    for path in paths:
        if (path in ANDROID_FILES or path.startswith(ANDROID_DIRS)
                or (path.startswith(b"web/") and path.count(b"/") == 1 and path.endswith(b".go"))):
            return "build"
    return "none"


if __name__ == "__main__":
    print(classify(sys.stdin.buffer.read().split(b"\0")))
