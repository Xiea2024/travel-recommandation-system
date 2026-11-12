// 后端 API 的类型与封装。BASE 为空字符串：dev 走 vite proxy，生产同源部署。
export interface Route {
  route_id: string;
  title: string;
  country_code: string;
  city_id: string;
  tags: string[];
  image_url?: string;
}

export interface Recommendation {
  route_id: string;
  title: string;
  source: 'two_tower' | 'rules';
}

export interface RecResponse {
  route_id: string;
  request_id: string;
  strategy: 'two_tower' | 'rules';
  recommendations: Recommendation[];
}

export interface TrackEvent {
  type: 'impression' | 'click';
  route_id: string;
  request_id: string;
  strategy: string;
  position: number;
}

async function http<T>(url: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(url, init);
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`${resp.status}: ${text}`);
  }
  return (await resp.json()) as T;
}

export function listRoutes(params: { city_id?: string; tag?: string; q?: string }): Promise<{ routes: Route[] }> {
  const usp = new URLSearchParams();
  if (params.city_id) usp.set('city_id', params.city_id);
  if (params.tag) usp.set('tag', params.tag);
  if (params.q) usp.set('q', params.q);
  const qs = usp.toString();
  return http<{ routes: Route[] }>(`/v1/routes${qs ? '?' + qs : ''}`);
}

export function getRoute(routeId: string): Promise<Route> {
  return http<Route>(`/v1/routes/${encodeURIComponent(routeId)}`);
}

export function getRecommendations(routeId: string, recent: string[]): Promise<RecResponse> {
  return http<RecResponse>('/v1/recommendations', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ route_id: routeId, recent_route_ids: recent }),
  });
}

// 埋点：fire-and-forget，任何失败都不打断用户操作（training data flywheel 的数据源）。
export function track(events: TrackEvent[]): void {
  if (events.length === 0) return;
  void fetch('/v1/track', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ events }),
  }).catch(() => undefined);
}
