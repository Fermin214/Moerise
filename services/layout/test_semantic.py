import unittest

from semantic import theme_label


class SemanticLabelTests(unittest.TestCase):
    def test_suppresses_ubiquitous_generic_tag_and_prefers_distinctive_terms(self):
        docs = []
        for i in range(10):
            docs.append({"name": f"memos/go{i}", "tags": ["想法", "后端"], "keywords": ["想法", "golang", "concurrency"]})
        for i in range(10):
            docs.append({"name": f"memos/tr{i}", "tags": ["想法"], "keywords": ["想法", "京都", "红叶"]})
        for i in range(10):
            docs.append({"name": f"memos/misc{i}", "tags": ["想法"], "keywords": ["想法", "日常"]})

        go_members = list(range(0, 10))
        tr_members = list(range(10, 20))
        classes = [go_members, tr_members]

        go_label = theme_label(docs, go_members, "未命名主题", class_members=classes)
        tr_label = theme_label(docs, tr_members, "未命名主题", class_members=classes)

        self.assertNotIn("想法", go_label)
        self.assertIn("后端", go_label)
        self.assertNotIn("想法", tr_label)
        self.assertTrue("京都" in tr_label or "红叶" in tr_label)

    def test_district_suppresses_parent_topic_shared_tag(self):
        docs = []
        for i in range(10):
            docs.append({"name": f"memos/sql{i}", "tags": ["Go"], "keywords": ["sqlite", "wal"]})
        for i in range(10):
            docs.append({"name": f"memos/net{i}", "tags": ["Go"], "keywords": ["http", "sse"]})
        for i in range(10):
            docs.append({"name": f"memos/other{i}", "tags": ["旅行"], "keywords": ["摄影"]})

        parent = list(range(0, 20))
        d1 = list(range(0, 10))
        d2 = list(range(10, 20))

        l1 = theme_label(docs, d1, "Go", scope=parent, class_members=[d1, d2])
        l2 = theme_label(docs, d2, "Go", scope=parent, class_members=[d1, d2])

        self.assertNotIn("Go", l1)
        self.assertTrue("sqlite" in l1 or "wal" in l1)
        self.assertNotIn("Go", l2)
        self.assertTrue("http" in l2 or "sse" in l2)


if __name__ == "__main__":
    unittest.main()
