# cmd/test-refund

一次性集成测试入口:直接连本地 `ai_drama` PostgreSQL,跑 `billing.RefundOrder` /
`billing.SyncOrderStatus` 走真实事务路径,SQL 验证状态机 + 余额回退 + 并发幂等。

**不是 cmd/api 的运行依赖**——只是开发期手动跑的回归脚本。生产部署只 build `./cmd/api`,
这个目录不会被打进生产镜像。

## 什么时候跑

- 改了 `internal/billing/billing.go` 的退款/查单逻辑,跑一次确认没破口径
- 改了 `internal/model/model.go` 的 Order 字段/状态常量
- 改了 `internal/payment/` 的 Provider 接口或四个实现
- 真接入支付宝沙箱后,把 testOrderNo 换成沙箱订单号,跑一次端到端

## 前置条件

1. **本地 PostgreSQL 跑着**(`pg_isready` 能通,`ai_drama` 库存在)
2. **`.env` 在项目根目录**,至少有 `DATABASE_DSN`
3. **DB 里有 `MOCK-PAID-0001` 这条 paid 订单**(990 分 wechat,seed 灌进去的);
   订单关联 drama 必须有 `creator_id` 不为 NULL(场景 1-12 用到)
4. **`PAYMENT_DEV_MODE=true` 且 alipay/wechat 密钥空**——这样 Registry 走 DevProvider,
   `Refund` / `QueryOrder` 不联网。如果密钥配齐了,会去调真沙箱/真生产,**测试会触发真退款**!

## 运行

```bash
cd DramaBackend
source .env
go run ./cmd/test-refund
```

退出码:0=全过,1=至少一个断言失败。

## 覆盖的 14 个场景 / 54 个断言

| # | 场景 | 关键验证 |
|---|---|---|
| 1 | 部分退款 500 分 | status→partial_refunded,refund 元数据 + creator 余额按比例 -250 |
| 2 | 同 refund_no 重入 | 幂等返回原 order,creator 余额**不再扣**第二次 |
| 3 | 超额退 999 分 | `ErrRefundAmountInvalid`,creator 余额未变 |
| 4 | 再退 490(累计 990 全退) | status→refunded,累计回扣 495=clawback(500)+clawback(490) |
| 5 | refunded 状态再退 | `ErrRefundNotAllowed` |
| 6 | 参数校验 | 空 refund_no/零金额/订单不存在分别返对应错误 |
| 7 | SyncOrderStatus paid no-op | 状态仍 paid |
| 8 | SyncOrderStatus pending→paid | 触发 `MarkOrderPaid` 完整链路,creator 余额 +495 分账 |
| 9 | 5 个 goroutine 并发各退 100 | 行锁串行化,refund_amount=500,余额-250 不重不漏 |
| 10 | 5 个并发各退 250(超额混合) | 行锁让 3 笔抢到 750 配额,后 2 笔被超额拒 |
| 11 | CreatorStatsDaily 当日聚合回退 | stats_daily.income_cents 按比例 -clawback |
| 12 | GREATEST 防负兜底 | 历史数据不一致(stats=50,要扣 100)→ 钳到 0 不报错 |
| 13 | drama.creator_id=NULL 订单 | 无 creator 分支早退,creator 表不被动 |
| 14 | 单笔等额全退(990→990) | status=refunded(不是 partial),余额-495 |

## 数据安全(测后自动清理)

测前快照:
- 订单 `MOCK-PAID-0001` 的 `paid_at` / `platform_trade_no` / `expired_at`
- creator 的 `balance_cents` / `total_income_cents`
- 当日 `creator_stats_daily` 的 `income_cents`

测后 `defer` 里:
- 订单回到 `status=paid`,退款字段清零
- creator 余额回到 snapshot
- creator_stats_daily 回到 snapshot(包括 INSERT ON CONFLICT 兜底)
- 临时改过 `drama.creator_id=NULL`(场景 13)的也会复位

跑完打印对比:`期望 600000 / 1200000 / 495`,3 个数字必须逐位匹配。

## 真接入支付宝沙箱后怎么改

当前用 `DevProvider`(`PAYMENT_DEV_MODE=true` + 密钥空),`Refund`/`QueryOrder` 返回 stub。
切到真沙箱:

1. `.env` 填齐 `ALIPAY_APP_ID/PRIVATE_KEY/PUBLIC_KEY/NOTIFY_URL`,`ALIPAY_SANDBOX=true`,
   `PAYMENT_DEV_MODE=false`
2. 把 `testOrderNo` 换成沙箱真实订单号(沙箱买家账号下过的单)
3. **场景 9/10 并发场景要注意**:沙箱有限流,5 个 goroutine 同时请求可能触发限流。
   把 `concurrentN` 调到 2-3 比较稳。
4. 跑完每一笔都是真退款,沙箱钱包会减,**别用生产密钥跑**

## 已知不覆盖的场景

- **真渠道侧错误**(网络异常、API 错误码、签名失败)— Provider 层 stub 不会触发
- **微信退款异步回调**(`PROCESSING`→`SUCCESS` 通知)— 路由还没接(见 [[payment-mock-decision]])
- **HTTP 层 handler 响应格式** — 需要启 server 实测,见 server log 验证
- **跨日聚合**(退款发生日 != 支付日)— 当前实现按 `refundedAt.Format("2006-01-02")` 找当日聚合,
  跨日场景目前未测

## 为什么不用 `go test`

`billing` 包没有测试基础设施(没有 mock DB / sqlite 适配层),改造成本高;
而且本项目用了 PostgreSQL 特有的 `pg_advisory_xact_lock`,sqlite 无法承载。
独立 `cmd/test-refund` 主程序最简单,直接复用 `database.Connect` 走真实 DB。

如果将来引入 testcontainer 跑 PostgreSQL 集成测试,这些场景可以平移过去。
