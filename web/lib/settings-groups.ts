// Presentation metadata for the settings groups. The canonical
// group keys, labels and display order live in the Go registry
// (server/internal/settings/registry.go) and reach the page through
// the settings snapshot; this module only adds what the snapshot
// does not carry: a one-line description per group for the settings
// index, and a key -> label map used solely for sub-page tab titles
// (kept in sync with the registry by hand; the pages themselves
// always render the server-provided label).

export const SETTINGS_GROUP_DESCRIPTIONS: Record<string, string> = {
  service: "监听地址、数据库与管理令牌等启动配置，只读",
  update: "面板自动升级的开关与执行时间",
  auth: "GitHub 与 OIDC 单点登录的接入配置",
  appearance: "站点名称、主色与默认外观",
  crawler: "DHT 爬虫开关、监听端口与采样并发",
  metadata: "元数据抓取的并发、超时、早筛与优先级队列",
  scrape: "Tracker 刮削开关与 Tracker 列表",
  filter: "成人、垃圾与体积的静态过滤规则",
  moderation: "LLM 审核在专页统一编辑",
  trending: "豆瓣趋势榜的开关、刷新周期与保留条数",
  feeds: "RSS 订阅源列表与轮询抓取",
  ratelimit: "公开接口限流与搜索并发上限",
  publicapi: "公开 API 总开关与令牌要求",
};

export const SETTINGS_GROUP_LABELS: Record<string, string> = {
  service: "服务与数据库",
  update: "系统更新",
  auth: "登录与账号",
  appearance: "外观与主题",
  crawler: "爬虫",
  metadata: "元数据抓取",
  scrape: "刮削排序",
  filter: "内容过滤",
  moderation: "LLM 审核",
  trending: "趋势",
  feeds: "订阅源",
  ratelimit: "限流与搜索",
  publicapi: "公开 API",
};

export function settingsGroupHref(key: string): string {
  return `/admin/settings/${key}`;
}
