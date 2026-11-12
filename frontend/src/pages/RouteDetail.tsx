import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router-dom';
import { getRecommendations, getRoute, RecResponse, Route, track } from '../api';
import RouteCard from '../components/RouteCard';
import TopNav from '../components/TopNav';
import { pushRecent, recentIdsForRequest } from '../storage';

// 详情页：常驻顶栏 + 面包屑（首页 › 城市 › 路线）+ 推荐模块。
// 标签点击开新标签页（按城市+标签的二级列表），不打断当前浏览动线。
export default function RouteDetail() {
  const { routeId = '' } = useParams();
  const [route, setRoute] = useState<Route | null>(null);
  const [rec, setRec] = useState<RecResponse | null>(null);
  const [recRoutes, setRecRoutes] = useState<Record<string, Route>>({});
  const [error, setError] = useState('');
  const navigate = useNavigate();

  useEffect(() => {
    window.scrollTo(0, 0);
    setRoute(null);
    setRec(null);
    setRecRoutes({});
    setError('');

    getRoute(routeId)
      .then((r) => {
        setRoute(r);
        pushRecent(r.route_id, r.title); // 写入“最近浏览”，之后的推荐都会带上它
      })
      .catch((e) => setError(String(e)));

    getRecommendations(routeId, recentIdsForRequest(routeId))
      .then((resp) => {
        setRec(resp);
        // 曝光埋点：一次批量上报，带 request_id/strategy/坑位，可与后续点击做归因
        track(
          resp.recommendations.map((item, i) => ({
            type: 'impression',
            route_id: item.route_id,
            request_id: resp.request_id,
            strategy: item.source,
            position: i + 1,
          })),
        );
        // 推荐接口只带 route_id/title，封面和标签需要逐条取详情（≤5 个并发请求，ES 命中毫秒级）
        return Promise.all(
          resp.recommendations.map((item) => getRoute(item.route_id).catch(() => null)),
        );
      })
      .then((list) => {
        const m: Record<string, Route> = {};
        for (const r of list) {
          if (r) m[r.route_id] = r;
        }
        setRecRoutes(m);
      })
      .catch((e) => setError(String(e)));
  }, [routeId]);

  const clickRec = (item: { route_id: string; source: string }, position: number) => {
    if (rec) {
      track([{ type: 'click', route_id: item.route_id, request_id: rec.request_id, strategy: item.source, position }]);
    }
    navigate(`/route/${item.route_id}`);
  };

  const cityName = route ? route.city_id.split(':')[1].replace(/-/g, ' ') : '';
  const tagHref = (t: string) =>
    `#/city/${encodeURIComponent(route?.city_id ?? '')}?tag=${encodeURIComponent(t)}`;

  if (error) {
    return (
      <div>
        <TopNav />
        <div className="page">
          <nav className="breadcrumb">
            <Link to="/">首页</Link>
          </nav>
          <div className="error">加载失败（这条路线可能不存在）：{error}</div>
        </div>
      </div>
    );
  }
  if (!route) {
    return (
      <div>
        <TopNav />
        <div className="page">加载中…</div>
      </div>
    );
  }

  return (
    <div>
      <TopNav />
      <div className="page">
        <nav className="breadcrumb">
          <Link to="/">首页</Link>
          <span>›</span>
          <Link to={`/city/${encodeURIComponent(route.city_id)}`}>{cityName}</Link>
          <span>›</span>
          <span>{route.title}</span>
        </nav>

        <section className="detail">
          {route.image_url ? (
            <img className="detail-img" src={route.image_url} alt={route.title} />
          ) : (
            <div className="detail-img detail-img-placeholder">
              <span>{route.title}</span>
            </div>
          )}
          <div className="detail-body">
            <h1>{route.title}</h1>
            <div className="detail-meta">
              {route.country_code} · {route.city_id}
            </div>
            <div className="card-tags">
              {route.tags.map((t) => (
                <a key={t} className="tag" href={tagHref(t)} target="_blank" rel="noreferrer">
                  {t}
                </a>
              ))}
            </div>
          </div>
        </section>

        <section className="rec-section">
          <h2>
            相似路线推荐
            {rec && (
              <span className={`strategy strategy-${rec.strategy}`}>
                {rec.strategy === 'two_tower' ? '双塔模型' : '规则引擎'}
              </span>
            )}
          </h2>
          <div className="grid">
            {rec?.recommendations.map((item, i) => (
              <RouteCard
                key={item.route_id}
                route={recRoutes[item.route_id] ?? { ...item, country_code: '', city_id: route.city_id, tags: [] }}
                onClick={() => clickRec(item, i + 1)}
              />
            ))}
          </div>
          {rec && rec.recommendations.length === 0 && <div className="empty">暂无相似的路线</div>}
        </section>
      </div>
    </div>
  );
}
