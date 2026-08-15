# FireScope

FireScope 是一个**完全离线**的卫星热异常处理工具。它读取本地 JSON/CSV 观测数据，在单次进程内完成数据校验、去重、跨源融合、火情事件跟踪、告警判定和确定性报告生成；快照功能可将轨道批次以带校验和的版本化 JSON 原子写入本地目录。运行时不访问网络，也不上传数据。

## 能力

- 严格读取 JSON（单条、数组或轨道批次）和带表头 CSV，并支持自动格式检测。
- 标准化卫星/仪器名称、UTC 时间、坐标和质量标志，记录可恢复的拒绝项。
- 对同源近时空观测去重，再对不同来源观测加权融合。
- 将融合证据关联为 candidate/active/contained/closed 事件。
- 按 FRP、置信度、状态和冷却时间生成本地渠道告警信封。
- 输出稳定排序的 text 或 JSON 报告；`--now` 可消除时钟带来的不确定性。
- 原子创建、校验、读取和列举版本化状态快照。
- 仅使用 Go 标准库；无服务端、数据库或网络依赖。

## 架构

`cmd/firescope` 只负责 CLI 和跨包编排，领域逻辑位于以下包：

1. `ingest`：严格解析、格式检测、标准化和拒绝统计。
2. `fusion`：可靠性评分、去重、时间衰减和多源融合。
3. `incident`：证据关联、事件聚合和生命周期。
4. `alert`：规则过滤、渠道扇出和抑制状态。
5. `report`：稳定 DTO、汇总以及 text/JSON 编码。
6. `store`：规范 JSON、SHA-256 校验和与同目录临时文件原子替换。
7. `internal/config`：严格 JSON 配置、默认值和跨字段校验。

`process` 的固定数据流为：

```text
input -> ingest -> dedup/fusion -> incident -> alert -> report
```

## 输入格式

所有时间使用 RFC3339；数值温度单位为 K，FRP 单位为 MW，置信度范围为 0–100。经度范围为 `[-180,180]`，纬度范围为 `[-90,90]`。`day_night` 只能是 `D` 或 `N`。质量标志可用值包括 `cloud`、`water`、`saturated`、`low_signal`、`geolocation_suspect`、`duplicate`。

### JSON 观测数组

```json
[
  {
    "id": "obs-001",
    "satellite": "NOAA-20",
    "instrument": "VIIRS",
    "acquired_at": "2026-07-07T12:00:00Z",
    "latitude": 34.05,
    "longitude": -118.25,
    "brightness": 335.2,
    "brightness_t31": 291.4,
    "frp": 18.7,
    "confidence": 88,
    "day_night": "D",
    "scan": 0.4,
    "track": 0.5,
    "quality_flags": []
  }
]
```

JSON 也可是一条观测，或一个轨道批次：

```json
{
  "orbit_id": "orbit-20260707-01",
  "satellite": "NOAA-20",
  "window_start": "2026-07-07T12:00:00Z",
  "window_end": "2026-07-07T12:10:00Z",
  "observations": [
    {
      "id": "obs-001",
      "satellite": "NOAA-20",
      "instrument": "VIIRS",
      "acquired_at": "2026-07-07T12:05:00Z",
      "latitude": 34.05,
      "longitude": -118.25,
      "brightness": 335.2,
      "brightness_t31": 291.4,
      "frp": 18.7,
      "confidence": 88,
      "day_night": "D",
      "scan": 0.4,
      "track": 0.5,
      "quality_flags": ["cloud"]
    }
  ]
}
```

### CSV

CSV 必须包含以下表头；`id` 可选，其余列必需。`quality_flags` 用 `|` 分隔，也可为空。

```csv
id,satellite,instrument,acquired_at,latitude,longitude,brightness,brightness_t31,frp,confidence,day_night,scan,track,quality_flags
obs-001,NOAA-20,VIIRS,2026-07-07T12:00:00Z,34.05,-118.25,335.2,291.4,18.7,88,D,0.4,0.5,cloud|low_signal
```

输入为 `-` 时从 stdin 读取；否则从指定文件读取。自动检测以第一个非空白字符判断 JSON 或 CSV。JSON 使用严格 schema，未知字段会成为拒绝项；CSV 未知、缺失或重复表头会直接报错。

## 命令

```sh
# stdin，自动识别，固定处理时间，输出文本
firescope process --input - --format auto --output text \
  --now 2026-07-07T12:30:00Z < observations.json

# 文件输入并输出完整 JSON 流水线结果
firescope process --input observations.csv --format csv --output json \
  --config firescope.json --now 2026-07-07T12:30:00Z

# 从轨道批次创建原子快照
firescope snapshot --input orbit.json --name orbit-20260707-01 \
  --store ./snapshots --tag source=archive \
  --now 2026-07-07T12:30:00Z --output json

# 平面 JSON/CSV 需要明确轨道 ID
firescope snapshot --input observations.csv --format csv \
  --orbit-id orbit-20260707-01 --name daily --store ./snapshots

# 校验并读取、列举
firescope inspect --store ./snapshots --name daily --output json
firescope list --store ./snapshots --output text

firescope version
firescope help
firescope process --help
```

命令成功时 stdout 只包含业务输出；诊断写入 stderr。flag/用法错误返回退出码 2，读取、校验、处理或写入失败返回退出码 1。`process` 未给 `--now` 时采用最新融合观测时间；`snapshot` 未给时采用最新批次时间，因此相同输入产生稳定输出。生产和测试自动化建议始终显式传入 `--now`。

## 配置

`--config` 仅接受严格 JSON：未知字段、错误类型、尾随 JSON 或跨字段不一致都会失败。省略时使用 `internal/config.Defaults()`，CLI 将其映射到领域配置：去重距离/窗口、融合时效与源权重、事件关联距离/激活证据/静默期/最大时长，以及告警阈值/渠道/冷却时间。源权重名称优先匹配仪器名，其次匹配卫星名。

配置示例：

```json
{
  "dedup": {
    "enabled": true,
    "distance_meters": 500,
    "time_window": "10m",
    "sources": ["VIIRS", "MODIS"]
  },
  "fusion": {
    "enabled": true,
    "source_weights": { "VIIRS": 0.6, "MODIS": 0.4 },
    "max_age": "30m",
    "minimum_sources": 2
  },
  "incident": {
    "merge_distance_meters": 3000,
    "quiet_period": "6h",
    "minimum_observations": 2,
    "maximum_duration": "168h"
  },
  "alert": {
    "enabled": true,
    "minimum_frp": 5,
    "minimum_confidence": 60,
    "cooldown": "1h",
    "channels": ["default"]
  }
}
```

## 输出和快照

文本输出先给出 ingestion/fusion/alert 计数，再给出人类可读报告。JSON 输出包含 `ingest`、`rejections`、`fusion`、`incidents`、`alerts` 和 `report`，适合下游程序处理。集合均按时间和稳定 ID 排序。

每个快照保存为 `<store>/<name>.json`，包含 schema/version、创建时间、标签、轨道批次、观测计数和内容校验和。写入先在同目录创建受限权限临时文件，写入并同步后再原子重命名；`inspect` 会验证严格 schema、版本、规范内容和校验和。名称只允许字母、数字、点、下划线和连字符，不能包含路径分隔符。不要手工修改快照；任何内容变更都会导致校验失败。

## 安全与限制

- 工具不联网，但输入文件、配置和快照仍应视为不可信数据；请使用最小文件权限并限制快照目录访问。
- 原子重命名保护单个快照文件，不等同于跨文件事务、远程备份或灾难恢复。
- 当前处理为内存内批处理，超大数据集应使用 `--max-records` 或预先分片。
- 这是分析和演示工具，不替代官方火情确认、应急调度、人员疏散决策或专业遥感校准。
- 告警“渠道”是离线输出中的逻辑标签；工具不会发送短信、邮件或网络通知。
- 卫星热异常可能来自云、工业热源、反射、地理定位误差等；结果可能误报或漏报。

## 新闻灵感与独立性声明

项目的主题灵感之一来自这则新闻：[California’s wildfire defense blasts off: Governor Newsom launches FireSat wildfire detection satellites to spot blazes from space](https://www.gov.ca.gov/2026/07/07/californias-wildfire-defense-blasts-off-governor-newsom-launches-firesat-wildfire-detection-satellites-to-spot-blazes-from-space/)。

**FireScope 是独立的软件项目，与该新闻、加利福尼亚州政府，以及新闻中提及的任何机构、公司、任务或项目均无隶属、合作、授权或背书关系；项目名称和功能描述也不表示上述各方对本项目的认可。**

## 验证

```sh
gofmt -w cmd/firescope/*.go integration_test.go
go test ./...
go vet ./...
```
