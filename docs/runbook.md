---
status: active
last-verified: 2026-11-07
verified-by: SVC-AN-G4 ⑥（起停 / 日志 / 迁移重放 / 集成腿 / 上游排查 / 报告保留期；起停与探针命令在本机 PowerShell 5 实跑）
---

# Runbook — tellurion-analytics

> 本文只写**本仓**怎么起停、怎么看、怎么排查。接口语义读 `api-schema.md`，表结构读 `db-schema.md`，
> 数据流向读 `dataflow.md`。本机没有 PG / Docker 引擎，凡标「未执行」的步骤都是**没有真跑**，不要当成已验证。

## 1. 起停

### 1.1 本地冒烟（无库、内存 Store）

```powershell
go build -o "$env:TEMP\analyticsservice.exe" .\cmd\analyticsservice
$p = Start-Process -FilePath "$env:TEMP\analyticsservice.exe" `
  -ArgumentList '-addr','127.0.0.1:18087' `
  -RedirectStandardError "$env:TEMP\analytics.err.log" -PassThru
```

实跑启动日志（stderr，`log.Printf` 默认写 stderr）：

```
2026/11/07 05:30:26 analyticsservice: WARNING authentication disabled (open access); bind address "127.0.0.1:18087" is loopback-only
2026/11/07 05:30:26 analyticsservice: WARNING in-memory store (set -dsn / ANALYTICS_DATABASE_DSN to persist analysis.*)
2026/11/07 05:30:26 analyticsservice: readers wired result=false benchmark=false holdings=false factors=false queue=false
2026/11/07 05:30:26 analyticsservice: listening on 127.0.0.1:18087 (auth=false)
```

探针（实跑）：

```
> curl.exe -s -i http://127.0.0.1:18087/healthz
HTTP/1.1 200 OK
Content-Type: text/plain; charset=utf-8
Content-Length: 3

ok

> curl.exe -s -i http://127.0.0.1:18087/readyz
HTTP/1.1 503 Service Unavailable
Content-Type: application/json; charset=utf-8
Content-Length: 106

{"status":"not_ready","store":"memory","missing":["store: postgres (set -dsn / ANALYTICS_DATABASE_DSN)"]}
```

- `/healthz` = 进程活着（无鉴权、无限流）；`/readyz` = PG + 迁移 + 上游装配，缺一项就 503 并把缺的东西写进 `missing`。
- 没配 `-jwt-secret` 且绑定回环地址时是**开放访问**（故意的本地模式）；绑非回环必须给 secret，否则进程拒绝启动。

### 1.2 起完整实例（PG + 四个上游）

```powershell
$env:ANALYTICS_DATABASE_DSN = 'postgres://user:pass@127.0.0.1:5432/tellurion?sslmode=disable'
$env:ANALYTICS_JWT_SECRET = '<32+ bytes>'
$p = Start-Process -FilePath "$env:TEMP\analyticsservice.exe" -ArgumentList `
  '-addr','127.0.0.1:8087', `
  '-core-url','http://127.0.0.1:8081', `
  '-data-url','http://127.0.0.1:8082', `
  '-artifact-url','http://127.0.0.1:8083', `
  '-orchestration-url','http://127.0.0.1:8084' `
  -RedirectStandardError "$env:TEMP\analytics.err.log" -PassThru
```

启动时自动跑 `Migrate`（单事务、幂等；没有版本表、没有 down）。**本条本机未执行**（无 PostgreSQL）。

### 1.3 停

```powershell
Stop-Process -Id $p.Id
Get-NetTCPConnection -LocalPort 18087 -State Listen -ErrorAction SilentlyContinue
```

实跑：`Stop-Process` 后端口不再监听（第二条无输出）。

## 2. 看日志

```powershell
Get-Content "$env:TEMP\analytics.err.log" -Tail 100
Get-Content "$env:TEMP\analytics.err.log" -Tail 50 -Wait
```

先看这四类行：

| 行 | 含义 | 不对时怎么办 |
|---|---|---|
| `authentication disabled (open access); bind address ... is loopback-only` | 本地开放模式 | 非回环部署必须配 `-jwt-secret` |
| `WARNING in-memory store` | 没接 PG，重启即丢报告 | 配 `-dsn` / `ANALYTICS_DATABASE_DSN` |
| `readers wired result=… benchmark=… holdings=… factors=… queue=…` | 五个上游适配器谁接上了 | `false` 的端口对应端点会 fail-closed 成 503 |
| `listening on <addr> (auth=…)` | 进程已监听 | 端口冲突 / 绑定失败看上一行错误 |

## 3. 重放迁移

迁移 DDL 在 `internal/analysis` 的 Go 数组里，启动即跑；**没有独立 CLI**。

```powershell
# 需要真库；本机未执行（无 PostgreSQL）
$env:ANALYTICS_TEST_DSN = 'postgres://user:pass@127.0.0.1:5432/tellurion_test?sslmode=disable'
go test ./internal/analysis -run TestG2MigrationReplayIsStable -count=1 -v
```

无 DSN 时实跑输出（本机记录）：

```
=== RUN   TestG2MigrationReplayIsStable
    g2_replay_test.go:27: ANALYTICS_TEST_DSN is not set; skipping PostgreSQL store test
--- SKIP: TestG2MigrationReplayIsStable (0.00s)
PASS
ok  	tellurion-analytics/internal/analysis	0.058s
```

含义：同一套 DDL 连跑两遍不报错、`information_schema` 结果稳定（`g2_replay_test.go`）。**没有真库就等于没验证**。

## 4. `ANALYTICS_TEST_DSN` 集成腿怎么跑

```powershell
# 全量：真库用例会被拉起，其余照跑
$env:ANALYTICS_TEST_DSN = 'postgres://user:pass@127.0.0.1:5432/tellurion_test?sslmode=disable'
go test ./... -count=1
go test ./... -count=1 -v   # 想确认哪些被 SKIP 时用

# 无库：清掉变量，真库用例自动 SKIP（不是失败）
Remove-Item Env:ANALYTICS_TEST_DSN -ErrorAction SilentlyContinue
go test ./... -count=1
```

覆盖的真库腿：`g2_replay_test.go`（迁移重放 / 结构）、`pgstore_test.go`（写读闭环）、
`e2e_test.go` 中的 PG 分支。**本机无 PostgreSQL，带 DSN 的整条腿未执行**；不带 DSN 的跑法已实跑，退出码 0。

## 5. 上游不可达排查顺序

按这个顺序从近到远，不要一上来改代码：

1. **本进程活着吗**：`curl.exe -s -i http://127.0.0.1:8087/healthz` → 期望 `200 ok`。
2. **缺什么**：`curl.exe -s -i http://127.0.0.1:8087/readyz` → 503 时读 `missing`（`store` / `migrate` / `upstream:*`）。
3. **启动日志**：`readers wired result=… benchmark=… holdings=… factors=… queue=…`，`false` 的就是没接。
4. **从本机直连上游**（绕过本仓，确认是上游/网络而不是本仓）：

   ```powershell
   curl.exe -s -i --max-time 3 http://127.0.0.1:8081/api/backtest/run-1/report
   Test-NetConnection 127.0.0.1 -Port 8081
   ```

   实跑不可达端口（`127.0.0.1:1`）的输出：
   `curl: (7) Failed to connect ...`（`curl_exit=7`）；`Test-NetConnection 127.0.0.1 -Port 1 -InformationLevel Quiet`
   ⇒ `警告: TCP connect to (127.0.0.1 : 1) failed` + `False`（连接被拒 / 不可达）。

5. **读本仓 503 的 `error` 串**，对号入座（口径表见 `api-schema.md` §2.2）：

   | error 里出现 | 判断 | 处置 |
   |---|---|---|
   | `connection refused` / `EOF` / `no such host` | 上游没起 / 地址错 / 网络不通 | 修部署或网络后重试 |
   | `context deadline exceeded` / `timeout` | 上游挂住，10s 出站上界已生效 | 查上游健康；**不是**该 run 没数据 |
   | `upstream 401` / `403` | 上游凭证错 | 改 `-core-token` / `-data-token` / `-artifact-token`；本仓对外仍是 503 |
   | `upstream 409` | 上游结论未就绪 | 退避重试，别当终态 |
   | `upstream 404`（结果面） | run 不存在 | 终态，换 run / 修 run_id |
   | `upstream 404`（artifact 面） | holdings / factor 输入缺失 | 返回 409 `run_result_incomplete` + `missing`，不要伪造输入 |

6. **确认不是本仓**：上游直连（第 4 步）成功、本仓仍 503 ⇒ 看 `readers wired` 与 `-*-url` 参数；
   上游直连也失败 ⇒ 问题在上游/网络，本仓按 §2.2 正确 fail-closed，不要去改折算。

## 6. 报告体生命周期与保留期（与 SVC-AN-G2 ④ 同数）

数值来自 `docs/db-schema.md` §1.1：**保留期 = 90 天**，与 broker 已裁的 90 天保留期同族；理由是本仓报告
**可由输入重算**（`input_hash` / `params_hash` / `seed` 留痕），不承担审计留痕义务。

| 阶段 | 行为 | 证据 / 约束 |
|---|---|---|
| 提交 | 派生确定性 `report_id`，落 `reports`（`pending`） | `(run_id, analysis_type, params_hash, input_hash, seed, metrics_version)` |
| 运行 | `pending → running`；上游失败按 §2.2 折算，不落半成品 | 断开上游 ⇒ 503 且 `PutReport = 0`（见 §7） |
| 完成 | `completed`；同键同内容复用、异内容 `ErrReportConflict` | `completed` 起不可变（trigger） |
| 重放 | 同输入两次返回同一 `report_id` 与逐字节一致响应体 | sha256 相同；`TestG4DrillUpstreamOutageAndDeterministicReplay` |
| 读 / 导出 | `completed` 才可读；`pending/running` ⇒ 409 `report_not_ready` | 报告体 = `reports.body` |
| 保留 | **90 天**（`reports.created_at` 起算；`benchmarks` 按 `registered_at`，其余四表跟随 `reports`） | `db-schema.md` §1.1 |
| 清理 | **本仓没有自动清理作业**（不声称有）；到期数据由运维按 90 天口径处置 | 若要自动化，另立 ODR，并同批改 `db-schema.md` §1.1 |

调整保留期必须**同批**改 `docs/db-schema.md` §1.1 与本表；只改代码/配置不改表 = 文档失真。

## 7. 演练记录（SVC-AN-G4 ⑥，2026-11-07）

用例：`internal/httpapi.TestG4DrillUpstreamOutageAndDeterministicReplay`（`httptest` 真跑）。
阶段 1 上游 handler 直接 hijack 并关闭 TCP 连接；阶段 2 恢复为正常回测结果体。

实跑输出：

```
=== RUN   TestG4DrillUpstreamOutageAndDeterministicReplay
    g4_drill_test.go:124: PHASE 1 upstream down: HTTP 503 code=dependency_unavailable body={"error":"dependency unavailable: get run result run-1: Get \"http://127.0.0.1:55540/api/backtest/run-1/report\": EOF","code":"dependency_unavailable"} PutReport=0
    g4_drill_test.go:155: PHASE 2 recovered: report_id=76d8f1cc2cb77d4ef385921009eabf38c593293be8c78f53c751ba054cff9d9a
    g4_drill_test.go:156: PHASE 2 POST #1: len=569 sha256=e020eea2268563b09aa5769642f564017aeaf339872d65868a37144101620112
    g4_drill_test.go:157: PHASE 2 POST #2: len=569 sha256=e020eea2268563b09aa5769642f564017aeaf339872d65868a37144101620112
    g4_drill_test.go:158: PHASE 2 identical bytes=true PutReport=1
--- PASS: TestG4DrillUpstreamOutageAndDeterministicReplay (0.00s)
ok  	tellurion-analytics/internal/httpapi	0.073s
```

结论：断开上游 ⇒ **503 + 零落库**（无半成品）；恢复后同输入两次 **569 字节 / sha256 完全一致**，只落一行。

## 8. 门禁（本机实跑，2026-11-07）

```powershell
gofmt -l .
go build ./...
go vet ./...
go test ./... -count=1
```

四条退出码见 `docs/TASKS.md` SVC-AN-G4 ⑦；`gofmt -l .` 无输出。**真库 / 容器腿未执行**（本机无 PG、Docker 引擎不可用）。
