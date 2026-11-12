import { useEffect, useState } from 'react';
import { Link, useNavigate } from 'react-router-dom';

// 顶部导航：Klook 式常驻白底导航条，左侧品牌，右侧搜索框（在任意页搜索都跳搜索结果页）。
export default function TopNav({ initialQuery = '' }: { initialQuery?: string }) {
  const [q, setQ] = useState(initialQuery);
  const navigate = useNavigate();

  useEffect(() => setQ(initialQuery), [initialQuery]);

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    const query = q.trim();
    if (query) navigate(`/search?q=${encodeURIComponent(query)}`);
  };

  return (
    <div className="topnav">
      <div className="topnav-inner">
        <Link to="/" className="brand">
          Journey Recommendation
        </Link>
        <form className="topnav-search" onSubmit={submit}>
          <svg viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round">
            <circle cx="11" cy="11" r="7" />
            <path d="m20 20-3.5-3.5" />
          </svg>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="搜索路线、标签或城市" />
        </form>
      </div>
    </div>
  );
}
