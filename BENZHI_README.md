# BENZHI_README

这是一个基于 Go 实现的后端服务，用于承载 urban-sports-safety-hub 的业务处理、数据管理与稳定运行。

## 项目说明

- 项目：11DingKing/urban-sports-safety-hub
- 项目用途：Urban Sports Safety Hub is a production-oriented Go backend for youth climbing, skateboarding, and flying-disc instruction. It coordinates guardian consent, learner prerequisites, coach qualifications, course capacity, training groups, assessments, certifications, and the safety lifecycle of fitted equipment.
- Go 工具链：`golang:1.26`
- 前端工具链：无

## 标准构建、运行和测试命令

进入容器后执行：

```bash
# 编译
cd '/app' && GOTOOLCHAIN=local go build ./...

# 启动
cd '/app' && GOTOOLCHAIN=local go run ./cmd/server

# 测试
cd '/app' && GOTOOLCHAIN=local go test ./...
```

## Docker 构建和进入容器

```bash
chmod +x build_benzhi_docker.sh
./build_benzhi_docker.sh benzhi-task-26-amd64 linux/amd64
./build_benzhi_docker.sh benzhi-task-26-arm64 linux/arm64
docker run -it benzhi-task-26-amd64:latest
docker run -it --platform linux/arm64 benzhi-task-26-arm64:latest
```

## 题目验证命令

1. 预期退出码 0：`go test ./internal/equipment -run '^TestCheckoutAuditFailureDoesNotStrandEquipmentWithoutLoan$' -count=1`
2. 预期退出码 0：`go test ./...`
3. 预期退出码 0：`GOTOOLCHAIN=local go build -buildvcs=false ./... && GOTOOLCHAIN=local go vet ./...`
