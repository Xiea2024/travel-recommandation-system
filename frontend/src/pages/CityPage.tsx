import { useEffect, useState } from 'react';
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom';
import { listRoutes, Route } from '../api';
import RouteCard from '../components/RouteCard';
import TopNav from '../components/TopNav';

// 二级界面：某个城市的路线列表。带面包屑与城市内标签筛选（标签点击开新标签页，不打断当前浏览）。
export default function CityPage() {
  const { cityId = '' } = useParams();
  const [searchParams] = useSearchParams();
  const tag = searchParams.get('tag') ?? '';
  const [routes, setRoutes] = useState<Route[]>([]);
  const [tags, setTags] = useState<string[]>([]);
  const [error, setError] = useState('');
  const navigate = useNavigate();

  useEffect(() => {
    listRoutes({ city_id: cityId, tag })
      .then((r) => setRoutes(r.routes))
      .catch((e) => setError(String(e)));
  }, [cityId, tag]);

  // 该城市下出现过的全部标签，用于城内筛选
  useEffect(() => {
    listRoutes({ city_id: cityId })
      .then((r) => setTags([...new Set(r.routes.flatMap((x) => x.tags))].sort()))
      .catch(() => undefined);
  }, [cityId]);

  const cityName = decodeURIComponent(cityId).split(':')[1]?.replace(/-/g, ' ') ?? cityId;

  return (
    <div>
      <TopNav />
      <div className="page">
        <nav className="breadcrumb">
          <Link to="/">首页</Link>
          <span>›</span>
          <span>{cityName}</span>
          {tag && (
            <>
              <span>›</span>
              <span>{tag}</span>
            </>
          )}
        </nav>

        <h1 className="page-title">
          {cityName} · {routes.length} 条路线
        </h1>

        <div className="quick-filters">
          <span className="quick-label">标签</span>
          <Link to={`/city/${cityId}`} className={`chip ${tag === '' ? 'chip-active' : ''}`}>
            全部
          </Link>
          {tags.map((t) => (
            <a
              key={t}
              className={`chip ${tag === t ? 'chip-active' : ''}`}
              href={`#${`/city/${encodeURIComponent(cityId)}?tag=${encodeURIComponent(t)}`}`}
              target="_blank"
              rel="noreferrer"
            >
              {t}
            </a>
          ))}
        </div>

        {error && <div className="error">加载失败：{error}</div>}
        <div className="grid">
          {routes.map((r) => (
            <RouteCard key={r.route_id} route={r} onClick={() => navigate(`/route/${r.route_id}`)} />
          ))}
        </div>
        {routes.length === 0 && !error && <div className="empty">这个筛选条件下暂时没有路线</div>}
      </div>
    </div>
  );
}
