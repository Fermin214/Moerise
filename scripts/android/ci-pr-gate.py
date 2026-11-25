#!/usr/bin/env python3
import os


def main():
    passed = os.environ["SCOPE_RESULT"] == os.environ["GO_RESULT"] == "success"
    for check in ("WEB", "ANDROID", "LINUX"):
        required = os.environ[f"{check}_REQUIRED"]
        result = os.environ[f"{check}_RESULT"]
        print(f"{check}: required={required} result={result}")
        passed &= (required, result) in {("true", "success"), ("false", "skipped")}
    return 0 if passed else 1


if __name__ == "__main__":
    raise SystemExit(main())
