# 群星视觉与语义实验

以 UMAP 为主、PaCMAP 为对照、PCA 为基线，比较二维语义布局与两种星空渲染。Python 离线计算，原生 JavaScript 与 Canvas 展示；生产 Go 服务和认知地图使用各自的数据与入口。

## 启动

在仓库根目录执行：

```bash
python3 -m venv .local/stars-venv
.local/stars-venv/bin/python -m pip install -r experiments/stars/requirements.txt
.local/stars-venv/bin/python experiments/stars/lab.py serve --port 4317
```

浏览器打开 `http://127.0.0.1:4317`。服务按顺序查找 `.local/stars/visual-v3/bundle.json`、`.local/stars/visual-v2/bundle.json` 和 `.local/stars/demo-1000/bundle.json`。页面默认进入沉浸预览，已有布局随首页压缩传输，无需再次导入向量。

指定布局文件：

```bash
.local/stars-venv/bin/python experiments/stars/lab.py serve \
  --bundle .local/stars/visual-v3/bundle.json --port 4317
```

没有布局时，通过“布局与数据 → 导入向量”选择索引副本或笔记向量 JSON。首次计算包含 Numba 编译，后续计算可复用编译缓存。PaCMAP 的 Annoy 依赖在 Linux 上可能需要 C++ 编译器和 Python 开发头文件。

从其他 Tailscale 设备访问：

```bash
tailscale serve --bg --https=4318 http://127.0.0.1:4317
```

使用该设备的 Tailscale HTTPS 域名与 4318 端口打开页面。

## 视觉与交互

- **A 精致星图**：深蓝黑色底、清晰星核、细微的尺寸与亮度变化，主题文字与光晕保持克制。
- **B 天文摄影**：共用 A 的数据、坐标和星体属性，使用更清晰的星核，叠加从笔记空间密度产生的分层辉光。
- 每个星体对应一条有向量的笔记。辉光等权累积实际笔记的位置，不使用预设银河轮廓或装饰星体。
- 星体属性由完整语料的原向量主题中心性、近邻密度、独特性及资源 ID 散列共同决定。每个主题选一颗核心星，少量相对独特的笔记使用暖色细光晕；不同规模共用同一笔记的属性。
- 远景最多显示六个主题名，窄屏最多三个；中景渐入局部主题；近景渐入经过避让的笔记摘录。点选星体或摘录可阅读全文和原向量近邻。
- A / B 切换只改变渲染，相机和坐标保持不变。算法、规模和新增笔记策略在各视觉方案间共用。
- 默认沉浸预览隐藏调试面板。“布局与数据”打开主题、笔记、算法指标和聚类比较；全屏按钮进入浏览器全屏。

渲染按交互触发，使用 Canvas 2D。密度场采用浮点高斯累积，在超过孤立星体密度的区域生成局部柔光，再以低强度模糊得到底雾；稀疏区域保持暗。主题之间以低饱和蓝、灰紫、蓝青区分，少量亮星带暖白与淡金。绘制顺序为底雾、密度柔光、星点小光晕、清晰星核和文字。

星点光晕使用缓存精灵，密度场只在坐标切换时生成 512 × 512 离屏纹理；平移与缩放只变换已有纹理与星点。设备像素比上限为 2。

## UMAP 精调

沉浸预览顶部直接切换三个版本。各版本使用相同向量、主题、星体属性与渲染，坐标采用相似变换对齐，便于观察结构变化。

| 版本 | n_neighbors | min_dist | spread | 用途 |
| --- | --- | --- | --- | --- |
| 均衡 | 15 | 0.12 | 1.0 | 保留自然形状的对照 |
| 轻聚落 | 12 | 0.055 | 1.05 | 适度增强局部分区 |
| 连贯聚落 | 24 | 0.065 | 1.1 | 扩大近邻范围并收紧局部 |

“主体”取景使用坐标 1.5%–98.5% 分位数，放大幅度上限为全部取景的 1.12 倍，同时要求视野覆盖至少 97% 的笔记和每个主题至少 80% 的成员。条件不满足时显示全部范围。“全部”取景包含离群点。两种取景只调整相机，不压缩语义坐标或改变成员关系。

使用已有向量和布局生成精调版本：

```bash
.local/stars-venv/bin/python experiments/stars/tuning.py \
  --bundle .local/stars/visual-v2/bundle.json \
  --input .local/semantic-demo/notes-diverse.json \
  --vectors-f32 .local/semantic-demo/vectors-diverse.f32 \
  --dimensions 384 --out .local/stars/visual-v3
```

`build` 也会生成三个 UMAP 版本。PaCMAP、PCA 和 A / B 渲染切换放在“布局与数据”中。

顶部模型选择可对照 MiniLM 384、Qwen API 1024 与本地 Qwen3-Embedding-0.6B 1024（ONNX q8）。各模型使用相同笔记、样本、UMAP 参数与渲染，主题和星体属性固定；原向量近邻随模型切换。`compare_embeddings.py` 根据资源名对齐另一套向量并生成对照布局，拒绝缺失笔记或正文差异，并计算模型之间的近邻重合度。两个 Qwen 模型维数相同，可直接观察模型与推理配置变化带来的布局差异。

## 数据入口

索引按 SQLite 只读事务读取。运行中的 WAL 数据库应先通过 SQLite backup API 生成一致性副本；也可直接通过 `--index` 读取可访问的原库。

```bash
.local/stars-venv/bin/python experiments/stars/lab.py serve \
  --index /path/to/index.sqlite3 --port 4317
```

离线生成布局：

```bash
.local/stars-venv/bin/python experiments/stars/lab.py build \
  --index /path/to/index.sqlite3 --out .local/stars/personal-001
```

索引包含多个作者或模型空间时，通过 `--instance`、`--user`、`--space` 选择唯一代次。实验只读取与投影 revision 匹配的 active 代次，按绑定作者筛选笔记。分块按 `content_hash` 去重，各块 L2 归一化后等权求和，再次归一化，与 `internal/map_store.go` 的笔记向量聚合规则一致。无向量笔记计入来源统计，语义布局只放置有向量的笔记。

JSON 格式：

```json
{
  "source": {
    "label": "笔记向量导出",
    "kind": "personal",
    "model": "模型名称"
  },
  "notes": [
    {
      "name": "memos/resource-name",
      "content": "笔记正文",
      "tags": ["标签"],
      "vector": [0.1, 0.2, 0.3]
    }
  ]
}
```

`kind` 支持 `personal`、`demo`、`evaluation`，页面据此显示来源。所有向量需属于同一模型空间。详情展示导出时的正文。

也支持已有笔记 JSON 与按笔记顺序存放的 little-endian float32 文件：

```bash
.local/stars-venv/bin/python experiments/stars/lab.py build \
  --input .local/semantic-demo/notes-diverse.json \
  --vectors-f32 .local/semantic-demo/vectors-diverse.f32 \
  --dimensions 384 --kind demo --label '已有多主题演示语料' \
  --out .local/stars/demo-1000
```

该入口复用已有的 1,000 条演示笔记及向量，不生成笔记，不调用模型。10,000 条模板化演示语料不加入此数据集。

## 比较方式

- 默认使用 50、300 条和全量三个嵌套样本，按 `SHA-256(seed:name)` 排序，随机种子为 42。`--sizes` 可调整小规模样本。
- 各样本留出约 5%、最多 20 条笔记，对比基础布局、加入笔记后全部重算、固定旧点后通过原生 `transform` 放置新点。
- PCA 使用归一化原向量；UMAP 使用 cosine、15 个近邻、`min_dist=0.12`；PaCMAP 使用归一化向量的欧氏距离、10 个近邻、`MN_ratio=0.5`、`FP_ratio=2`。
- 聚类与二维展示分开计算：归一化原向量经独立的 UMAP 10 维投影，再用 HDBSCAN Leaf 识别主题，避免 EOM 把多个领域合成少数大群。主题候选在原向量空间接受质心余弦亲和度和近邻成员支持的核验，弱归属笔记保持独立。
- 聚类 UMAP 使用 cosine、30 个近邻和 `min_dist=0`。HDBSCAN 的 `min_cluster_size` 约为笔记数平方根的 0.6 倍，限制在 5–24；`min_samples` 限制在 3–8。质心余弦下限为 0.35，同组近邻比例下限为 0.25。
- 32 条以上的主题可以继续细分局部主题。主题名称来自组内标签分布，混合领域保留组合名称，缺少标签时使用代表笔记标题。标签不参与成员分配，代表笔记取最靠近原向量质心的三条。
- 页面同时列出 PCA 密度参数、UMAP EOM、UMAP Leaf 和原向量核验的结果，比较主题数、独立笔记数及原标签一致率。原标签只是辅助参照，各策略覆盖的笔记不同。
- 近邻保持率对比原向量余弦 top-10 与二维欧氏 top-10。Trustworthiness 使用同一原空间排名。至多抽取 512 个查询点，参考库包含该规模全部点；新增点指标覆盖全部留出笔记。
- 重算位移先通过相似变换消除整体平移、旋转、镜像和缩放，再用基础布局 P90 半径归一化。固定旧点的零位移由策略保证。
- 少于 12 条或完全相同的向量使用 PCA，页面显示原因。

页面支持算法切换、规模切换、主题聚焦、全文筛选、笔记阅读、原向量近邻、平移、滚轮与双指缩放，以及旧位置连线。默认固定旧点，由各算法的原生 `transform` 放置新增笔记。打开“新增标记”后，金色圆环表示新增笔记；位置变化连线用于观察布局重算。方向键平移，`+` / `-` 缩放，`Home` 显示全图。

输出包含 `bundle.json`、`comparison.md`、`stars.html` 和依赖版本，保存在 `.local/stars/`。`stars.html` 内含两种视觉、笔记与布局，可直接用浏览器打开并离线比较；在线页面的“离线页面”提供下载。输入副本、正文、布局及报告均独立存放，实验读取流程不写入索引或地图快照。服务只监听 `127.0.0.1`。

`engine.py` 负责向量读取、坐标、指标与导出；`semantics.py` 负责主题层级和可复现的星体属性；`tuning.py` 负责 UMAP 参数比较；`scene.js` 负责渲染；`app.js` 负责浏览器交互。已有布局可通过 `semantics.refine_bundle` 更新语义元数据，二维坐标数组保持原值；输出中保存坐标指纹用于比较。

## 实现依据

生产观星由 `internal/map.go` 调度，`services/layout/` 计算坐标与主题，`internal/map_stars_store.go` 发布快照。升级时，`internal/map_store.go` 可从 `map_layouts` 与 `map_points` 读取旧 PCA 快照。实验的 PCA 仅用于比较投影效果。

算法与增量投影依据：UMAP 官方参数、复现性和新数据投影文档；PaCMAP 官方实现中的 `fit_transform` / `transform`；scikit-learn 的 PCA、HDBSCAN 和 Trustworthiness 定义。
