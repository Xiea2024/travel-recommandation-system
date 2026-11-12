import { useEffect, useState } from 'react';
import { Link, useNavigate, useSearchParams } from 'react-router-dom';
import { listRoutes, Route } from '../api';
import RouteCard from '../components/RouteCard';
import TopNav from '../components/TopNav';

// 搜索结果页：统一搜索（标题/标签/城市三路命中）的结果网格。
export default function SearchPage() {
  const [searchParams] = useSearchParams();
  const q = searchParams.get('q') ?? '';
  const [routes, setRoutes] = useState<Route[]>([]);
  const [error, setError] = useState('');
  const navigate = useNavigate();

  useEffect(() => {
    setRoutes([]);
    listRoutes({ q })
      .then((r) => setRoutes(r.routes))
      .catch((e) => setError(String(e)));
  }, [q]);

  return (
    <div>
      <TopNav initialQuery={q} />
      <div className="page">
        <nav className="breadcrumb">
          <Link to="/">首页</Link>
          <span>›</span>
          <span>搜索“{q}”</span>
        </nav>

        <h1 className="page-title">
          “{q}” 的搜索结果 · {routes.length} 条路线
        </h1>

        {error && <div className="error">加载失败：{error}</div>}
        <div className="grid">
          {routes.map((r) => (
            <RouteCard key={r.route_id} route={r} onClick={() => navigate(`/route/${r.route_id}`)} />
          ))}
        </div>
        {routes.length === 0 && !error && <div className="empty">没有找到相关路线，换个关键词试试</div>}
      </div>
    </div>
  );
}
