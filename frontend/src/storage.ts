// 最近浏览：localStorage 匿名维护（device 级），既是“最近浏览条”的数据，
// 也会作为 recent_route_ids 传给推荐接口做会话级个性化。
const KEY = 'recent_routes_v1';
const MAX = 8;

export interface RecentRoute {
  id: string;
  title: string;
}

export function getRecent(): RecentRoute[] {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? '[]');
  } catch {
    return [];
  }
}

export function pushRecent(id: string, title: string): RecentRoute[] {
  const rest = getRecent().filter((r) => r.id !== id);
  const next = [{ id, title }, ...rest].slice(0, MAX);
  localStorage.setItem(KEY, JSON.stringify(next));
  return next;
}

// 推荐请求用的历史：去掉当前路线本身，最多 5 条，旧→新顺序（与后端约定一致）。
export function recentIdsForRequest(currentId: string): string[] {
  return getRecent()
    .map((r) => r.id)
    .filter((id) => id !== currentId)
    .slice(0, 5)
    .reverse();
}
