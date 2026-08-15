# BENZHI_README

## 项目说明

- 项目：zhanglei10281852-gif/gogo-28
- 项目用途：FireScope 是一个完全离线的卫星热异常处理工具。它读取本地 JSON/CSV 观测数据，在单次进程内完成数据校验、去重、跨源融合、火情事件跟踪、告警判定和确定性报告生成；快照功能可将轨道批次以带校验和的版本化 JSON 原子写入本地目录。运行时不访问网络，也不上传数据。
- Go 工具链：`golang:1.22`
- 前端工具链：无

## 标准构建、运行和测试命令

进入容器后执行：

```bash
# 编译
cd '/app' && GOTOOLCHAIN=local go build ./...

# 启动
cd '/app' && GOTOOLCHAIN=local go run ./cmd/firescope

# 测试
cd '/app' && GOTOOLCHAIN=local go test ./...
```

## Docker 构建和进入容器

```bash
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-task-28-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-task-28-arm64 linux/arm64
docker run -it benzhi-task-28-amd64:latest
docker run -it --platform linux/arm64 benzhi-task-28-arm64:latest
```

## 题目验证命令

1. 预期退出码 0：`go test ./alert -run "^TestClearIncident" -count=1 -v`
2. 预期退出码 0：`go test -buildvcs=false -count=1 ./...`

## Bug 复现

Bug 现象、触发步骤和完整错误信息见 `BUG_REPRO.md`。
