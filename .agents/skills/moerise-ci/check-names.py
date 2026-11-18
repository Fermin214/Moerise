import json
import sys

import yaml


def main():
    required = json.loads(sys.argv[1])
    jobs = yaml.safe_load(sys.stdin)["jobs"]
    stable_names = [
        job.get("name", job_id)
        for job_id, job in jobs.items()
        if "matrix" not in job.get("strategy", {}) and "uses" not in job
    ]
    if not required:
        print("分支 ruleset 没有绑定 GitHub Actions 的必需检查")
    valid = True
    for name in required:
        count = stable_names.count(name)
        print(f"{name}\t匹配到 {count} 个固定名称任务")
        valid = valid and count == 1
    return 0 if valid else 1


if __name__ == "__main__":
    sys.exit(main())
