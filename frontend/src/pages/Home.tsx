import { useEffect, useMemo, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { listRoutes, Route } from '../api';
import RecentBar from '../components/RecentBar';

// 首页：Hero 大搜索 + “想去哪里玩？”城市卡片墙（Klook 式），点击城市进二级列表页。
// 城市与标签的 chips 筛选从这里撤掉，交给二级页。
export default function Home() {
  const [all, setAll] = useState<Route[]>([]);
  const [q, setQ] = useState('');
  const [error, setError] = useState('');
  const navigate = useNavigate();

  useEffect(() => {
    listRoutes({})
      .then((r) => setAll(r.routes))
      .catch((e) => setError(String(e)));
  }, []);

  // 城市卡片按路线数排序；卡片底色轮换几个渐变，避免单调
  const cities = useMemo(() => {
    const m = new Map<string, number>();
    all.forEach((r) => m.set(r.city_id, (m.get(r.city_id) ?? 0) + 1));
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [all]);

  // 城市封面：/data/cities.json 提供 city_id → image_url（批量补图工具生成，缺失则保持渐变）
  const [cityImages, setCityImages] = useState<Record<string, { image_url?: string }>>({});
  useEffect(() => {
    fetch('/data/cities.json')
      .then((r) => (r.ok ? r.json() : {}))
      .then(setCityImages)
      .catch(() => undefined);
  }, []);

  const cityName = (c: string) => c.split(':')[1].replace(/-/g, ' ');

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const query = q.trim();
    if (query) navigate(`/search?q=${encodeURIComponent(query)}`);
  };

  return (
    <div>
      <header className="hero">
        <div className="hero-inner">
          <h1>Journey Recommendation</h1>
          <form className="searchbar" onSubmit={submit}>
            <svg className="searchbar-icon" viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
              <circle cx="11" cy="11" r="7" />
              <path d="m20 20-3.5-3.5" />
            </svg>
            <input
              value={q}
              onChange={(e) => setQ(e.target.value)}
              placeholder="搜索路线、标签或城市，如：咖啡 / 日落 / kyoto"
            />
            <button type="submit">搜索</button>
          </form>
          <RecentBar />
        </div>
      </header>

      <div className="page">
        <h2 className="section-title">想去哪里玩？</h2>
        {error && <div className="error">加载失败：{error}</div>}
        <div className="city-grid">
          {cities.map(([cid, count], i) => {
            const img = cityImages[cid]?.image_url;
            return (
              <button
                key={cid}
                className={`city-card city-card-g${i % 4}`}
                onClick={() => navigate(`/city/${encodeURIComponent(cid)}`)}
              >
                {img && <img className="city-card-img" src={img} alt={cityName(cid)} loading="lazy" />}
                <span className="city-card-name">{cityName(cid)}</span>
                <span className="city-card-count">{count} 条路线</span>
              </button>
            );
          })}
        </div>
      </div>
    </div>
  );
}
